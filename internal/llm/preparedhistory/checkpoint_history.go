package preparedhistory

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"reflect"
)

// BindHistory binds the complete immutable checkpoint history before publication.
// ReplayRows and their payloads are immutable; copy mutable record containers.
func (r *ReusableState) BindHistory(history []models.Execution) {
	r.state.boundHistory = append([]models.Execution(nil), history...)
	for i := range r.state.boundHistory {
		r.state.boundHistory[i].ReplayMessages = append([]models.ExecutionReplayMessage(nil), history[i].ReplayMessages...)
		r.state.boundHistory[i].RetainedUserMessage = append([]byte(nil), history[i].RetainedUserMessage...)
	}
}

// ReadCheckpointHistory reuses the combined prepared checkpoint, not individual
// replay entries. The returned value is immutable. Changed or trimmed histories
// bypass reuse; newer exchanges are left to the caller and never retained.
func ReadCheckpointHistory[T any](ctx context.Context, history []models.Execution, build func([]models.Execution) T) (value T, start, count int) {
	if ctx == nil {
		return value, 0, 0
	}
	c, _ := ctx.Value(contextKey{}).(*cache)
	if c == nil || c.statesOwner == nil {
		return value, 0, 0
	}
	owner := c.statesOwner
	n := len(owner.boundHistory)
	if n == 0 {
		return value, 0, 0
	}
	for i := 0; i+n <= len(history); i++ {
		if !sameCheckpointHistory(history[i:i+n], owner.boundHistory) {
			continue
		}
		value, _ = cachedState(owner, owner, func() (T, error) { return build(history[i : i+n]), nil }, false, true)
		return value, i, n
	}
	return value, 0, 0
}

// Restoration may clear source IDs and mark old checkpoint records as context.
// Those labels do not affect message preparation; all payload/fallback fields
// must still match before the combined prepared value can be reused.
func sameCheckpointHistory(a, b []models.Execution) bool {
	for i := range a {
		left, right := a[i], b[i]
		left.ID, right.ID = "", ""
		left.ContextKind, right.ContextKind = "", ""
		if !reflect.DeepEqual(left, right) {
			return false
		}
	}
	return true
}
