package service

import (
	"encoding/json"
	"strconv"
)

// OpenAICompatibleModelLimits are optional limits reported by a /models entry.
type OpenAICompatibleModelLimits struct {
	ContextLength int
	MaxOutput     int
}

// ParseOpenAICompatibleModelEntry extracts model id and optional context limits from a discovery payload item.
func ParseOpenAICompatibleModelEntry(raw map[string]any) (id string, limits OpenAICompatibleModelLimits) {
	if raw == nil {
		return "", OpenAICompatibleModelLimits{}
	}
	id = stringField(raw, "id")
	if id == "" {
		id = stringField(raw, "model")
	}
	limits.ContextLength = intField(raw,
		"context_length", "max_model_len", "max_context_length", "context_window", "max_sequence_length")
	limits.MaxOutput = intField(raw, "max_output_tokens", "max_tokens", "max_completion_tokens")
	if root, ok := raw["root"].(map[string]any); ok && limits.ContextLength == 0 {
		limits.ContextLength = intField(root, "max_model_len", "context_length")
	}
	return id, limits
}

func stringField(raw map[string]any, key string) string {
	v, ok := raw[key]
	if !ok || v == nil {
		return ""
	}
	switch typed := v.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func intField(raw map[string]any, keys ...string) int {
	for _, key := range keys {
		v, ok := raw[key]
		if !ok || v == nil {
			continue
		}
		switch typed := v.(type) {
		case float64:
			if typed > 0 {
				return int(typed)
			}
		case int:
			if typed > 0 {
				return typed
			}
		case int64:
			if typed > 0 {
				return int(typed)
			}
		case json.Number:
			if n, err := typed.Int64(); err == nil && n > 0 {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(typed); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}
