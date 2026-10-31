package requestjson

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Select reuses encoded item payloads after the caller rechecks permissions.
// It never caches authorization decisions. Indexes refer to immutable byte
// offsets, so Marshal can move the field without retaining its old buffer.
func (f *Field) Select(indices []int, extra []any) (*Field, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.itemsIndexed {
		rows, err := Array(f.raw)
		f.itemsError = err
		if err == nil {
			f.itemOffsets = make([][2]int, len(rows))
			pos := 0
			for i, row := range rows {
				start := pos + bytes.Index(f.raw[pos:], row)
				f.itemOffsets[i] = [2]int{start, start + len(row)}
				pos = start + len(row)
			}
		}
		f.itemsIndexed = true
	}
	if f.itemsError != nil {
		return nil, f.itemsError
	}
	same := len(extra) == 0 && len(indices) == len(f.itemOffsets)
	size := 2
	for i, n := range indices {
		if n < 0 || n >= len(f.itemOffsets) {
			return nil, fmt.Errorf("prepared item index %d out of bounds", n)
		}
		if i != n {
			same = false
		}
		span := f.itemOffsets[n]
		size += span[1] - span[0] + 1
	}
	if same {
		return f, nil
	}
	var added []byte
	if len(extra) > 0 {
		var err error
		added, err = json.Marshal(extra)
		if err != nil {
			return nil, err
		}
		added = added[1 : len(added)-1]
		size += len(added) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, n := range indices {
		if i > 0 {
			out = append(out, ',')
		}
		span := f.itemOffsets[n]
		out = append(out, f.raw[span[0]:span[1]]...)
	}
	if len(added) > 0 {
		if len(indices) > 0 {
			out = append(out, ',')
		}
		out = append(out, added...)
	}
	out = append(out, ']')
	return &Field{key: f.key, raw: out}, nil
}
