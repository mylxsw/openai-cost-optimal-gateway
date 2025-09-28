package gateway

import (
	"context"
	"time"

	"github.com/mylxsw/asteria/log"
	"github.com/tidwall/gjson"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
)

func (g *Gateway) prepareUsageRecord(providerID, providerModel, originalModel, path, requestID string, tokenCount, statusCode, attempt int) *storage.UsageRecord {
	if g.usageStore == nil || !g.cfg.SaveUsage {
		return nil
	}
	if attempt <= 0 {
		attempt = 1
	}
	return &storage.UsageRecord{
		CreatedAt:     time.Now(),
		Provider:      providerID,
		Model:         providerModel,
		OriginalModel: originalModel,
		Path:          path,
		RequestTokens: tokenCount,
		StatusCode:    statusCode,
		RequestID:     requestID,
		Attempt:       attempt,
	}
}

func (g *Gateway) saveUsageRecord(ctx context.Context, record storage.UsageRecord) {
	if g.usageStore == nil || !g.cfg.SaveUsage {
		return
	}

	go func(rec storage.UsageRecord) {
		base := context.Background()
		if ctx != nil {
			base = context.WithoutCancel(ctx)
		}
		ctxWithTimeout, cancel := context.WithTimeout(base, 5*time.Second)
		defer cancel()
		if err := g.usageStore.RecordUsage(ctxWithTimeout, rec); err != nil {
			log.Warningf("save usage record: %v", err)
		}
	}(record)
}

func extractUsageTokens(body []byte) (int, int) {
	usage := gjson.GetBytes(body, "usage")
	if !usage.Exists() {
		return 0, 0
	}

	prompt := int(usage.Get("prompt_tokens").Int())
	if prompt == 0 {
		prompt = int(usage.Get("input_tokens").Int())
	}

	completion := int(usage.Get("completion_tokens").Int())
	if completion == 0 {
		completion = int(usage.Get("output_tokens").Int())
	}

	return prompt, completion
}
