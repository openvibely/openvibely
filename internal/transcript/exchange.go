package transcript

import (
	"context"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ExchangeUpdate contains the current execution, or explicitly replaced history.
type ExchangeUpdate struct {
	Items           []any
	FirstChanged    int
	ReplacesHistory bool
}

// CanSeparateHistory is a cheap preparation hint. BeginExchange verifies the
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
// their current-execution items and headers. BeginExchange establishes prior
// history using other completed, persisted executions. Recovery includes that
// history regardless of storage form.
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
	prefix           []any
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

// BeginExchange verifies prior history before the client starts collecting its
// current exchange. Synthetic steering history belongs to the current execution
// and must instead be included in a replacement save.
func (r *ExchangeRecorder) BeginExchange(ctx context.Context, prefix []any) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	durable, err := r.prefixIsDurable(ctx)
	if err != nil {
		return false, err
	}
	if durable {
		r.prefix = FreezeItems(prefix)
	}
	return durable, nil
}

func (r *ExchangeRecorder) SaveUpdate(ctx context.Context, header, replacement ReplayHeader, u ExchangeUpdate) ([]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u.ReplacesHistory {
		header = replacement
		u.FirstChanged = 0
	}
	header.NullItems = u.Items == nil
	if err := r.recorder.SaveChanged(ctx, header, u.Items, u.FirstChanged); err != nil {
		return nil, err
	}
	saved := r.recorder.Recovery()
	if !u.ReplacesHistory && len(r.prefix) > 0 {
		saved = append(append([]any(nil), r.prefix...), saved...)
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
