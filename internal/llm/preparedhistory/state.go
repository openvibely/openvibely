package preparedhistory

import (
	"context"
	"reflect"
)

type stateEntry struct {
	raw   string
	value any
}

// ReadState shares decoded native state only inside one request scope. A changed
// serialized state replaces the entry. Callers must treat ReadState as immutable.
func ReadState[T any](ctx context.Context, raw string, decode func(string) (T, error)) (T, error) {
	return state(ctx, raw, decode, false)
}

// TakeState releases cache ownership before the provider may mutate the result.
func TakeState[T any](ctx context.Context, raw string, decode func(string) (T, error)) (T, error) {
	return state(ctx, raw, decode, true)
}
func state[T any](ctx context.Context, raw string, decode func(string) (T, error), take bool) (T, error) {
	if ctx == nil {
		return decode(raw)
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil {
		return decode(raw)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if old, ok := c.states[key]; ok && old.raw == raw {
		if take {
			delete(c.states, key)
		}
		return old.value.(T), nil
	}
	delete(c.states, key)
	value, err := decode(raw)
	if err == nil && !take && raw != "" {
		if c.states == nil {
			c.states = make(map[reflect.Type]stateEntry)
		}
		c.states[key] = stateEntry{raw, value}
	}
	return value, err
}

// ForgetState releases a prepared state when recovery removes its checkpoint.
func ForgetState[T any](ctx context.Context) {
	if ctx == nil {
		return
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, reflect.TypeFor[T]())
}
