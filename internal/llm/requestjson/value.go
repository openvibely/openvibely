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

// encodedValueSize counts one metadata field. Only directly borrowed fields
// are verbatim; nested ordinary values follow encoding/json, like appendValue.
func encodedValueSize(v any) (int, error) {
	if raw, ok := rawValue(v); ok && len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) {
		return len(raw), nil
	}

	return Size(v)
}

// plainSizeHint recognizes ordinary JSON containers without invoking custom
// marshalers. String lengths are a cheap capacity hint, not a validation or
// escaping pass: the standard encoder still handles escaping and buffer growth.
// Bound depth so cyclic/custom values retain the ordinary marshal fallback.
func plainSizeHint(v any, depth int) (int, bool) {
	if depth >= 128 {
		return 0, false
	}
	switch x := v.(type) {
	case nil:
		return 4, true
	case bool:
		return 5, true
	case int, int64, uint64, float64:
		return 24, true
	case string:
		// Small escaped context must fit beside a large image without forcing
		// a full-buffer growth for a few newline or quote escapes.
		if len(x) <= 64<<10 {
			return StringSize(x), true
		}
		return len(x) + 2, true
	case []any:
		n := 4
		for _, v := range x {
			size, ok := plainSizeHint(v, depth+1)
			if !ok {
				return 0, false
			}
			n += size + 1
		}
		return n, true
	case []map[string]any:
		n := 4
		for _, v := range x {
			size, ok := plainSizeHint(v, depth+1)
			if !ok {
				return 0, false
			}
			n += size + 1
		}
		return n, true
	case map[string]any:
		n := 4
		for k, v := range x {
			size, ok := plainSizeHint(v, depth+1)
			if !ok {
				return 0, false
			}
			n += StringSize(k) + 2 + size
		}
		return n, true
	default:
		return 0, false
	}
}
