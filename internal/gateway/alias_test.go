package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
	"github.com/tidwall/gjson"
)

func TestProxyAliasResolution(t *testing.T) {
	// Mock provider
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := gjson.GetBytes(body, "model").String()
		if model != "target-model" {
			t.Errorf("expected model 'target-model', got '%s'", model)
			http.Error(w, "wrong model", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer providerServer.Close()

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "p1", BaseURL: providerServer.URL, AccessToken: "token"},
		},
		Models: []config.ModelConfig{
			{Name: "target-model", Providers: []config.ModelProvider{{ID: "p1"}}},
		},
		Alias: []config.AliasConfig{
			{Model: "alias-model", Target: "target-model"},
		},
	}

	gw, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	// Test Proxy
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"alias-model"}`)))
	rec := httptest.NewRecorder()

	gw.Proxy(rec, req, RequestTypeChatCompletions)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Test ModelList
	listResp := gw.ModelList()
	found := false
	for _, m := range listResp.Data {
		if m.ID == "alias-model" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("alias-model not found in ModelList")
	}
}
