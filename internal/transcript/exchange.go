package transcript

import (
	"context"
	"reflect"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ExchangeUpdate describes provider data, independently of its wire format.
// Prefix is an unchanged candidate for omission, not proof that it is durable.
// Items contains the rest of the invocation; a rewrite contains all replacement
// items and sets ReplacesHistory.
type ExchangeUpdate struct {
	Items           []any
	Prefix          []any
	FirstChanged    int
	ReplacesHistory bool
	// CompleteHistory means Items includes Prefix; otherwise Items is current-execution data.
	CompleteHistory bool
}

// CanSeparateHistory is a cheap preparation hint. SaveUpdate also verifies the
// source executions in storage before excluding any prefix. In particular,
// synthetic steering contexts must never count as independently durable history.
func CanSeparateHistory(execID string, history []models.Execution) bool {
	history = models.EffectiveReplayHistory(history)
	if execID == "" || len(history) == 0 {
		return false
	}
	taskID := history[0].TaskID
	for _, e := range history {
		if e.ID == "" || e.ID == execID || e.TaskID == "" || e.TaskID != taskID || e.Status != models.ExecCompleted || e.ContextKind != "" || len(e.ReplayMessages) == 0 {
			return false
		}
		for _, replay := range e.ReplayMessages {
			if !replay.HasTranscript() {
				return false
			}
		}
	}
	return true
}

// ExchangeRecorder owns the save boundary for every provider. Providers supply
// their current-execution items and headers; those with combined history may
// additionally supply a prefix. Only other completed, persisted executions can
// justify omitting that prefix. Recovery includes it regardless of storage form.
type ExchangeRecorder struct {
	mu       sync.Mutex
	recorder *Recorder
	lookup   interface {
		GetByID(context.Context, string) (*models.Execution, error)
	}
	owner            models.ConversationOwner
	sources          []string
	taskID           string
	checked, durable bool
	replacement      bool
}

func NewExchangeRecorder(store persistence.ConversationStore, owner models.ConversationOwner, history []models.Execution) *ExchangeRecorder {
	r := &ExchangeRecorder{recorder: NewRecorder(store, owner), owner: owner}
	r.lookup, _ = store.(interface {
		GetByID(context.Context, string) (*models.Execution, error)
	})
	if CanSeparateHistory(owner.ExecutionID, history) {
		for _, e := range models.EffectiveReplayHistory(history) {
			r.sources = append(r.sources, e.ID)
			r.taskID = e.TaskID
		}
	}
	return r
}

func (r *ExchangeRecorder) prefixIsDurable(ctx context.Context) (bool, error) {
	if r.checked {
		return r.durable, nil
	}
	if r.lookup == nil || len(r.sources) == 0 {
		r.checked = true
		return false, nil
	}
	current, err := r.lookup.GetByID(persistence.WithExecutionMetadata(ctx), r.owner.ExecutionID)
	if err != nil {
		return false, err
	}
	if current == nil || current.TaskID != r.taskID {
		r.checked = true
		return false, nil
	}
	for _, id := range r.sources {
		source, err := r.lookup.GetByID(persistence.WithExecutionMetadata(ctx), id)
		if err != nil {
			return false, err
		}
		if source == nil || source.ID == r.owner.ExecutionID || source.TaskID != current.TaskID || source.Status != models.ExecCompleted {
			r.checked = true
			return false, nil
		}
	}
	r.checked, r.durable = true, true
	return true, nil
}

func (r *ExchangeRecorder) SaveUpdate(ctx context.Context, header, replacement ReplayHeader, u ExchangeUpdate) ([]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	durable := true
	if len(u.Prefix) > 0 && !u.ReplacesHistory {
		var err error
		durable, err = r.prefixIsDurable(ctx)
		if err != nil {
			return nil, err
		}
	}
	u = selectExchange(u, durable, r.replacement)
	r.replacement = u.ReplacesHistory
	replacement.NullItems = u.Items == nil

	if u.ReplacesHistory {
		header = replacement
		u.FirstChanged = 0
	}
	if err := r.recorder.SaveChanged(ctx, header, u.Items, u.FirstChanged); err != nil {
		return nil, err
	}
	saved := r.recorder.Recovery()
	if !u.ReplacesHistory && len(u.Prefix) > 0 {
		saved = append(append([]any(nil), u.Prefix...), saved...)
	}
	return saved, nil
}

func (r *ExchangeRecorder) SaveChanged(ctx context.Context, header ReplayHeader, items []any, firstChanged int) error {
	_, err := r.SaveUpdate(ctx, header, header, ExchangeUpdate{Items: items, FirstChanged: firstChanged})
	return err
}
func (r *ExchangeRecorder) Save(ctx context.Context, header ReplayHeader, items []any) error {
	return r.SaveChanged(ctx, header, items, 0)
}

// selectExchange is the single boundary rule for all recorder callers. Inputs
// may be a complete provider conversation or an already isolated exchange.
// Rewrites are sticky for this execution: later saves must not revert to deltas.
func selectExchange(u ExchangeUpdate, durable, replaced bool) ExchangeUpdate {
	explicitReplacement := u.ReplacesHistory
	u.ReplacesHistory = u.ReplacesHistory || replaced
	if u.CompleteHistory {
		n := len(u.Prefix)
		if !durable || len(u.Items) < n || (n > 0 && !reflect.DeepEqual(u.Items[:min(len(u.Items), n)], u.Prefix)) {
			u.ReplacesHistory = true
		}
		if !u.ReplacesHistory {
			u.Items = u.Items[n:]
			u.FirstChanged = max(0, u.FirstChanged-n)
		}
	} else if len(u.Prefix) > 0 && (!durable || replaced) && !explicitReplacement {
		u.Items = append(append([]any(nil), u.Prefix...), u.Items...)
		u.ReplacesHistory = true
	}
	if u.ReplacesHistory {
		u.FirstChanged = 0
	}
	return u
}
