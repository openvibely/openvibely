package preparedhistory

import (
	"context"
	"reflect"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
)

type stateEntry struct {
	identity any
	value    any
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
	return cachedState(c, replayStateIdentity{replay.TranscriptJSON, replay.Rows}, func() (T, error) { return decode(replay) }, take, replay.TranscriptJSON != "" || replay.Rows != nil)
}

type replayStateIdentity struct {
	raw  string
	rows *models.ReplayRows
}

// cachedState owns reuse and mutable handoff for both native checkpoints and
// combined checkpoint history. The owner is bounded by the checkpoint cache.
func cachedState[T any](c *cache, identity any, decode func() (T, error), take, retain bool) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := reflect.TypeFor[T]()
	if old, ok := c.states[key]; ok && old.identity == identity {
		if take && c.reusable {
			return cloneState(old.value.(T)), nil
		}
		if take {
			delete(c.states, key)
		}
		return old.value.(T), nil
	}
	delete(c.states, key)
	value, err := decode()
	if err == nil && (!take || c.reusable) && retain {
		if c.states == nil {
			c.states = make(map[reflect.Type]stateEntry)
		}
		c.states[key] = stateEntry{identity: identity, value: value}
	}
	if err == nil && take && c.reusable {
		return cloneState(value), nil
	}
	return value, err
}

// ReusableState retains immutable decoded native state across calls. Mutable
// handoffs clone containers, sharing immutable strings. Explicitly bound checkpoint
// combined history may also be reused; newer history stays request-local.
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
