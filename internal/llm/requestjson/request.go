// Package requestjson owns immutable encoded request fields shared by provider
// preflight checks and wire encoding. Provider-specific request schemas stay in
// their clients.
package requestjson

import (
	"encoding/json"
	"fmt"
	"sync"
	"unicode/utf8"
)

// Field is immutable after preparation. It can be reused across retry attempts
// while the corresponding logical messages remain unchanged.
type Field struct {
	mu  sync.Mutex
	key string
	raw []byte
}

func Prepare(key string, value any) (*Field, error) {
	// Encode in an object, matching the wire encoder's nesting and buffering.
	envelope, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		return nil, err
	}
	encodedKey, _ := json.Marshal(key)
	return &Field{key: key, raw: envelope[len(encodedKey)+2 : len(envelope)-1]}, nil
}

// JSON returns a read-only view; callers must not modify it.
func (f *Field) JSON() []byte { f.mu.Lock(); defer f.mu.Unlock(); return f.raw }

// envelope accepts only metadata: the prepared field must be absent or null.
// Rejecting a populated field prevents accidental duplicate full serialization.
func (f *Field) envelope(metadata any) ([]byte, error) {
	b, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("request metadata must be an object")
	}
	if value, ok := fields[f.key]; ok && string(value) != "null" {
		return nil, fmt.Errorf("prepared field %q must be absent or null", f.key)
	}
	delete(fields, f.key)
	head, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	key, _ := json.Marshal(f.key)
	head = head[:len(head)-1]
	if len(fields) > 0 {
		head = append(head, ',')
	}
	head = append(head, key...)
	head = append(head, ':')
	return head, nil
}

// Size computes exact encoded bytes without copying the prepared field.
func (f *Field) Size(metadata any) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head, err := f.envelope(metadata)
	if err != nil {
		return 0, err
	}
	return len(head) + len(f.raw) + 1, nil
}

func (f *Field) Marshal(metadata any) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head, err := f.envelope(metadata)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(head)+len(f.raw)+1)
	out = append(out, head...)
	out = append(out, f.raw...)
	out = append(out, '}')
	// Preserve immutable message bytes, but move their view into the wire buffer.
	// The retry options must not retain a second complete encoded history.
	f.raw = out[len(head) : len(out)-1]
	return out, nil
}

// MarshalItems encodes an array field directly into its final envelope. This
// avoids allocating a separate large array buffer for one-shot Responses sends.
func MarshalItems(metadata any, key string, items []any) ([]byte, error) {
	if items == nil {
		f, err := Prepare(key, items)
		if err != nil {
			return nil, err
		}
		return f.Marshal(metadata)
	}
	f := &Field{key: key}
	head, err := f.envelope(metadata)
	if err != nil {
		return nil, err
	}
	size := len(head) + 2
	for _, item := range items {
		if raw, ok := item.(json.RawMessage); ok {
			size += len(raw) + 1
		}
	}
	out := make([]byte, 0, size)
	out = append(out, head...)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		if raw, ok := item.(json.RawMessage); ok && len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw) {
			out = append(out, raw...)
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		out = append(out, raw...)
	}
	return append(out, ']', '}'), nil
}
