package gateway

import (
	"bytes"
	"compress/gzip"
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
	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
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
	usageStore      storage.Store
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

func New(cfg *config.Config, usageStore storage.Store) (*Gateway, error) {
	gw := &Gateway{
		cfg:        cfg,
		providers:  make(map[string]config.ProviderConfig),
		models:     make(map[string]*modelRoute),
		httpClient: &http.Client{Timeout: 2 * time.Minute},
		usageStore: usageStore,
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
				providers = append(providers, ruleProvider{id: override.Provider, model: override.Model})
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

	normalized, changed, err := normalizeRequestBody(bodyBytes, reqType)
	if err != nil {
		http.Error(w, fmt.Sprintf("normalize request body: %v", err), http.StatusBadRequest)
		return
	}
	if changed {
		bodyBytes = normalized
	}

	if log.DebugEnabled() {
		log.Debug("request body: ", string(bodyBytes))
	}

	modelName := gjson.GetBytes(bodyBytes, "model").String()
	if modelName == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}

	tokenCount := CountTokens(modelName, reqType, bodyBytes)

	route, ok := g.models[modelName]
	if !ok {
		if g.defaultProvider != nil {
			stream := gjson.GetBytes(bodyBytes, "stream").Bool()
			record, fwdErr := g.forwardRequest(w, r, *g.defaultProvider, modelName, bodyBytes, tokenCount, r.URL.Path, stream)
			if fwdErr != nil {
				log.Errorf("forward to default provider: %v", fwdErr)
				status := http.StatusBadGateway
				if errors.Is(fwdErr, errShouldRetry) {
					http.Error(w, fwdErr.Error(), status)
				} else {
					http.Error(w, fmt.Sprintf("forward to default provider: %v", fwdErr), status)
				}
				return
			}
			if record != nil {
				g.saveUsageRecord(r.Context(), *record)
			}
			return
		}
		http.Error(w, fmt.Sprintf("model %s not configured", modelName), http.StatusNotFound)
		return
	}

	candidates := g.selectProviders(route, modelName, tokenCount, r.URL.Path)
	if len(candidates) == 0 {
		http.Error(w, "no provider available", http.StatusBadGateway)
		return
	}

	log.Debugf("[%s] select providers: %v", modelName, candidates)

	var lastErr error
	stream := gjson.GetBytes(bodyBytes, "stream").Bool()
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
			modifiedBody, err = sjson.SetBytes(bodyBytes, "model", targetModel)
			if err != nil {
				lastErr = fmt.Errorf("modify request body: %w", err)
				continue
			}
		}

		record, err := g.forwardRequest(w, r, provider, targetModel, modifiedBody, tokenCount, r.URL.Path, stream)
		if err != nil {
			lastErr = err
			if errors.Is(err, errShouldRetry) {
				log.Warningf("[%s] provider %s(%s) failed, we will try another provider: %v", modelName, candidate.id, candidate.model, err)
				continue
			}

			return
		}
		if record != nil {
			g.saveUsageRecord(r.Context(), *record)
		}
		return
	}

	status := http.StatusBadGateway
	if lastErr == nil {
		lastErr = fmt.Errorf("no available provider")
	}

	var retryErr *retryableError
	if errors.As(lastErr, &retryErr) {
		copyResponseHeaders(w.Header(), retryErr.header)
		w.WriteHeader(retryErr.status)
		if len(retryErr.body) > 0 {
			_, _ = w.Write(retryErr.body)
		}
		return
	}

	http.Error(w, lastErr.Error(), status)
}

var errShouldRetry = errors.New("should retry")

type retryableError struct {
	providerID string
	status     int
	header     http.Header
	body       []byte
}

func (e *retryableError) Error() string {
	bodyStr := string(e.body)
	if contentEncoding := e.header.Get("Content-Encoding"); strings.Contains(strings.ToLower(contentEncoding), "gzip") {
		if decodedBody, err := decodeGzip(e.body); err == nil {
			bodyStr = string(decodedBody)
		}
	}

	return fmt.Sprintf("provider %s returned status %d, body: %s", e.providerID, e.status, bodyStr)
}

func decodeGzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (e *retryableError) Unwrap() error {
	return errShouldRetry
}

func (g *Gateway) forwardRequest(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, model string, body []byte, tokenCount int, path string, stream bool) (*storage.UsageRecord, error) {
	endpoint, err := joinURL(provider.BaseURL, strings.TrimPrefix(r.URL.Path, "/v1/"), r.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("build provider url: %w", err)
	}

	ctx := r.Context()
	if provider.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, provider.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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

	log.Debugf("[%s] forward request to %s, url: %s", model, provider.ID, endpoint)

	started := time.Now()
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("[%s] forward request to %s: %w", model, provider.ID, err)
	}
	defer resp.Body.Close()

	responseRecord := g.prepareUsageRecord(provider.ID, model, path, tokenCount, resp.StatusCode)

	isEventStream := strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
	var respBody []byte
	if shouldRetryStatus(resp.StatusCode) {
		respBody, _ = io.ReadAll(resp.Body)
		return nil, &retryableError{
			providerID: provider.ID,
			status:     resp.StatusCode,
			header:     resp.Header.Clone(),
			body:       respBody,
		}
	}

	if !stream && !isEventStream {
		respBody, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("[%s] read response from %s: %w", model, provider.ID, err)
		}
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if respBody != nil {
		if _, err = w.Write(respBody); err != nil {
			return nil, err
		}
	} else {
		if _, err = io.Copy(w, resp.Body); err != nil {
			return nil, err
		}
	}

	if responseRecord != nil {
		responseRecord.Duration = time.Since(started)
		if len(respBody) > 0 {
			prompt, completion := extractUsageTokens(respBody)
			if prompt > 0 {
				responseRecord.RequestTokens = prompt
			}
			if completion > 0 {
				responseRecord.ResponseTokens = completion
			}
		}
	}

	return responseRecord, nil
}

func shouldRetryStatus(status int) bool {
	return status >= 400
}

func (g *Gateway) selectProviders(route *modelRoute, model string, tokenCount int, path string) []ruleProvider {
	env := EvalEnv{TokenCount: tokenCount, Model: model, Path: path}
	for _, rule := range route.rules {
		out, err := vm.Run(rule.program, env)
		if err != nil {
			log.Warningf("eval rule %v", err)
			continue
		}

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

	baseSegments := splitPathSegments(baseURL.Path)
	reqSegments := splitPathSegments(path)

	// Remove overlapping path segments so that paths like /v1/... are not duplicated
	// when the provider base URL already ends with /v1.
	maxOverlap := min(len(baseSegments), len(reqSegments))
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if hasSuffixPrefixOverlap(baseSegments, reqSegments, overlap) {
			reqSegments = reqSegments[overlap:]
			break
		}
	}

	merged := append(append([]string(nil), baseSegments...), reqSegments...)

	var joinedPath string
	if len(merged) > 0 {
		joinedPath = "/" + strings.Join(merged, "/")
	}

	target := *baseURL
	target.Path = joinedPath
	target.RawPath = ""
	target.RawQuery = rawQuery

	return target.String(), nil
}

func splitPathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func hasSuffixPrefixOverlap(baseSegments, reqSegments []string, overlap int) bool {
	if overlap == 0 {
		return true
	}
	if overlap > len(baseSegments) || overlap > len(reqSegments) {
		return false
	}
	baseStart := len(baseSegments) - overlap
	for i := 0; i < overlap; i++ {
		if baseSegments[baseStart+i] != reqSegments[i] {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	endpoint, err := joinURL(provider.BaseURL, "/models", "")
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
