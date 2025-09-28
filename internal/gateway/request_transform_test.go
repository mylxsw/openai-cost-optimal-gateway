package gateway

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRequestBodyMultimodal(t *testing.T) {
	body := []byte(`{
                "model": "gpt-4o",
                "messages": [
                        {
                                "role": "user",
                                "content": [
                                        {"type": "text", "text": "hello"},
                                        {"type": "image", "image_url": {"url": "data:image/jpeg;base64,abc"}}
                                ]
                        }
                ]
        }`)

	normalized, changed, err := normalizeRequestBody(body, RequestTypeChatCompletions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected payload to change")
	}

	var payload struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatalf("unmarshal normalized payload: %v", err)
	}

	if got := payload.Messages[0].Content[1].Type; got != "image_url" {
		t.Fatalf("expected second content type to be image_url, got %s", got)
	}
}

func TestNormalizeRequestBodyToolContent(t *testing.T) {
	body := []byte(`{
                "model": "gpt-4o",
                "messages": [
                        {
                                "role": "tool",
                                "tool_call_id": "call_test",
                                "content": []
                        }
                ]
        }`)

	normalized, changed, err := normalizeRequestBody(body, RequestTypeChatCompletions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("expected payload to change")
	}

	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatalf("unmarshal normalized payload: %v", err)
	}

	if payload.Messages[0].Content != "[]" {
		t.Fatalf("expected tool content to be serialized array, got %q", payload.Messages[0].Content)
	}
}
