package requestjson

import (
	"encoding/json"
	"sort"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

func rawValue(value any) (json.RawMessage, bool) {
	switch v := value.(type) {
	case *jsonview.ResponseItem:
		if v == nil {
			return nil, false
		}
		return v.RawJSON(), true
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
func HasRawValue(value any) bool { return hasRawValue(value, 0) }

func hasRawValue(value any, depth int) bool {
	// Cyclic/deep ordinary values must reach encoding/json's error handling.
	if depth >= 64 {
		return false
	}
	if _, ok := rawValue(value); ok {
		return true
	}
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if hasRawValue(item, depth+1) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasRawValue(item, depth+1) {
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
func appendValue(dst []byte, value any) ([]byte, error) { return appendValueDepth(dst, value, 0) }
func appendValueDepth(dst []byte, value any, depth int) ([]byte, error) {
	// SavedResponse already validated syntax and UTF-8; its bytes are immutable.
	if saved, ok := value.(*jsonview.ResponseItem); ok && saved != nil && len(saved.RawJSON()) > 0 {
		return append(dst, saved.RawJSON()...), nil
	}
	if depth >= 64 {
		encoded, err := json.Marshal(value)
		return append(dst, encoded...), err
	}
	if raw, ok := rawValue(value); ok {
		if len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) {
			return append(dst, raw...), nil
		}
		encoded, err := json.Marshal(value)
		return append(dst, encoded...), err
	}
	if items, ok := value.([]any); ok && HasRawValue(items) {
		dst = append(dst, '[')
		for i, item := range items {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendValueDepth(dst, item, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
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
		// Ordinary values retain encoding/json's type, cycle and error rules.
		if HasRawValue(fields[key]) {
			dst, err = appendValueDepth(dst, fields[key], depth+1)
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

// encodedValueSize follows the same nested raw policy as appendValue.
func encodedValueSize(v any) (int, error) { return encodedValueSizeDepth(v, 0) }
func encodedValueSizeDepth(v any, depth int) (int, error) {
	if depth >= 64 {
		return Size(v)
	}
	if raw, ok := rawValue(v); ok && len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) {
		return len(raw), nil
	}
	if !HasRawValue(v) {
		return Size(v)
	}
	n := 2
	switch value := v.(type) {
	case map[string]any:
		for k, item := range value {
			size, err := encodedValueSizeDepth(item, depth+1)
			if err != nil {
				return 0, err
			}
			n += jsonview.StringSize(k) + 1 + size + 1
		}
		if len(value) > 0 {
			n--
		}
		return n, nil
	case []any:
		for _, item := range value {
			size, err := encodedValueSizeDepth(item, depth+1)
			if err != nil {
				return 0, err
			}
			n += size + 1
		}
		if len(value) > 0 {
			n--
		}
		return n, nil
	}
	return Size(v)
}
