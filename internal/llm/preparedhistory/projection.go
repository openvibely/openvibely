package preparedhistory

import (
	"context"
	"reflect"

	"github.com/openvibely/openvibely/internal/models"
)

type replayIdentity struct {
	raw  string
	rows *models.ReplayRows
}
type projectionKey struct {
	replayIdentity
	typ reflect.Type
}

func identifyReplay(replay models.ExecutionReplayMessage) replayIdentity {
	if replay.TranscriptJSON != "" {
		return replayIdentity{raw: replay.TranscriptJSON}
	}
	return replayIdentity{rows: replay.Rows}
}

// BindHistory permits reuse only for immutable entries owned by the budgeted
// checkpoint. Call before publishing the scope. New turns and rewritten history
// never enter this retained cache.
func (r *ReusableState) BindHistory(history []models.Execution) {
	r.state.boundHistory = make(map[replayIdentity]bool)
	for _, execution := range history {
		for _, replay := range execution.ReplayMessages {
			if replay.HasTranscript() {
				r.state.boundHistory[identifyReplay(replay)] = true
			}
		}
	}
}

// ReadReplayProjection returns an immutable projection. Senders must clone its
// mutable containers before editing. It never retains unbound replay entries.
func ReadReplayProjection[T any](ctx context.Context, replay models.ExecutionReplayMessage, decode func(models.ExecutionReplayMessage) (T, error)) (T, error) {
	if ctx == nil {
		return decode(replay)
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil || c.statesOwner == nil {
		return decode(replay)
	}
	owner := c.statesOwner
	id := identifyReplay(replay)
	owner.mu.Lock()
	if !owner.boundHistory[id] {
		owner.mu.Unlock()
		return decode(replay)
	}
	defer owner.mu.Unlock()
	key := projectionKey{id, reflect.TypeFor[T]()}
	if value, ok := owner.projections[key]; ok {
		return value.(T), nil
	}
	value, err := decode(replay)
	if err == nil {
		if owner.projections == nil {
			owner.projections = make(map[projectionKey]any)
		}
		owner.projections[key] = value
	}
	return value, err
}
