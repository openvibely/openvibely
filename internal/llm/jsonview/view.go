package jsonview

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"strings"
)

// Object visits borrowed field values. Views are immutable and live as long as raw.
// Duplicate fields are visited in wire order; callers choose their schema rules.
func Object(raw []byte, visit func(string, json.RawMessage) error) error {
	if !json.Valid(raw) {
		return fmt.Errorf("invalid JSON object")
	}
	d := jsontext.NewDecoder(bytes.NewBuffer(raw), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	t, err := d.ReadToken()
	if err != nil {
		return err
	}
	if t.Kind() != '{' {
		return fmt.Errorf("expected JSON object")
	}
	for d.PeekKind() != '}' {
		k, err := d.ReadToken()
		if err != nil {
			return err
		}
		name := k.String()
		v, err := d.ReadValue()
		if err != nil {
			return err
		}
		end := int(d.InputOffset())
		if err = visit(name, raw[end-len(v):end]); err != nil {
			return err
		}
	}
	_, err = d.ReadToken()
	return err
}

// Array returns borrowed, immutable elements, without copying their payloads.
func Array(raw []byte) ([]json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON array")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	d := jsontext.NewDecoder(bytes.NewBuffer(raw), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	t, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	if t.Kind() != '[' {
		return nil, fmt.Errorf("expected JSON array")
	}
	out := make([]json.RawMessage, 0)
	for d.PeekKind() != ']' {
		v, err := d.ReadValue()
		if err != nil {
			return nil, err
		}
		end := int(d.InputOffset())
		out = append(out, raw[end-len(v):end])
	}
	_, err = d.ReadToken()
	return out, err
}

// Content borrows the last case-insensitive content field, matching the existing
// provider JSON decoders. It does not decode large text just to inspect a field.
func Content(raw []byte) (json.RawMessage, error) {
	var content json.RawMessage
	err := Object(raw, func(k string, v json.RawMessage) error {
		if strings.EqualFold(k, "content") {
			content = v
		}
		return nil
	})
	return content, err
}
