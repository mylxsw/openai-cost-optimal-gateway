package tiktoken

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type Tiktoken struct{}

func EncodingForModel(model string) (*Tiktoken, error) {
	if model == "" {
		return nil, errors.New("model name is required")
	}
	return &Tiktoken{}, nil
}

func GetEncoding(name string) (*Tiktoken, error) {
	if name == "" {
		return nil, errors.New("encoding name is required")
	}
	return &Tiktoken{}, nil
}

func (t *Tiktoken) Encode(text string, allowedSpecial map[string]struct{}, disallowedSpecial map[string]struct{}) ([]int, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return []int{}, nil
	}
	words := strings.Fields(trimmed)
	if len(words) == 0 {
		length := utf8.RuneCountInString(trimmed)
		words = make([]string, length)
	}
	tokens := make([]int, len(words))
	return tokens, nil
}
