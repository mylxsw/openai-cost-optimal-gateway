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
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/google/uuid"
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
		httpClient: &http.Client{Timeout: 30 * time.Minute},
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
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}

	route, ok := g.models[modelName]
	if !ok {
		if g.defaultProvider != nil {
			stream := gjson.GetBytes(bodyBytes, "stream").Bool()
			record, fwdErr := g.forwardRequest(w, r, *g.defaultProvider, modelName, bodyBytes, tokenCount, r.URL.Path, stream, reqType, 1, requestID, modelName)
			if record != nil {
				g.saveUsageRecord(r.Context(), *record)
			}
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
	for attemptIdx, candidate := range candidates {
		attempt := attemptIdx + 1
		provider, ok := g.providers[candidate.id]
		if !ok {
			err := fmt.Errorf("provider %s not found", candidate.id)
			lastErr = err
			if rec := g.prepareUsageRecord(candidate.id, candidate.model, modelName, r.URL.Path, requestID, tokenCount, 0, attempt); rec != nil {
				rec.Outcome = "failure"
				rec.Error = err.Error()
				rec.Duration = 0
				rec.FirstTokenLatency = 0
				g.saveUsageRecord(r.Context(), *rec)
			}
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
				if rec := g.prepareUsageRecord(provider.ID, targetModel, modelName, r.URL.Path, requestID, tokenCount, 0, attempt); rec != nil {
					rec.Outcome = "failure"
					rec.Error = err.Error()
					rec.Duration = 0
					g.saveUsageRecord(r.Context(), *rec)
				}
				continue
			}
		}

		record, err := g.forwardRequest(w, r, provider, targetModel, modifiedBody, tokenCount, r.URL.Path, stream, reqType, attempt, requestID, modelName)
		if record != nil {
			g.saveUsageRecord(r.Context(), *record)
		}
		if err != nil {
			lastErr = err
			if errors.Is(err, errShouldRetry) {
				log.Warningf("[%s] provider %s(%s) failed, we will try another provider: %v", modelName, candidate.id, candidate.model, err)
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
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func (e *retryableError) Unwrap() error {
	return errShouldRetry
}

func (g *Gateway) forwardRequest(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, model string, body []byte, tokenCount int, path string, stream bool, reqType RequestType, attempt int, requestID, originalModel string) (*storage.UsageRecord, error) {
	endpoint, err := joinURL(provider.BaseURL, strings.TrimPrefix(r.URL.Path, "/v1/"), r.URL.RawQuery)
	record := g.prepareUsageRecord(provider.ID, model, originalModel, path, requestID, tokenCount, 0, attempt)
	started := time.Now()
	if record != nil {
		record.CreatedAt = started
	}
	if err != nil {
		if record != nil {
			record.Outcome = "failure"
			record.Error = err.Error()
		}
		return record, fmt.Errorf("build provider url: %w", err)
	}

	ctx := r.Context()
	if provider.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, provider.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, endpoint, bytes.NewReader(body))
	if err != nil {
		if record != nil {
			record.Outcome = "failure"
			record.Error = err.Error()
		}
		return record, fmt.Errorf("create request: %w", err)
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

	resp, err := g.httpClient.Do(req)
	if err != nil {
		if record != nil {
			record.Outcome = "failure"
			record.Error = err.Error()
			record.Duration = time.Since(started)
		}
		return record, fmt.Errorf("[%s] forward request to %s: %w", model, provider.ID, err)
	}
	defer resp.Body.Close()

	isEventStream := isEventStreamResponse(resp.Header)
	if record != nil {
		record.StatusCode = resp.StatusCode
	}

	tracker := newFirstByteReader(resp.Body, started)

	if shouldRetryStatus(resp.StatusCode) {
		respBody, _ := io.ReadAll(tracker)
		if record != nil {
			record.Duration = time.Since(started)
			record.FirstTokenLatency = tracker.Latency()
			record.Outcome = "failure"
			record.Error = shortenErrorMessage(extractErrorMessage(respBody, resp.Header.Get("Content-Encoding"), resp.StatusCode))
			decoded := decodeBodyForAnalysis(respBody, resp.Header.Get("Content-Encoding"))
			providerReqID, completion := extractResponseMetadata(model, reqType, decoded, stream || isEventStream)
			if providerReqID != "" {
				record.ProviderRequestID = providerReqID
			}
			if completion > 0 {
				record.ResponseTokens = completion
			}
		}
		return record, &retryableError{
			providerID: provider.ID,
			status:     resp.StatusCode,
			header:     resp.Header.Clone(),
			body:       respBody,
		}
	}

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	var respBody []byte
	if stream || isEventStream {
		var buf bytes.Buffer
		writer := io.MultiWriter(w, &buf)
		if _, err = io.Copy(writer, tracker); err != nil {
			if record != nil {
				record.Outcome = "failure"
				record.Error = err.Error()
				record.Duration = time.Since(started)
				record.FirstTokenLatency = tracker.Latency()
			}
			return record, fmt.Errorf("[%s] stream response from %s: %w", model, provider.ID, err)
		}
		respBody = buf.Bytes()
	} else {
		data, readErr := io.ReadAll(tracker)
		if readErr != nil {
			if record != nil {
				record.Outcome = "failure"
				record.Error = readErr.Error()
				record.Duration = time.Since(started)
				record.FirstTokenLatency = tracker.Latency()
			}
			return record, fmt.Errorf("[%s] read response from %s: %w", model, provider.ID, readErr)
		}
		respBody = data
		if _, err = w.Write(respBody); err != nil {
			if record != nil {
				record.Outcome = "failure"
				record.Error = err.Error()
				record.Duration = time.Since(started)
				record.FirstTokenLatency = tracker.Latency()
			}
			return record, err
		}
	}

	if record != nil {
		record.Duration = time.Since(started)
		record.FirstTokenLatency = tracker.Latency()
		if record.Outcome == "" {
			record.Outcome = "success"
		}
		decoded := decodeBodyForAnalysis(respBody, resp.Header.Get("Content-Encoding"))
		providerReqID, completion := extractResponseMetadata(model, reqType, decoded, stream || isEventStream)
		if providerReqID != "" {
			record.ProviderRequestID = providerReqID
		}
		if completion > 0 {
			record.ResponseTokens = completion
		}
	}

	return record, nil
}

func shouldRetryStatus(status int) bool {
	return status >= 400
}

type firstByteReader struct {
	reader    io.Reader
	started   time.Time
	firstRead time.Time
}

func newFirstByteReader(r io.Reader, started time.Time) *firstByteReader {
	if started.IsZero() {
		started = time.Now()
	}
	return &firstByteReader{reader: r, started: started}
}

func (r *firstByteReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.firstRead.IsZero() {
		r.firstRead = time.Now()
	}
	return n, err
}

func (r *firstByteReader) Latency() time.Duration {
	if r.firstRead.IsZero() {
		return 0
	}
	return r.firstRead.Sub(r.started)
}

func isEventStreamResponse(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func decodeBodyForAnalysis(data []byte, encoding string) []byte {
	if len(data) == 0 {
		return data
	}
	if strings.Contains(strings.ToLower(encoding), "gzip") {
		if decoded, err := decodeGzip(data); err == nil {
			return decoded
		}
	}
	return data
}

func extractErrorMessage(body []byte, encoding string, status int) string {
	decoded := decodeBodyForAnalysis(body, encoding)
	if trimmed := strings.TrimSpace(string(decoded)); trimmed != "" {
		return trimmed
	}
	if status > 0 {
		if text := http.StatusText(status); text != "" {
			return text
		}
		return fmt.Sprintf("status %d", status)
	}
	if len(body) > 0 {
		return string(body)
	}
	return "request failed"
}

func shortenErrorMessage(msg string) string {
	const maxRunes = 512
	runes := []rune(msg)
	if len(runes) <= maxRunes {
		return msg
	}
	return string(runes[:maxRunes])
}

func extractResponseMetadata(model string, reqType RequestType, body []byte, isStream bool) (string, int) {
	if len(body) == 0 {
		return "", 0
	}
	encoding, err := tiktoken.EncodingForModel(model)
	if err != nil {
		encoding, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return "", 0
		}
	}

	texts, providerID := extractResponseTexts(reqType, isStream, body)
	if len(texts) == 0 {
		return providerID, 0
	}
	total := 0
	for _, txt := range texts {
		total += tokenLen(encoding, txt)
	}
	return providerID, total
}

func extractResponseTexts(reqType RequestType, isStream bool, body []byte) ([]string, string) {
	switch reqType {
	case RequestTypeChatCompletions:
		if isStream {
			return extractChatStreamTexts(body)
		}
		return extractChatResponseTexts(body)
	case RequestTypeResponses:
		if isStream {
			return extractResponsesStreamTexts(body)
		}
		return extractResponsesTexts(body)
	case RequestTypeAnthropicMessages:
		if isStream {
			return extractAnthropicStreamTexts(body)
		}
		return extractAnthropicTexts(body)
	default:
		return nil, ""
	}
}

func extractChatResponseTexts(body []byte) ([]string, string) {
	providerID := gjson.GetBytes(body, "id").String()
	choices := gjson.GetBytes(body, "choices")
	texts := make([]string, 0)
	if choices.Exists() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			var builder strings.Builder
			gatherText(&builder, choice.Get("message.content"))
			gatherText(&builder, choice.Get("content"))
			gatherText(&builder, choice.Get("text"))
			if out := strings.TrimSpace(builder.String()); out != "" {
				texts = append(texts, out)
			}
			return true
		})
	}
	return texts, providerID
}

func extractChatStreamTexts(body []byte) ([]string, string) {
	payloads := parseSSEPayloads(body)
	if len(payloads) == 0 {
		return nil, ""
	}
	builders := make(map[int]*strings.Builder)
	providerID := ""
	for _, payload := range payloads {
		res := gjson.ParseBytes(payload)
		if providerID == "" {
			providerID = res.Get("id").String()
			if providerID == "" {
				providerID = res.Get("response.id").String()
			}
		}
		res.Get("choices").ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			builder := builders[idx]
			if builder == nil {
				builder = &strings.Builder{}
				builders[idx] = builder
			}
			gatherText(builder, choice.Get("delta"))
			gatherText(builder, choice.Get("message"))
			gatherText(builder, choice.Get("content"))
			gatherText(builder, choice.Get("text"))
			return true
		})
	}
	return buildersToSlice(builders), providerID
}

func extractResponsesTexts(body []byte) ([]string, string) {
	providerID := gjson.GetBytes(body, "id").String()
	texts := make([]string, 0)
	outputText := gjson.GetBytes(body, "output_text")
	if outputText.Exists() {
		if outputText.IsArray() {
			outputText.ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String {
					texts = append(texts, item.String())
				}
				return true
			})
		} else if outputText.Type == gjson.String {
			texts = append(texts, outputText.String())
		}
	}
	gjson.GetBytes(body, "output").ForEach(func(_, output gjson.Result) bool {
		var builder strings.Builder
		gatherText(&builder, output.Get("content"))
		if out := strings.TrimSpace(builder.String()); out != "" {
			texts = append(texts, out)
		}
		return true
	})
	return texts, providerID
}

func extractResponsesStreamTexts(body []byte) ([]string, string) {
	payloads := parseSSEPayloads(body)
	if len(payloads) == 0 {
		return nil, ""
	}
	builders := make(map[int]*strings.Builder)
	providerID := ""
	for _, payload := range payloads {
		res := gjson.ParseBytes(payload)
		if providerID == "" {
			providerID = res.Get("id").String()
			if providerID == "" {
				providerID = res.Get("response.id").String()
			}
		}
		idx := int(res.Get("index").Int())
		builder := builders[idx]
		if builder == nil {
			builder = &strings.Builder{}
			builders[idx] = builder
		}
		gatherText(builder, res.Get("delta"))
		gatherText(builder, res.Get("text"))
		gatherText(builder, res.Get("output_text"))
		gatherText(builder, res.Get("content"))
	}
	return buildersToSlice(builders), providerID
}

func extractAnthropicTexts(body []byte) ([]string, string) {
	providerID := gjson.GetBytes(body, "id").String()
	var builder strings.Builder
	gatherText(&builder, gjson.GetBytes(body, "content"))
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return nil, providerID
	}
	return []string{text}, providerID
}

func extractAnthropicStreamTexts(body []byte) ([]string, string) {
	payloads := parseSSEPayloads(body)
	if len(payloads) == 0 {
		return nil, ""
	}
	var builder strings.Builder
	providerID := ""
	for _, payload := range payloads {
		res := gjson.ParseBytes(payload)
		if providerID == "" {
			providerID = res.Get("id").String()
			if providerID == "" {
				providerID = res.Get("message.id").String()
			}
		}
		typ := res.Get("type").String()
		switch typ {
		case "message_start", "message_delta", "content_block_delta", "content_block_start", "message_stop", "content_block_stop", "":
			gatherText(&builder, res)
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return nil, providerID
	}
	return []string{text}, providerID
}

func gatherText(builder *strings.Builder, node gjson.Result) {
	if !node.Exists() {
		return
	}
	if node.Type == gjson.String {
		builder.WriteString(node.String())
		return
	}
	if node.IsArray() {
		node.ForEach(func(_, item gjson.Result) bool {
			gatherText(builder, item)
			return true
		})
		return
	}
	keys := []string{"text", "content", "delta", "value"}
	for _, key := range keys {
		child := node.Get(key)
		if child.Exists() {
			gatherText(builder, child)
		}
	}
}

func buildersToSlice(builders map[int]*strings.Builder) []string {
	if len(builders) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(builders))
	for idx := range builders {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	texts := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		builder := builders[idx]
		if builder == nil {
			continue
		}
		if text := strings.TrimSpace(builder.String()); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

func parseSSEPayloads(body []byte) [][]byte {
	lines := bytes.Split(body, []byte("\n"))
	payloads := make([][]byte, 0)
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		payloads = append(payloads, payload)
	}
	return payloads
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
