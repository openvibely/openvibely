package preparedhistory

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"reflect"
)

type stateEntry struct {
	raw   string
	rows  *models.ReplayRows
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
	return replayState(ctx, models.ExecutionReplayMessage{TranscriptJSON: raw}, func(replay models.ExecutionReplayMessage) (T, error) { return decode(replay.TranscriptJSON) }, take)
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

// ReadReplayState and TakeReplayState have the same single-request ownership
// contract as the string helpers, keyed by immutable row identity or raw JSON.
func ReadReplayState[T any](ctx context.Context, replay models.ExecutionReplayMessage, decode func(models.ExecutionReplayMessage) (T, error)) (T, error) {
	return replayState(ctx, replay, decode, false)
}
func TakeReplayState[T any](ctx context.Context, replay models.ExecutionReplayMessage, decode func(models.ExecutionReplayMessage) (T, error)) (T, error) {
	return replayState(ctx, replay, decode, true)
}
func replayState[T any](ctx context.Context, replay models.ExecutionReplayMessage, decode func(models.ExecutionReplayMessage) (T, error), take bool) (T, error) {
	if replay.TranscriptJSON != "" {
		replay.Rows = nil
	}
	if ctx == nil {
		return decode(replay)
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil {
		return decode(replay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if old, ok := c.states[key]; ok && old.raw == replay.TranscriptJSON && old.rows == replay.Rows {
		if take {
			delete(c.states, key)
		}
		return old.value.(T), nil
	}
	delete(c.states, key)
	value, err := decode(replay)
	if err == nil && !take && (replay.TranscriptJSON != "" || replay.Rows != nil) {
		if c.states == nil {
			c.states = make(map[reflect.Type]stateEntry)
		}
		c.states[key] = stateEntry{raw: replay.TranscriptJSON, rows: replay.Rows, value: value}
	}
	return value, err
}
