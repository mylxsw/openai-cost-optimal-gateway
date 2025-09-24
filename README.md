# OpenAI Cost Optimal Gateway

[English](README.md) | [中文](README.zh.md)

OpenAI Cost Optimal Gateway is a Go service that proxies OpenAI- and Anthropic-compatible API requests to multiple upstream providers. It adds rule-driven routing so that traffic can be steered to the most cost-effective provider while keeping API compatibility with official OpenAI and Anthropic clients.

## Features

- **Drop-in API gateway** &mdash; Exposes `/v1/chat/completions`, `/v1/responses`, `/v1/messages`, and `/v1/models` endpoints that mimic OpenAI or Anthropic semantics so existing SDKs keep working.
- **Provider abstraction** &mdash; Supports any provider that implements the same REST contract, including OpenAI, Azure OpenAI, Anthropic, and third-party resellers.
- **Rule-based routing** &mdash; Evaluates configurable expressions (powered by [`expr`](https://github.com/expr-lang/expr)) to pick the best provider/model combination per request.
- **Token-aware decisions** &mdash; Counts tokens per request to drive routing logic, enabling automatic fallbacks when prompts exceed context windows.
- **Gateway API keys** &mdash; Protects the gateway itself with shared API keys that clients must provide via the `Authorization` header.
- **Observability helpers** &mdash; Includes structured logging and panic recovery middleware.

## Architecture Overview

```text
client ──► gateway server ──► selected provider (OpenAI/Azure/Anthropic/...)
             │
             └─► routing rules + token counting
```

The service boots with a configuration file that defines:

- Gateway listen address and API keys
- A list of providers with base URLs, access tokens, and optional headers/timeouts
- Logical models with default provider order
- Optional routing rules that override provider/model pairs when an expression matches

The gateway uses this configuration to build an in-memory routing table. Requests are authenticated, the body is inspected to resolve the `model` name, routing rules are evaluated, and the request is forwarded to the chosen upstream provider.

## Getting Started

### Prerequisites

- Go 1.21+
- Access tokens for the providers you plan to proxy

### Clone and build

```bash
git clone https://github.com/mylxsw/openai-cost-optimal-gateway.git
cd openai-cost-optimal-gateway
go build ./cmd/gateway
```

### Configure the gateway

Copy `config.example.yaml` and edit it to fit your environment:

```bash
cp config.example.yaml config.yaml
```

Key sections of the configuration:

- `listen`: Address the HTTP server binds to.
- `api_keys`: Gateway API keys clients must present. Multiple keys are supported.
- `providers`: Upstream providers, each with `id`, `base_url`, `access_token`, optional `headers`, and `timeout`.
- `models`: Logical models exposed by the gateway, listing default providers and optional `rules`.
- `rules`: Expressions evaluated with the following environment:
  - `TokenCount`: Counted tokens for the request payload.
  - `Model`: Requested model name.
  - `Path`: Request path (e.g., `/v1/chat/completions`).

Rules can return either an array of provider overrides or an object map for convenience. Overrides accept an `id` (matching a provider) and an optional `model` that replaces the outbound `model` field.

### Run the gateway

```bash
./gateway -config config.yaml
```

The server listens on the configured `listen` address. Clients must include `Authorization: Bearer <gateway-api-key>` and can call the standard OpenAI endpoints using the logical models defined in `config.yaml`.

## API Endpoints

| Path | Method | Description |
| --- | --- | --- |
| `/healthz` | GET | Health probe returning `ok` when the service is running. |
| `/v1/chat/completions` | POST | Proxies OpenAI Chat Completions requests. |
| `/v1/responses` | POST | Proxies OpenAI Assistants Responses requests. |
| `/v1/messages` | POST | Proxies Anthropic Messages requests. |
| `/v1/models` | GET | Lists logical models exposed by the gateway. |

## Development

Run unit tests before submitting changes:

```bash
go test ./...
```

## License

This project is licensed under the [MIT License](LICENSE).
