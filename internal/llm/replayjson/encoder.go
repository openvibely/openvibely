// Package replayjson encodes provider replay data without changing its format.
package replayjson

import (
	"encoding/json"
	"reflect"
	"sync"
)

// Encode returns an immutable representation suitable for storage and recovery.
func Encode(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Encoder caches only the latest snapshot, for one provider invocation. Snapshot
// must detach mutable fields; returning false disables reuse for that value.
// Encoding reuse never skips a database write or marks a write successful.
type Encoder[T any] struct {
	mu       sync.Mutex
	Snapshot func(T) (T, bool)
	last     T
	encoded  string
	valid    bool
}

func (e *Encoder[T]) Encode(value T) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.valid && reflect.DeepEqual(e.last, value) {
		return e.encoded, nil
	}
	encoded, err := Encode(value)
	if err != nil {
		return "", err
	}
	e.valid = false
	var zero T
	e.last = zero
	e.encoded = ""
	if e.Snapshot != nil {
		if snapshot, ok := e.Snapshot(value); ok {
			e.last, e.encoded, e.valid = snapshot, encoded, true
		}
	}
	return encoded, nil
}
