package transcript

import (
	"context"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ExchangeUpdate contains the current invocation, or explicitly replaced history.
type ExchangeUpdate struct {
	Items           []any
	FirstChanged    int
	ReplacesHistory bool
}

// ExchangeRecorder preserves earlier invocations of the same execution on every
// save, for every provider. Replacement after compaction is explicit.
type ExchangeRecorder struct {
	mu       sync.Mutex
	recorder *Recorder
	replaced bool
}

func NewExchangeRecorder(store persistence.ConversationStore, owner models.ConversationOwner) *ExchangeRecorder {
	recorder := NewRecorder(store, owner)
	recorder.preserveExisting = true
	return &ExchangeRecorder{recorder: recorder}
}

// OpenExchangeRecorder captures the existing execution before steering commits
// or provider callbacks can modify it. Every provider uses this setup boundary.
func OpenExchangeRecorder(ctx context.Context, store persistence.ConversationStore, owner models.ConversationOwner, replace bool) (*ExchangeRecorder, error) {
	r := NewExchangeRecorder(store, owner)
	if replace {
		r.ReplacePrevious()
	}
	if err := r.recorder.load(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *ExchangeRecorder) ReplacePrevious() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.replaced {
		r.recorder.replacePrevious()
		r.replaced = true
	}
}

func (r *ExchangeRecorder) SaveUpdate(ctx context.Context, header, replacement ReplayHeader, u ExchangeUpdate) ([]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u.ReplacesHistory {
		if !r.replaced {
			r.recorder.replacePrevious()
			r.replaced = true
		}
		header = replacement
		u.FirstChanged = 0
	}
	header.NullItems = u.Items == nil
	if err := r.recorder.SaveChanged(ctx, header, u.Items, u.FirstChanged); err != nil {
		return nil, err
	}
	return r.recorder.Recovery(), nil
}

func (r *ExchangeRecorder) SaveChanged(ctx context.Context, header ReplayHeader, items []any, firstChanged int) error {
	_, err := r.SaveUpdate(ctx, header, header, ExchangeUpdate{Items: items, FirstChanged: firstChanged})
	return err
}
func (r *ExchangeRecorder) Save(ctx context.Context, header ReplayHeader, items []any) error {
	return r.SaveChanged(ctx, header, items, 0)
}
