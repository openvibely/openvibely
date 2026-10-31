package jsonview

import (
	"encoding/json"
	"unicode/utf8"
)

// EncodedSize counts the compact, HTML-escaped encoding of a RawMessage without
// materializing it. Invalid JSON/UTF-8 uses the encoder's compatibility behavior.
func EncodedSize(raw []byte) (int, error) {
	if !json.Valid(raw) || !utf8.Valid(raw) {
		v, err := json.Marshal(json.RawMessage(raw))
		return len(v), err
	}
	size := 0
	quoted := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !quoted {
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				continue
			}
			size++
			if c == '"' {
				quoted = true
			}
			continue
		}
		if c == '\\' {
			size += 2
			i++
			continue
		}
		if c == '"' {
			quoted = false
			size++
			continue
		}
		if c == '<' || c == '>' || c == '&' {
			size += 6
			continue
		}
		if c == 0xe2 && i+2 < len(raw) && raw[i+1] == 0x80 && (raw[i+2] == 0xa8 || raw[i+2] == 0xa9) {
			size += 6
			i += 2
			continue
		}
		size++
	}
	return size, nil
}
