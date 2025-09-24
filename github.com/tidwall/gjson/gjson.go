package gjson

import (
	"encoding/json"
	"strconv"
	"strings"
)

type Result struct {
	value  interface{}
	exists bool
}

func GetBytes(data []byte, path string) Result {
	var root interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return Result{}
	}
	if path == "" {
		return wrap(root, true)
	}
	parts := strings.Split(path, ".")
	current := root
	exists := true
	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return Result{}
			}
			current = val
		default:
			return Result{}
		}
	}
	return wrap(current, exists)
}

func wrap(value interface{}, exists bool) Result {
	return Result{value: value, exists: exists}
}

func (r Result) Exists() bool {
	return r.exists && r.value != nil
}

func (r Result) String() string {
	if !r.Exists() {
		return ""
	}
	switch v := r.value.(type) {
	case string:
		return v
	case float64:
		return trimFloat(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		bytes, _ := json.Marshal(v)
		return string(bytes)
	}
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return s
}

func (r Result) Get(path string) Result {
	if !r.Exists() {
		return Result{}
	}
	m, ok := r.value.(map[string]interface{})
	if !ok {
		return Result{}
	}
	val, ok := m[path]
	if !ok {
		return Result{}
	}
	return wrap(val, true)
}

func (r Result) ForEach(fn func(key, value Result) bool) {
	if !r.Exists() {
		return
	}
	if arr, ok := r.value.([]interface{}); ok {
		for _, item := range arr {
			if !fn(Result{}, wrap(item, true)) {
				return
			}
		}
	} else if m, ok := r.value.(map[string]interface{}); ok {
		for k, v := range m {
			if !fn(wrap(k, true), wrap(v, true)) {
				return
			}
		}
	}
}

func (r Result) IsArray() bool {
	if !r.Exists() {
		return false
	}
	_, ok := r.value.([]interface{})
	return ok
}

func (r Result) Value() interface{} {
	if !r.Exists() {
		return nil
	}
	return r.value
}
