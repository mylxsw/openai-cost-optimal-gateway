# OpenAI 成本最优网关

[English](README.md) | [中文](README.zh.md)

OpenAI 成本最优网关是一个使用 Go 编写的代理服务，可以将兼容 OpenAI 与 Anthropic 的 API 请求转发到多个上游服务提供方。通过自定义的规则路由机制，网关能够在保持与官方 SDK 兼容的同时，将流量引导到成本最合适的提供方。

## 功能特性

- **即插即用的 API 网关** —— 暴露 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 与 `/v1/models` 等端点，行为与 OpenAI / Anthropic 接口保持一致，现有客户端可直接接入。
- **统一的提供方抽象** —— 支持任何遵循相同 REST 协议的提供方，包括 OpenAI、Azure OpenAI、Anthropic 以及第三方代理商。
- **基于规则的路由** —— 使用 [`expr`](https://github.com/expr-lang/expr) 表达式评估路由规则，根据请求动态挑选最优的提供方与模型组合。
- **基于 Token 的决策** —— 统计请求中的 Token 数量，将其作为路由条件，帮助在输入超出上下文长度时自动切换模型或提供方。
- **网关级 API Key** —— 网关自身通过共享的 API Key 鉴权，客户端需在 `Authorization` 请求头中携带。
- **观测性增强** —— 内置结构化日志与 panic 恢复中间件，便于排查问题。

## 架构概览

```text
client ──► gateway server ──► selected provider (OpenAI/Azure/Anthropic/...)
             │
             └─► routing rules + token counting
```

服务启动时会加载配置文件，该文件定义：

- 网关监听地址与访问 API Key
- 上游提供方列表，包含基础 URL、访问令牌以及可选的请求头/超时时间
- 对外暴露的逻辑模型以及默认的提供方顺序
- 可选的路由规则，用于在表达式命中时覆盖默认的提供方/模型

网关根据这些配置构建内存中的路由表。请求在进入网关时会先通过鉴权，然后读取请求体中的 `model` 字段，依次评估路由规则，最终将请求转发到被选中的上游提供方。

## 快速开始

### 环境要求

- Go 1.21 及以上版本
- 计划接入的各上游服务提供方访问令牌

### 克隆与编译

```bash
git clone https://github.com/mylxsw/openai-cost-optimal-gateway.git
cd openai-cost-optimal-gateway
go build ./cmd/gateway
```

### 配置网关

复制示例配置文件并按实际情况修改：

```bash
cp config.example.yaml config.yaml
```

配置文件的关键字段：

- `listen`：HTTP 服务监听的地址。
- `api_keys`：访问网关所需的 API Key，可配置多个。
- `providers`：上游提供方列表，每项包含 `id`、`base_url`、`access_token` 以及可选的 `headers`、`timeout`。
- `models`：网关对外暴露的逻辑模型，包含默认提供方和可选的 `rules`。
- `rules`：基于以下环境变量的表达式：
  - `TokenCount`：请求推测出的 Token 数。
  - `Model`：请求的模型名称。
  - `Path`：请求路径（例如 `/v1/chat/completions`）。

规则可以返回提供方覆盖数组，或使用对象映射的简写形式。每个覆盖项需指定提供方 `id`，并可选指定新的下游 `model`。

### 启动网关

```bash
./gateway -config config.yaml
```

服务会监听配置的地址。客户端需在请求头中携带 `Authorization: Bearer <gateway-api-key>`，即可使用标准 OpenAI 接口调用配置中的逻辑模型。

## API 接口

| Path | Method | 描述 |
| --- | --- | --- |
| `/healthz` | GET | 健康检查接口，返回 `ok` 表示运行正常。 |
| `/v1/chat/completions` | POST | 代理 OpenAI Chat Completions 请求。 |
| `/v1/responses` | POST | 代理 OpenAI Assistants Responses 请求。 |
| `/v1/messages` | POST | 代理 Anthropic Messages 请求。 |
| `/v1/models` | GET | 返回网关暴露的逻辑模型列表。 |
| `/usage` | GET | 在启用日志时返回近期的用量记录。 |
| `/dashboard` | GET | 内嵌的 React 仪表盘，可视化展示用量数据。 |

## 用量统计与仪表盘

在配置中设置 `save_usage: true` 即可为每次代理请求记录 Token 用量。网关通过 `sqlite3` 命令行工具将数据写入 SQLite 数据库，因此需要保证运行环境的 `PATH` 中可以找到 `sqlite3`。默认的 `storage_uri`（例如 `file:usage.db?...`）会在当前目录生成数据库文件。如果指定 `storage_type: mysql`，目前仍会退回到按照 MySQL DSN 生成文件名的 JSON 文件存储。

启用用量记录后，会额外开放两个需要 API Key 授权的管理端点：

- `GET /usage`：返回原始记录以及聚合的 Token 统计。
- `GET /dashboard`：提供内嵌的 React 仪表盘页面，无需外部静态资源即可查看图表。

## 开发说明

提交代码前建议先运行单元测试：

```bash
go test ./...
```

## 许可证

本项目基于 [MIT License](LICENSE) 开源。
