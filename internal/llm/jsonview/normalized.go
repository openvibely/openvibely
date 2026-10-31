package jsonview

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// DecodeNormalized borrows canonical string values while retaining the numeric,
// duplicate-key and escaping semantics of json.Unmarshal into any followed by
// json.Marshal. The input and borrowed values must remain immutable. This is for
// provider conversion, not for executing tools (which need ordinary Go values).
func DecodeNormalized(raw json.RawMessage) (any, error) {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		var value any
		err := json.Unmarshal(raw, &value)
		return value, err
	}
	return decodeNormalized(raw)
}

func decodeNormalized(raw json.RawMessage) (any, error) {
	raw = bytes.TrimSpace(raw)
	switch raw[0] {
	case '{':
		fields, err := Fields(raw)
		if err != nil {
			return nil, err
		}
		values := make(map[string]any, len(fields))
		for key, field := range fields {
			value, err := decodeNormalized(field)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return values, nil
	case '[':
		fields, err := Array(raw)
		if err != nil {
			return nil, err
		}
		values := make([]any, len(fields))
		for i, field := range fields {
			values[i], err = decodeNormalized(field)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	case '"':
		if canonicalString(raw) {
			return Borrow(raw)
		}
	}
	var value any
	err := json.Unmarshal(raw, &value)
	return value, err
}

// Fast path for the exact spelling produced by encoding/json. Unusual escapes,
// malformed UTF-8 and surrogate pairs use the decoder rather than guessing.
func canonicalString(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 1; i < len(raw)-1; i++ {
		switch raw[i] {
		case '<', '>', '&':
			return false
		case 0xe2:
			if i+2 < len(raw) && raw[i+1] == 0x80 && (raw[i+2] == 0xa8 || raw[i+2] == 0xa9) {
				return false
			}
		case '\\':
			i++
			switch raw[i] {
			case '"', '\\', 'b', 'f', 'n', 'r', 't':
			case 'u':
				// These non-control escapes are canonical with HTML escaping enabled.
				code := string(raw[i+1 : i+5])
				if code != "003c" && code != "003e" && code != "0026" && code != "2028" && code != "2029" {
					return false
				}
				i += 4
			default:
				return false
			}
		}
	}
	return true
}
