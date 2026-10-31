package preparedhistory

import (
	"context"
	"sync"
)

type transferKey[T any] struct{}
type transfer[T any] struct {
	mu      sync.Mutex
	value   T
	present bool
}

// WithTransfer hands an already prepared value to a nested provider operation.
// It is consumed once, and does not require a serialized native-state detour.
func WithTransfer[T any](ctx context.Context, value T) context.Context {
	return context.WithValue(ctx, transferKey[T]{}, &transfer[T]{value: value, present: true})
}
func TakeTransfer[T any](ctx context.Context) (T, bool) {
	var zero T
	if ctx == nil {
		return zero, false
	}
	t, _ := ctx.Value(transferKey[T]{}).(*transfer[T])
	if t == nil {
		return zero, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.present {
		return zero, false
	}
	value := t.value
	t.value = zero
	t.present = false
	return value, true
}
