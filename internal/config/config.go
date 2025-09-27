package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
)

type Config struct {
	Listen      string           `json:"listen" yaml:"listen"`
	APIKeys     []string         `json:"api_keys" yaml:"api_keys"`
	Providers   []ProviderConfig `json:"providers" yaml:"providers"`
	Models      []ModelConfig    `json:"models" yaml:"models"`
	Default     string           `json:"default_provider" yaml:"default_provider"`
	Debug       bool             `json:"debug" yaml:"debug"`
	SaveUsage   bool             `json:"save_usage" yaml:"save_usage"`
	StorageType string           `json:"storage_type" yaml:"storage_type"`
	StorageURI  string           `json:"storage_uri" yaml:"storage_uri"`
}

type ProviderConfig struct {
	ID          string            `json:"id" yaml:"id"`
	BaseURL     string            `json:"base_url" yaml:"base_url"`
	AccessToken string            `json:"access_token" yaml:"access_token"`
	Type        ProviderType      `json:"type" yaml:"type"`
	Headers     map[string]string `json:"headers" yaml:"headers"`
	Timeout     time.Duration     `json:"timeout" yaml:"timeout"`
}

type ModelConfig struct {
	Name      string         `json:"model" yaml:"model"`
	Providers ModelProviders `json:"providers" yaml:"providers"`
	Rules     []RuleConfig   `json:"rules" yaml:"rules"`
}

type ModelProviders []ModelProvider

type ModelProvider struct {
	ID    string `json:"provider" yaml:"provider"`
	Model string `json:"model" yaml:"model"`
}

type RuleConfig struct {
	Expression string                 `json:"rule" yaml:"rule"`
	Providers  ProviderOverrideConfig `json:"providers" yaml:"providers"`
}

type ProviderOverrideConfig []ProviderOverride

type ProviderOverride struct {
	Provider string `json:"provider" yaml:"provider"`
	Model    string `json:"model" yaml:"model"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := unmarshalYAML(data, &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg.setDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *Config) setDefaults() {
	for i := range c.Providers {
		if c.Providers[i].Type == "" {
			c.Providers[i].Type = ProviderTypeOpenAI
		}
	}

	if c.StorageType == "" {
		c.StorageType = "sqlite"
	}
	if c.StorageURI == "" {
		c.StorageURI = "file:usage.db?_pragma=busy_timeout=5000&_pragma=journal_mode=WAL"
	}
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen address is required")
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("at least one api key is required")
	}

	providers := make(map[string]struct{})
	for _, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider id is required")
		}
		if _, ok := providers[p.ID]; ok {
			return fmt.Errorf("duplicated provider id: %s", p.ID)
		}
		providers[p.ID] = struct{}{}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %s base_url is required", p.ID)
		}
		if p.AccessToken == "" {
			return fmt.Errorf("provider %s access_token is required", p.ID)
		}
	}

	for _, m := range c.Models {
		if m.Name == "" {
			return fmt.Errorf("model name is required")
		}
		if len(m.Providers) == 0 {
			return fmt.Errorf("model %s must have at least one provider", m.Name)
		}
		for _, provider := range m.Providers {
			if provider.ID == "" {
				return fmt.Errorf("model %s provider id is required", m.Name)
			}
			if _, ok := providers[provider.ID]; !ok {
				return fmt.Errorf("model %s references unknown provider %s", m.Name, provider.ID)
			}
		}
		for _, r := range m.Rules {
			if r.Expression == "" {
				return fmt.Errorf("model %s has rule with empty expression", m.Name)
			}
			if len(r.Providers) == 0 {
				return fmt.Errorf("model %s rule %s must specify providers", m.Name, r.Expression)
			}
			for _, override := range r.Providers {
				if override.Provider == "" {
					return fmt.Errorf("model %s rule %s provider is required", m.Name, r.Expression)
				}
				if _, ok := providers[override.Provider]; !ok {
					return fmt.Errorf("model %s rule %s references unknown provider %s", m.Name, r.Expression, override.Provider)
				}
			}
		}
	}

	if c.Default != "" {
		if _, ok := providers[c.Default]; !ok {
			return fmt.Errorf("default provider %s not found", c.Default)
		}
	}

	if c.SaveUsage {
		if c.StorageType != "sqlite" && c.StorageType != "mysql" {
			return fmt.Errorf("unsupported storage_type %s", c.StorageType)
		}
		if strings.TrimSpace(c.StorageURI) == "" {
			return fmt.Errorf("storage_uri is required when save_usage is enabled")
		}
	}

	return nil
}

func (m *ModelProviders) UnmarshalJSON(data []byte) error {
	var obj []ModelProvider
	if err := json.Unmarshal(data, &obj); err == nil {
		*m = obj
		return nil
	}

	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}

	providers := make([]ModelProvider, 0, len(arr))
	for _, id := range arr {
		providers = append(providers, ModelProvider{ID: id})
	}
	*m = providers
	return nil
}

func (p *ProviderOverrideConfig) UnmarshalJSON(data []byte) error {
	var arr []ProviderOverride
	if err := json.Unmarshal(data, &arr); err == nil {
		*p = arr
		return nil
	}

	var obj map[string]string
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := make([]ProviderOverride, 0, len(keys))
	for _, k := range keys {
		result = append(result, ProviderOverride{Provider: k, Model: obj[k]})
	}
	*p = result
	return nil
}

func (c Config) ProviderByID(id string) (*ProviderConfig, bool) {
	for i := range c.Providers {
		if c.Providers[i].ID == id {
			return &c.Providers[i], true
		}
	}
	return nil, false
}

type yamlContext struct {
	indent    int
	kind      string
	mapVal    map[string]interface{}
	listVal   []interface{}
	parentMap map[string]interface{}
	parentKey string
}

func unmarshalYAML(data []byte, out interface{}) error {
	root := map[string]interface{}{}
	stack := []yamlContext{{indent: -1, kind: "map", mapVal: root}}
	lines := strings.Split(string(data), "\n")

	for i := 0; i < len(lines); i++ {
		rawLine := removeComment(lines[i])
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		indent := countIndent(rawLine)

		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			return fmt.Errorf("invalid indentation at line %d", i+1)
		}
		curr := stack[len(stack)-1]

		if strings.HasPrefix(trimmed, "-") {
			if curr.kind != "list" {
				return fmt.Errorf("unexpected list item at line %d", i+1)
			}
			itemText := strings.TrimSpace(trimmed[1:])
			if itemText == "" {
				nextIdx, nextLine, nextIndent := nextNonEmpty(lines, i+1)
				var item interface{}
				if nextIdx >= 0 && nextIndent > indent && strings.HasPrefix(strings.TrimSpace(removeComment(nextLine)), "-") {
					item = []interface{}{}
				} else {
					item = map[string]interface{}{}
				}
				curr.listVal = append(curr.listVal, item)
				curr.parentMap[curr.parentKey] = curr.listVal
				stack[len(stack)-1] = curr
				stack = append(stack, yamlContext{indent: indent, kind: detectKind(item), mapVal: toMap(item), listVal: toSlice(item), parentMap: curr.parentMap, parentKey: curr.parentKey})
				continue
			}

			key, value, hasValue := parseKeyValue(itemText)
			if hasValue {
				itemMap := map[string]interface{}{key: value}
				curr.listVal = append(curr.listVal, itemMap)
				curr.parentMap[curr.parentKey] = curr.listVal
				stack[len(stack)-1] = curr
				stack = append(stack, yamlContext{indent: indent, kind: "map", mapVal: itemMap})
				continue
			}
			val := parseScalar(itemText)
			curr.listVal = append(curr.listVal, val)
			curr.parentMap[curr.parentKey] = curr.listVal
			stack[len(stack)-1] = curr
			continue
		}

		key, value, hasValue := parseKeyValue(trimmed)
		if !hasValue {
			nextIdx, nextLine, nextIndent := nextNonEmpty(lines, i+1)
			var child interface{}
			if nextIdx >= 0 && nextIndent > indent && strings.HasPrefix(strings.TrimSpace(removeComment(nextLine)), "-") {
				child = []interface{}{}
			} else {
				child = map[string]interface{}{}
			}
			if curr.kind != "map" {
				return fmt.Errorf("unexpected mapping at line %d", i+1)
			}
			curr.mapVal[key] = child
			stack[len(stack)-1] = curr
			stack = append(stack, yamlContext{indent: indent, kind: detectKind(child), mapVal: toMap(child), listVal: toSlice(child), parentMap: curr.mapVal, parentKey: key})
			continue
		}
		if curr.kind != "map" {
			return fmt.Errorf("unexpected mapping at line %d", i+1)
		}
		curr.mapVal[key] = value
		stack[len(stack)-1] = curr
	}

	jsonData, err := json.Marshal(root)
	if err != nil {
		return err
	}
	return json.Unmarshal(jsonData, out)
}

func parseKeyValue(text string) (string, interface{}, bool) {
	parts := strings.SplitN(text, ":", 2)
	key := strings.TrimSpace(parts[0])
	if key == "" {
		return "", nil, false
	}
	if len(parts) == 1 {
		return key, nil, false
	}
	valueStr := strings.TrimSpace(parts[1])
	if valueStr == "" {
		return key, nil, false
	}
	return key, parseScalar(valueStr), true
}

func parseScalar(text string) interface{} {
	if strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"") && len(text) >= 2 {
		return strings.Trim(text, "\"")
	}
	if strings.HasPrefix(text, "'") && strings.HasSuffix(text, "'") && len(text) >= 2 {
		return strings.Trim(text, "'")
	}
	if text == "true" {
		return true
	}
	if text == "false" {
		return false
	}
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return f
	}
	return text
}

func countIndent(line string) int {
	count := 0
	for _, ch := range line {
		if ch == ' ' {
			count++
		} else {
			break
		}
	}
	return count
}

func removeComment(line string) string {
	inSingle := false
	inDouble := false
	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func nextNonEmpty(lines []string, start int) (int, string, int) {
	for i := start; i < len(lines); i++ {
		line := removeComment(lines[i])
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return i, line, countIndent(line)
	}
	return -1, "", 0
}

func detectKind(v interface{}) string {
	switch v.(type) {
	case []interface{}:
		return "list"
	case map[string]interface{}:
		return "map"
	default:
		return ""
	}
}

func toMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func toSlice(v interface{}) []interface{} {
	s, _ := v.([]interface{})
	return s
}
