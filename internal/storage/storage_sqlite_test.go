package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteStoreRecordAndQuery(t *testing.T) {
	dir := t.TempDir()
	uri := fmt.Sprintf("file:%s", filepath.Join(dir, "usage.db"))

	store, err := New(context.Background(), "sqlite", uri)
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close(context.Background())
	})

	record := UsageRecord{
		Path:           "/v1/chat/completions",
		Provider:       "provider-a",
		Model:          "gpt-4o",
		RequestTokens:  42,
		ResponseTokens: 11,
		Status:         200,
		Duration:       time.Second,
	}
	if err := store.RecordUsage(context.Background(), record); err != nil {
		t.Fatalf("record usage: %v", err)
	}

	records, err := store.QueryUsage(context.Background(), 10)
	if err != nil {
		t.Fatalf("query usage: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	got := records[0]
	if got.Provider != record.Provider || got.Model != record.Model || got.Path != record.Path {
		t.Fatalf("unexpected record: %+v", got)
	}
	if got.RequestTokens != record.RequestTokens || got.ResponseTokens != record.ResponseTokens {
		t.Fatalf("unexpected tokens: %+v", got)
	}
	if got.Status != record.Status {
		t.Fatalf("unexpected status: %d", got.Status)
	}
	if got.Duration != record.Duration {
		t.Fatalf("unexpected duration: %s", got.Duration)
	}
}
