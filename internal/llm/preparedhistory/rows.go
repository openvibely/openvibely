package preparedhistory

import (
	"context"
	"encoding/json"
	"reflect"
)

type rowsEntry struct {
	rows  []json.RawMessage
	value any
}

// ReadRows prepares immutable summary rows once. The row index is snapshotted;
// payloads obey the same immutable contract as stored replay rows.
func ReadRows[T any](ctx context.Context, rows []json.RawMessage, decode func([]json.RawMessage) (T, error)) (T, error) {
	return preparedRows(ctx, rows, decode, false)
}

// TakeRows releases cache ownership before the sender can mutate the projection.
func TakeRows[T any](ctx context.Context, rows []json.RawMessage, decode func([]json.RawMessage) (T, error)) (T, error) {
	return preparedRows(ctx, rows, decode, true)
}
func preparedRows[T any](ctx context.Context, rows []json.RawMessage, decode func([]json.RawMessage) (T, error), take bool) (T, error) {
	if ctx == nil {
		return decode(rows)
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil {
		return decode(rows)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if old, ok := c.rows[key]; ok && reflect.DeepEqual(old.rows, rows) {
		if take {
			delete(c.rows, key)
		}
		return old.value.(T), nil
	}
	delete(c.rows, key)
	value, err := decode(rows)
	if err == nil && !take && len(rows) > 0 {
		if c.rows == nil {
			c.rows = make(map[reflect.Type]rowsEntry)
		}
		c.rows[key] = rowsEntry{append([]json.RawMessage(nil), rows...), value}
	}
	return value, err
}
