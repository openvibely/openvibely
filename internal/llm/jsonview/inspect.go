package jsonview

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Fields borrows an object's values. The source must remain immutable.
func Fields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	err := Object(raw, func(k string, v json.RawMessage) error { out[k] = v; return nil })
	return out, err
}

// FindString inspects metadata without decoding unrelated text or tool results.
// Fields on the current object take precedence over descendants.
func FindString(raw json.RawMessage, key string) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	var children []json.RawMessage
	var found string
	switch raw[0] {
	case '{':
		err := Object(raw, func(k string, v json.RawMessage) error {
			if k == key {
				var s string
				if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
					found = s
				}
			}
			children = append(children, v)
			return nil
		})
		if err != nil {
			return "", false
		}
	case '[':
		var err error
		children, err = Array(raw)
		if err != nil {
			return "", false
		}
	default:
		return "", false
	}
	if found != "" {
		return found, true
	}
	for _, child := range children {
		if s, ok := FindString(child, key); ok {
			return s, true
		}
	}
	return "", false
}

// FoldedFields matches the case-insensitive field selection of struct decoders.
// Last occurrence wins, including differently cased spellings.
func FoldedFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	err := Object(raw, func(k string, v json.RawMessage) error { out[strings.ToLower(k)] = v; return nil })
	return out, err
}

// ToolDisplayFields decodes only fields used by tool labels, borrowing/skipping
// unrelated arguments such as file contents. Tool execution still decodes its
// complete validated arguments independently.
func ToolDisplayFields(raw json.RawMessage) (map[string]any, error) {
	out := map[string]any{}
	err := Object(raw, func(k string, v json.RawMessage) error {
		switch k {
		case "file_path", "command", "pattern", "path", "query", "url", "action", "search_query", "image_query", "find", "open":
			var value any
			if err := json.Unmarshal(v, &value); err != nil {
				return err
			}
			out[k] = value
		}
		return nil
	})
	return out, err
}
