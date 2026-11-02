// Package preparedhistory shares decoded history between budgeting and sending
// within one provider call. Sending normally takes ownership. A bounded owner
// may explicitly reuse immutable native checkpoint state between calls; mutable
// handoffs then clone containers. Changed history is rebuilt.
package preparedhistory

import (
	"context"
	"reflect"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

type contextKey struct{}
type entry struct {
	history []models.Execution
	value   any
}
type cache struct {
	statesOwner  *cache
	projections  map[projectionKey]any
	boundHistory map[replayIdentity]bool
	boundReplay  *models.ExecutionReplayMessage
	reusable     bool
	mu           sync.Mutex
	entries      map[reflect.Type]entry
	states       map[reflect.Type]stateEntry
	rows         map[reflect.Type]rowsEntry
}

// WithScope always creates a fresh scope, including for nested provider calls.
func WithScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &cache{entries: make(map[reflect.Type]entry)})
}

// Read returns immutable messages for estimation. Neither the outer slice nor
// nested provider data may be mutated. Take transfers ownership to the sender.
func Read[T any](ctx context.Context, history []models.Execution, build func([]models.Execution) []T) []T {
	return get(ctx, history, build, false)
}
func Take[T any](ctx context.Context, history []models.Execution, build func([]models.Execution) []T) []T {
	return get(ctx, history, build, true)
}
func get[T any](ctx context.Context, history []models.Execution, build func([]models.Execution) []T, take bool) []T {
	if ctx == nil || (!take && len(history) == 0) {
		return build(history)
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil {
		return build(history)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if len(history) == 0 {
		if take {
			delete(c.entries, key)
		}
		return build(history)
	}
	old, ok := c.entries[key]
	if ok && reflect.DeepEqual(old.history, history) {
		if take {
			delete(c.entries, key)
		}
		return old.value.([]T)
	}
	delete(c.entries, key)
	value := build(history)
	if !take {
		// Execution values and replay headers can be replaced in place by recovery.
		// ReplayRows are immutable by contract; their payload storage is shared.
		snapshot := append([]models.Execution(nil), history...)
		for i := range snapshot {
			snapshot[i].ReplayMessages = append([]models.ExecutionReplayMessage(nil), history[i].ReplayMessages...)
			snapshot[i].RetainedUserMessage = append([]byte(nil), history[i].RetainedUserMessage...)
		}
		c.entries[key] = entry{snapshot, value}
	}
	return value
}
