package gateway

import (
	"context"
	"time"

	"github.com/mylxsw/asteria/log"
	"github.com/tidwall/gjson"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/storage"
)

func (g *Gateway) prepareUsageRecord(providerID, model, path string, tokenCount int, status int) *storage.UsageRecord {
	if g.usageStore == nil || !g.cfg.SaveUsage {
		return nil
	}

	return &storage.UsageRecord{
		CreatedAt:     time.Now(),
		Provider:      providerID,
		Model:         model,
		Path:          path,
		RequestTokens: tokenCount,
		Status:        status,
	}
}

func (g *Gateway) saveUsageRecord(ctx context.Context, record storage.UsageRecord) {
	if g.usageStore == nil || !g.cfg.SaveUsage {
		return
	}

	go func(rec storage.UsageRecord) {
		parent := context.Background()
		if ctx != nil && ctx.Err() == nil {
			parent = ctx
		}
		ctxWithTimeout, cancel := context.WithTimeout(parent, 5*time.Second)
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
