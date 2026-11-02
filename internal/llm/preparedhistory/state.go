package preparedhistory

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
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

// TakeState transfers ownership in a request scope, or clones a reusable state
// before the provider may mutate the result.
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
	clear := func(target *cache) {
		target.mu.Lock()
		defer target.mu.Unlock()
		delete(target.states, reflect.TypeFor[T]())
	}
	clear(c)
	if c.statesOwner != nil {
		clear(c.statesOwner)
	}
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
	if c.statesOwner != nil && (c.boundReplay == nil || (c.boundReplay.TranscriptJSON == replay.TranscriptJSON && c.boundReplay.Rows == replay.Rows)) {
		c = c.statesOwner
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if old, ok := c.states[key]; ok && old.raw == replay.TranscriptJSON && old.rows == replay.Rows {
		if take && c.reusable {
			return cloneState(old.value.(T)), nil
		}
		if take {
			delete(c.states, key)
		}
		return old.value.(T), nil
	}
	delete(c.states, key)
	value, err := decode(replay)
	if err == nil && (!take || c.reusable) && (replay.TranscriptJSON != "" || replay.Rows != nil) {
		if c.states == nil {
			c.states = make(map[reflect.Type]stateEntry)
		}
		c.states[key] = stateEntry{raw: replay.TranscriptJSON, rows: replay.Rows, value: value}
	}
	if err == nil && take && c.reusable {
		return cloneState(value), nil
	}
	return value, err
}

// ReusableState retains immutable decoded native state across calls. Mutable
// handoffs clone containers, sharing immutable strings. Explicitly bound checkpoint
// replay projections may also be reused; newer history stays request-local.
type ReusableState struct {
	state  cache
	replay *models.ExecutionReplayMessage
}

func NewReusableState() *ReusableState { return &ReusableState{state: cache{reusable: true}} }

// BindReplay keeps recovery/compaction replacements request-local, so they cannot
// grow the retained cache beyond the checkpoint for which it was budgeted.
// Call once before publishing the scope to other goroutines.
func (r *ReusableState) BindReplay(replay models.ExecutionReplayMessage) {
	if replay.TranscriptJSON != "" {
		replay.Rows = nil
	}
	r.replay = &replay
}
func (r *ReusableState) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, &cache{statesOwner: &r.state, boundReplay: r.replay, entries: make(map[reflect.Type]entry)})
}

func cloneState[T any](value T) T {
	if any(value) == nil {
		return value
	}
	return transcript.FreezeItems([]any{value})[0].(T)
}
