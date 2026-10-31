package requestjson

import (
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

func rawValue(value any) (json.RawMessage, bool) {
	switch v := value.(type) {
	case json.RawMessage:
		return v, true
	case jsonview.Value:
		return v.Raw(), true
	default:
		return nil, false
	}
}

// HasRawValue reports native raw items or borrowed fields within a message.
// Provider-specific message structs stay at the caller boundary.
func HasRawValue(value any) bool {
	if _, ok := rawValue(value); ok {
		return true
	}
	if fields, ok := value.(map[string]any); ok {
		for _, v := range fields {
			if _, ok := rawValue(v); ok {
				return true
			}
		}
	}
	return false
}

func rawSizeHint(value any) int {
	if raw, ok := rawValue(value); ok {
		return len(raw)
	}
	n := 0
	if fields, ok := value.(map[string]any); ok {
		n = 2
		for key, v := range fields {
			n += jsonview.StringSize(key) + 2 // colon and comma (one spare byte)
			if raw, ok := rawValue(v); ok {
				n += len(raw)
			} else if size, err := jsonview.Size(v); err == nil {
				n += size
			}
		}
	}
	return n
}

// appendValue shares the existing verbatim raw-item policy with nested borrowed
// fields. It never mutates input. Invalid JSON/UTF-8 keeps encoder semantics.
func appendValue(dst []byte, value any) ([]byte, error) {
	if raw, ok := rawValue(value); ok {
		if len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) {
			return append(dst, raw...), nil
		}
		encoded, err := json.Marshal(value)
		return append(dst, encoded...), err
	}
	fields, ok := value.(map[string]any)
	if !ok || !HasRawValue(fields) {
		encoded, err := json.Marshal(value)
		return append(dst, encoded...), err
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dst = append(dst, '{')
	for i, key := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		name, _ := json.Marshal(key)
		dst = append(dst, name...)
		dst = append(dst, ':')
		var err error
		// Non-raw values retain encoding/json's type, cycle and error rules.
		if _, raw := rawValue(fields[key]); raw {
			dst, err = appendValue(dst, fields[key])
		} else {
			var encoded []byte
			encoded, err = json.Marshal(fields[key])
			dst = append(dst, encoded...)
		}
		if err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

// MarshalValue encodes one immutable replay value with the same raw-field policy
// used for requests. Ordinary values retain encoding/json validation.
func MarshalValue(value any) ([]byte, error) {
	return appendValue(make([]byte, 0, rawSizeHint(value)), value)
}
