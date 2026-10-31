// Package requestjson owns immutable encoded request fields shared by provider
// preflight checks and wire encoding. Provider-specific request schemas stay in
// their clients.
package requestjson

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Field is immutable after preparation. It can be reused across retry attempts
// while the corresponding logical messages remain unchanged.
type Field struct {
	mu  sync.Mutex
	key string
	raw []byte
	// Item offsets survive Marshal moving raw into a new wire buffer.
	itemOffsets  [][2]int
	itemsIndexed bool
	itemsError   error
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

// Metadata adds already encoded immutable fields to a small request object.
// This lets budgeting and sending share tool schemas without re-encoding them.
type Metadata struct {
	Value  any
	Fields map[string]*Field
}

// envelope accepts only metadata: the prepared field must be absent or null.
// Rejecting a populated field prevents accidental duplicate full serialization.
func (f *Field) envelope(metadata any) ([]byte, error) {
	var prepared map[string]*Field
	if m, ok := metadata.(Metadata); ok {
		metadata, prepared = m.Value, m.Fields
	}
	b, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err = Object(b, func(k string, v json.RawMessage) error { fields[k] = v; return nil }); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("request metadata must be an object")
	}
	if value, ok := fields[f.key]; ok && string(value) != "null" {
		return nil, fmt.Errorf("prepared field %q must be absent or null", f.key)
	}
	delete(fields, f.key)
	for key, value := range prepared {
		if value == nil {
			return nil, fmt.Errorf("nil prepared metadata field %q", key)
		}
		if key == f.key || value == f {
			return nil, fmt.Errorf("duplicate prepared field %q", key)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate metadata field %q", key)
		}
		fields[key] = value.JSON()
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	head := []byte{'{'}
	for i, name := range keys {
		if i > 0 {
			head = append(head, ',')
		}
		encoded, _ := json.Marshal(name)
		head = append(head, encoded...)
		head = append(head, ':')
		head = append(head, fields[name]...)
	}
	key, _ := json.Marshal(f.key)
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
	// Encode non-raw rows once before allocating the final body. Omitting their
	// sizes can force a second full-history allocation for a tiny overflow.
	encoded := make([][]byte, len(items))
	for i, item := range items {
		if HasRawValue(item) {
			size += rawSizeHint(item) + 1
		} else {
			encoded[i], err = json.Marshal(item)
			if err != nil {
				return nil, err
			}
			size += len(encoded[i]) + 1
		}
	}
	out := make([]byte, 0, size)
	out = append(out, head...)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		if encoded[i] != nil {
			out = append(out, encoded[i]...)
			continue
		}
		out, err = appendValue(out, item)
		if err != nil {
			return nil, err
		}
	}
	return append(out, ']', '}'), nil
}

// PrepareItems encodes native raw items once, preserving their immutable bytes.
func PrepareItems(key string, items []any) (*Field, error) {
	body, err := MarshalItems(struct{}{}, key, items)
	if err != nil {
		return nil, err
	}
	encodedKey, _ := json.Marshal(key)
	return &Field{key: key, raw: body[len(encodedKey)+2 : len(body)-1]}, nil
}
