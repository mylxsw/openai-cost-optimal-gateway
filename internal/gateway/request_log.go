package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mylxsw/asteria/log"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
)

func (g *Gateway) saveRequestLog(ctx context.Context, r *http.Request, body []byte, requestID string) {
	if g.usageStore == nil || !g.cfg.SaveUsage {
		return
	}

	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	entry := storage.RequestLog{
		CreatedAt: time.Now(),
		RequestID: requestID,
		Method:    r.Method,
		Path:      path,
		Headers:   sanitizeHeaders(r.Header),
		Body:      string(body),
	}

	go func(logEntry storage.RequestLog) {
		base := context.Background()
		if ctx != nil {
			base = context.WithoutCancel(ctx)
		}
		ctxWithTimeout, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		if err := g.usageStore.RecordRequestLog(ctxWithTimeout, logEntry); err != nil {
			log.Warningf("save request log: %v", err)
		}
	}(entry)
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	if headers == nil {
		return nil
	}
	sanitized := make(map[string][]string, len(headers))
	for k, values := range headers {
		cleanVals := make([]string, 0, len(values))
		for _, v := range values {
			switch strings.ToLower(k) {
			case "authorization":
				cleanVals = append(cleanVals, maskAuthorizationValue(v))
			case "x-api-key":
				cleanVals = append(cleanVals, maskToken(v))
			default:
				cleanVals = append(cleanVals, v)
			}
		}
		sanitized[k] = cleanVals
	}
	return sanitized
}

func maskAuthorizationValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return maskToken(parts[0])
	}

	token := parts[len(parts)-1]
	parts[len(parts)-1] = maskToken(token)
	return strings.Join(parts, " ")
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	const prefix = 4
	const suffix = 4
	if len(token) <= prefix+suffix {
		return strings.Repeat("*", len(token))
	}
	return token[:prefix] + strings.Repeat("*", len(token)-prefix-suffix) + token[len(token)-suffix:]
}
