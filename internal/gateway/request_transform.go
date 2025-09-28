package gateway

import (
	"encoding/json"
	"strings"
)

// normalizeRequestBody mutates chat style payloads so they conform to the
// provider expectations. It currently adjusts multimodal message entries where
// images use the legacy "image" type and converts tool message content arrays
// into JSON strings.
func normalizeRequestBody(body []byte, reqType RequestType) ([]byte, bool, error) {
	switch reqType {
	case RequestTypeChatCompletions, RequestTypeResponses:
	default:
		return body, false, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false, nil
	}

	messagesAny, ok := payload["messages"].([]any)
	if !ok || len(messagesAny) == 0 {
		return body, false, nil
	}

	changed := false
	for i, msg := range messagesAny {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		contentVal, ok := msgMap["content"]
		if !ok {
			continue
		}

		switch content := contentVal.(type) {
		case []any:
			role, _ := msgMap["role"].(string)
			if strings.EqualFold(role, "tool") {
				marshalled, err := json.Marshal(content)
				if err != nil {
					return nil, false, err
				}
				msgMap["content"] = string(marshalled)
				changed = true
				messagesAny[i] = msgMap
				continue
			}

			for j, item := range content {
				itemMap, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if typ, _ := itemMap["type"].(string); strings.EqualFold(typ, "image") {
					itemMap["type"] = "image_url"
					content[j] = itemMap
					changed = true
				}
			}
			msgMap["content"] = content
			messagesAny[i] = msgMap
		}
	}

	if !changed {
		return body, false, nil
	}

	payload["messages"] = messagesAny
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
