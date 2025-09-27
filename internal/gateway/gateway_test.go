package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mylxsw/openai-cost-optimal-gateway/internal/config"
)

func TestProxyRetriesProvidersOnServerError(t *testing.T) {
	firstCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(first.Close)

	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	t.Cleanup(second.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "first", BaseURL: first.URL, AccessToken: "token1"},
			{ID: "second", BaseURL: second.URL, AccessToken: "token2"},
		},
		Models: []config.ModelConfig{
			{Name: "gpt-3.5", Providers: []config.ModelProvider{{ID: "first"}, {ID: "second"}}},
		},
	}

	gw, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-3.5"}`)))
	rec := httptest.NewRecorder()

	gw.Proxy(rec, req, RequestTypeChatCompletions)

	if firstCalls != 1 {
		t.Fatalf("expected first provider to be called once, got %d", firstCalls)
	}
	if secondCalls != 1 {
		t.Fatalf("expected second provider to be called once, got %d", secondCalls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if rec.Body.String() != `{"id":"ok"}` {
		t.Fatalf("unexpected response body: %s", rec.Body.String())
	}
}

func TestProxyRetriesProviderOnContentFilter(t *testing.T) {
	firstCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		http.Error(w, `{"error":"content_filter"}`, http.StatusBadRequest)
	}))
	t.Cleanup(first.Close)

	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	t.Cleanup(second.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "first", BaseURL: first.URL, AccessToken: "token1"},
			{ID: "second", BaseURL: second.URL, AccessToken: "token2"},
		},
		Models: []config.ModelConfig{
			{Name: "gpt-3.5", Providers: []config.ModelProvider{{ID: "first"}, {ID: "second"}}},
		},
	}

	gw, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-3.5"}`)))
	rec := httptest.NewRecorder()

	gw.Proxy(rec, req, RequestTypeChatCompletions)

	if firstCalls != 1 {
		t.Fatalf("expected first provider to be called once, got %d", firstCalls)
	}
	if secondCalls != 1 {
		t.Fatalf("expected second provider to be called once, got %d", secondCalls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestProxyRetriesOnBadRequestWithoutSpecialError(t *testing.T) {
	firstCalls := 0
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls++
		http.Error(w, `{"error":"bad_request"}`, http.StatusBadRequest)
	}))
	t.Cleanup(first.Close)

	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	t.Cleanup(second.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "first", BaseURL: first.URL, AccessToken: "token1"},
			{ID: "second", BaseURL: second.URL, AccessToken: "token2"},
		},
		Models: []config.ModelConfig{
			{Name: "gpt-3.5", Providers: []config.ModelProvider{{ID: "first"}, {ID: "second"}}},
		},
	}

	gw, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-3.5"}`)))
	rec := httptest.NewRecorder()

	gw.Proxy(rec, req, RequestTypeChatCompletions)

	if firstCalls != 1 {
		t.Fatalf("expected first provider to be called once, got %d", firstCalls)
	}
	if secondCalls != 1 {
		t.Fatalf("expected second provider to be called once, got %d", secondCalls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestProxyReturnsBadRequestWhenAllProvidersFail(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad_request"}`, http.StatusBadRequest)
	}))
	t.Cleanup(first.Close)

	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "first", BaseURL: first.URL, AccessToken: "token1"},
		},
		Models: []config.ModelConfig{
			{Name: "gpt-3.5", Providers: []config.ModelProvider{{ID: "first"}}},
		},
	}

	gw, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-3.5"}`)))
	rec := httptest.NewRecorder()

	gw.Proxy(rec, req, RequestTypeChatCompletions)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Fatalf("expected response body, got empty")
	}
}
