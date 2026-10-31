package jsonview

import (
	"bytes"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// Value retains immutable JSON. It never caches a decoded copy. Callers that
// need mutable data decode into their own destination; inspection borrows bytes.
type Value struct {
	raw         json.RawMessage
	encodedSize int
	stringSize  int
}

func Borrow(raw json.RawMessage) (Value, error) {
	if !json.Valid(raw) {
		var v any
		err := json.Unmarshal(raw, &v)
		return Value{}, err
	}
	v := Value{raw: raw}
	var err error
	v.encodedSize, err = EncodedSize(raw)
	if err != nil {
		return Value{}, err
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '"' {
		v.stringSize, err = v.countStringSize()
		if err != nil {
			return Value{}, err
		}
	}
	return v, nil
}

// Raw returns a read-only view; the caller must never mutate it.
func (v Value) Raw() json.RawMessage { return v.raw }
func (v Value) Decode(dst any) error { return json.Unmarshal(v.raw, dst) }
func (v Value) Size() (int, error) {
	if v.raw == nil {
		return 4, nil
	}
	return v.encodedSize, nil
}
func (v Value) Text() (string, error) { var s string; err := v.Decode(&s); return s, err }

// DecodedStringBytes counts the UTF-8 bytes after JSON unescaping without
// retaining decoded text. The fallback preserves the encoder's malformed input rules.
func (v Value) DecodedStringBytes() (int, error) {
	raw := bytes.TrimSpace(v.raw)
	if len(raw) < 2 || raw[0] != '"' || !utf8.Valid(raw) {
		s, err := v.Text()
		return len(s), err
	}
	n := 0
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			n++
			continue
		}
		i++
		if raw[i] != 'u' {
			n++
			continue
		}
		r, end := escapeRune(raw, i-1)
		n += utf8.RuneLen(r)
		i = end - 1
	}
	return n, nil
}

// StringSize counts the canonical encoding of the decoded string, without
// materializing large string values. This differs from Size for escaped letters.
func (v Value) StringSize() (int, error) {
	if v.stringSize != 0 {
		return v.stringSize, nil
	}
	return v.countStringSize()
}
func (v Value) countStringSize() (int, error) {
	raw := bytes.TrimSpace(v.raw)
	if len(raw) < 2 || raw[0] != '"' || !utf8.Valid(raw) {
		s, err := v.Text()
		return StringSize(s), err
	}
	n := 2
	for i := 1; i < len(raw)-1; {
		if raw[i] != '\\' {
			start := i
			for i < len(raw)-1 && raw[i] != '\\' {
				i++
			}
			n += StringBytesSize(raw[start:i]) - 2
			continue
		}
		if raw[i+1] != 'u' {
			if raw[i+1] == '/' {
				n++
			} else {
				n += 2
			}
			i += 2
			continue
		}
		r, end := escapeRune(raw, i)
		switch r {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			n += 2
		case '<', '>', '&', '\u2028', '\u2029':
			n += 6
		default:
			if r < 0x20 {
				n += 6
			} else {
				n += utf8.RuneLen(r)
			}
		}
		i = end
	}
	return n, nil
}

// escapeRune reads a validated Unicode escape, consuming a low surrogate only
// when it belongs to the high surrogate. No temporary strings are constructed.
func escapeRune(raw []byte, start int) (rune, int) {
	hex4 := func(b []byte) rune {
		var r rune
		for _, c := range b {
			r <<= 4
			switch {
			case c >= '0' && c <= '9':
				r += rune(c - '0')
			case c >= 'a' && c <= 'f':
				r += rune(c - 'a' + 10)
			default:
				r += rune(c - 'A' + 10)
			}
		}
		return r
	}
	r := hex4(raw[start+2 : start+6])
	end := start + 6
	if r >= 0xD800 && r <= 0xDBFF && end+6 <= len(raw)-1 && raw[end] == '\\' && raw[end+1] == 'u' {
		next := hex4(raw[end+2 : end+6])
		if next >= 0xDC00 && next <= 0xDFFF {
			return utf16.DecodeRune(r, next), end + 6
		}
	}
	if utf16.IsSurrogate(r) {
		r = utf8.RuneError
	}
	return r, end
}

// MarshalJSON preserves the JSON value at compatibility boundaries. Callers
// retain ownership of the immutable source passed to Borrow.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.raw == nil {
		return []byte("null"), nil
	}
	return v.raw, nil
}
