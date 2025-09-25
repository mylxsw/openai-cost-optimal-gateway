package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/mylxsw/asteria/log"
	tiktoken "github.com/pkoukk/tiktoken-go"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
)

type RequestType int

const (
	RequestTypeChatCompletions RequestType = iota
	RequestTypeResponses
	RequestTypeAnthropicMessages
)

type Gateway struct {
	cfg             *config.Config
	providers       map[string]config.ProviderConfig
	models          map[string]*modelRoute
	httpClient      *http.Client
	modelList       []ModelInfo
	defaultProvider *config.ProviderConfig
}

type modelRoute struct {
	config config.ModelConfig
	rules  []compiledRule
}

type compiledRule struct {
	program   *vm.Program
	providers []ruleProvider
}

type ruleProvider struct {
	id    string
	model string
}

type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelListResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

type EvalEnv struct {
	TokenCount int
	Model      string
	Path       string
}

func New(cfg *config.Config) (*Gateway, error) {
	gw := &Gateway{
		cfg:        cfg,
		providers:  make(map[string]config.ProviderConfig),
		models:     make(map[string]*modelRoute),
		httpClient: &http.Client{Timeout: 2 * time.Minute},
	}

	for _, p := range cfg.Providers {
		gw.providers[p.ID] = p
	}

	if cfg.Default != "" {
		if provider, ok := gw.providers[cfg.Default]; ok {
			p := provider
			gw.defaultProvider = &p
		}
	}

	created := time.Now().Unix()
	for _, m := range cfg.Models {
		mr := &modelRoute{config: m}
		for _, r := range m.Rules {
			program, err := expr.Compile(r.Expression, expr.Env(EvalEnv{}), expr.AsBool())
			if err != nil {
				return nil, fmt.Errorf("compile rule %s for model %s: %w", r.Expression, m.Name, err)
			}
			var providers []ruleProvider
			for _, override := range r.Providers {
				providers = append(providers, ruleProvider{id: override.ID, model: override.Model})
			}
			mr.rules = append(mr.rules, compiledRule{program: program, providers: providers})
		}
		gw.models[m.Name] = mr
		gw.modelList = append(gw.modelList, ModelInfo{
			ID:      m.Name,
			Object:  "model",
			Created: created,
			OwnedBy: "openai-cost-optimal-gateway",
		})
	}

	return gw, nil
}

func (g *Gateway) ModelList() ModelListResponse {
	data := make([]ModelInfo, 0, len(g.modelList))
	seen := make(map[string]struct{}, len(g.modelList))
	for _, model := range g.modelList {
		data = append(data, model)
		seen[model.ID] = struct{}{}
	}

	if g.defaultProvider != nil {
		if models, err := g.fetchProviderModels(*g.defaultProvider); err != nil {
			log.Errorf("fetch default provider models: %v", err)
		} else {
			for _, model := range models {
				if _, ok := seen[model.ID]; ok {
					continue
				}
				data = append(data, model)
				seen[model.ID] = struct{}{}
			}
		}
	}

	return ModelListResponse{
		Object: "list",
		Data:   data,
	}
}

func (g *Gateway) Proxy(w http.ResponseWriter, r *http.Request, reqType RequestType) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	modelName := gjson.GetBytes(bodyBytes, "model").String()
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	route, ok := g.models[modelName]
	if !ok {
		if g.defaultProvider != nil {
			if err := g.forwardRequest(w, r, *g.defaultProvider, bodyBytes); err != nil {
				log.Errorf("forward to default provider: %v", err)
				status := http.StatusBadGateway
				if errors.Is(err, errShouldRetry) {
					http.Error(w, err.Error(), status)
				} else {
					http.Error(w, fmt.Sprintf("forward to default provider: %v", err), status)
				}
				return
			}
			return
		}
		http.Error(w, fmt.Sprintf("model %s not configured", modelName), http.StatusNotFound)
		return
	}

	tokenCount := CountTokens(modelName, reqType, bodyBytes)

	log.Debugf("model: %s, token count: %d, path: %s", modelName, tokenCount, r.URL.Path)

	candidates := g.selectProviders(route, modelName, tokenCount, r.URL.Path)
	if len(candidates) == 0 {
		http.Error(w, "no provider available", http.StatusBadGateway)
		return
	}

	log.Debugf("select providers: %v", candidates)

	var lastErr error
	for _, candidate := range candidates {
		provider, ok := g.providers[candidate.id]
		if !ok {
			lastErr = fmt.Errorf("provider %s not found", candidate.id)
			continue
		}

		targetModel := modelName
		if candidate.model != "" {
			targetModel = candidate.model
		}
		modifiedBody := bodyBytes
		if targetModel != modelName {
			if reqType == RequestTypeAnthropicMessages {
				modifiedBody, err = sjson.SetBytes(bodyBytes, "model", targetModel)
			} else {
				modifiedBody, err = sjson.SetBytes(bodyBytes, "model", targetModel)
			}
			if err != nil {
				lastErr = fmt.Errorf("modify request body: %w", err)
				continue
			}
		}

		if err := g.forwardRequest(w, r, provider, modifiedBody); err != nil {
			lastErr = err
			if errors.Is(err, errShouldRetry) {
				continue
			}
			return
		}
		return
	}

	status := http.StatusBadGateway
	if lastErr == nil {
		lastErr = fmt.Errorf("no available provider")
	}
	http.Error(w, lastErr.Error(), status)
}

var errShouldRetry = errors.New("should retry")

func (g *Gateway) forwardRequest(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, body []byte) error {
	endpoint, err := joinURL(provider.BaseURL, r.URL.Path, r.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("build provider url: %w", err)
	}

	ctx := r.Context()
	if provider.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, provider.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	copyHeaders(req.Header, r.Header)

	if provider.Type == config.ProviderTypeAnthropic {
		req.Header.Set("x-api-key", provider.AccessToken)
		req.Header.Del("Authorization")
	} else {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", provider.AccessToken))
		req.Header.Del("x-api-key")
	}
	req.Host = req.URL.Host
	req.ContentLength = int64(len(body))
	if provider.Headers != nil {
		for k, v := range provider.Headers {
			req.Header.Set(k, v)
		}
	}

	log.Debugf("forward request to %s, method: %s, url: %s", provider.ID, r.Method, endpoint)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("forward request to %s: %w", provider.ID, err)
	}
	defer resp.Body.Close()

	if shouldRetry(resp.StatusCode) {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("provider %s returned status %d: %w", provider.ID, resp.StatusCode, errShouldRetry)
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func shouldRetry(status int) bool {
	if status >= 500 {
		return true
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

func (g *Gateway) selectProviders(route *modelRoute, model string, tokenCount int, path string) []ruleProvider {
	env := EvalEnv{TokenCount: tokenCount, Model: model, Path: path}
	for _, rule := range route.rules {
		out, err := vm.Run(rule.program, env)
		if err != nil {
			log.Debugf("eval rule %v", err)
			continue
		}

		log.Debugf("rule %s result: %v", rule.program.Source(), out)
		if matched, ok := out.(bool); ok && matched {
			return rule.providers
		}
	}

	providers := make([]ruleProvider, 0, len(route.config.Providers))
	for _, provider := range route.config.Providers {
		providers = append(providers, ruleProvider{id: provider.ID, model: provider.Model})
	}
	return providers
}

func joinURL(base, path, rawQuery string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	target := baseURL.ResolveReference(rel)
	target.RawQuery = rawQuery
	return target.String(), nil
}

func copyHeaders(dst, src http.Header) {
	dst.Del("Content-Length")
	dst.Del("Authorization")
	dst.Del("x-api-key")
	for k, values := range src {
		switch strings.ToLower(k) {
		case "content-length", "authorization", "x-api-key", "host":
			continue
		}
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k := range dst {
		dst.Del(k)
	}
	for k, values := range src {
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func (g *Gateway) fetchProviderModels(provider config.ProviderConfig) ([]ModelInfo, error) {
	endpoint, err := joinURL(provider.BaseURL, "/v1/models", "")
	if err != nil {
		return nil, fmt.Errorf("build provider url: %w", err)
	}

	ctx := context.Background()
	if provider.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, provider.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if provider.Type == config.ProviderTypeAnthropic {
		req.Header.Set("x-api-key", provider.AccessToken)
	} else {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", provider.AccessToken))
	}
	if provider.Headers != nil {
		for k, v := range provider.Headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models from %s: %w", provider.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider %s returned status %d: %s", provider.ID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode provider response: %w", err)
	}

	return result.Data, nil
}

func CountTokens(model string, reqType RequestType, body []byte) int {
	encoding, err := tiktoken.EncodingForModel(model)
	if err != nil {
		encoding, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return 0
		}
	}

	switch reqType {
	case RequestTypeChatCompletions:
		return countChatTokens(encoding, body)
	case RequestTypeResponses:
		return countResponsesTokens(encoding, body)
	case RequestTypeAnthropicMessages:
		return countAnthropicTokens(encoding, body)
	default:
		return 0
	}
}

func countChatTokens(enc *tiktoken.Tiktoken, body []byte) int {
	total := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, value gjson.Result) bool {
		if role := value.Get("role"); role.Exists() {
			total += tokenLen(enc, role.String())
		}
		if content := value.Get("content"); content.Exists() {
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("type").String() == "text" {
						total += tokenLen(enc, item.Get("text").String())
					}
					return true
				})
			} else {
				total += tokenLen(enc, content.String())
			}
		}
		return true
	})
	if system := gjson.GetBytes(body, "system"); system.Exists() {
		total += tokenLen(enc, system.String())
	}
	if prompt := gjson.GetBytes(body, "prompt"); prompt.Exists() {
		total += tokenLen(enc, prompt.String())
	}
	return total
}

func countResponsesTokens(enc *tiktoken.Tiktoken, body []byte) int {
	total := 0
	input := gjson.GetBytes(body, "input")
	if input.Exists() {
		if input.IsArray() {
			input.ForEach(func(_, value gjson.Result) bool {
				total += tokenLen(enc, value.String())
				return true
			})
		} else {
			total += tokenLen(enc, input.String())
		}
	}
	if instructions := gjson.GetBytes(body, "instructions"); instructions.Exists() {
		total += tokenLen(enc, instructions.String())
	}
	total += countChatTokens(enc, body)
	return total
}

func countAnthropicTokens(enc *tiktoken.Tiktoken, body []byte) int {
	total := 0
	gjson.GetBytes(body, "messages").ForEach(func(_, value gjson.Result) bool {
		if content := value.Get("content"); content.Exists() {
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("type").String() == "text" {
						total += tokenLen(enc, item.Get("text").String())
					}
					return true
				})
			} else {
				total += tokenLen(enc, content.String())
			}
		}
		return true
	})
	if system := gjson.GetBytes(body, "system"); system.Exists() {
		total += tokenLen(enc, system.String())
	}
	return total
}

func tokenLen(enc *tiktoken.Tiktoken, text string) int {
	if text == "" {
		return 0
	}
	tokens := enc.Encode(text, nil, nil)
	return len(tokens)
}
