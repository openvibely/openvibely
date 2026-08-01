package events

import (
	"sync"

	"github.com/openvibely/openvibely/internal/applog"
)

type ExecutionStreamEventType string

const (
	ExecutionStreamDelta ExecutionStreamEventType = "delta"
	ExecutionStreamDone  ExecutionStreamEventType = "done"
	ExecutionStreamError ExecutionStreamEventType = "error"
)

type ExecutionStreamEvent struct {
	ExecID string
	Type   ExecutionStreamEventType
	Delta  string
	Offset int
	Status string
	Error  string
}

type ExecutionStreamSubscriber chan ExecutionStreamEvent

type executionStreamSubGuard struct {
	mu     sync.Mutex
	closed bool
}

type executionStreamEntry struct {
	ch    ExecutionStreamSubscriber
	guard *executionStreamSubGuard
}

// executionStreamGroup holds the subscribers for a single execution. members is
// the mutable set used for subscribe/unsubscribe bookkeeping, while snapshot is
// an immutable copy-on-write slice that Publish iterates without allocating.
//
// Snapshot rebuilds are lazy: membership changes only set dirty, and the next
// Publish regenerates the snapshot once under the hub write lock. Streamed
// output is far more frequent than connect/disconnect, so steady-state Publish
// reuses the cached snapshot with zero allocations while subscribe/unsubscribe
// churn stays near a plain map mutation. The snapshot is always replaced (never
// mutated in place), so a reader that has captured the slice header can safely
// range over it after releasing the hub lock.
type executionStreamGroup struct {
	members  map[ExecutionStreamSubscriber]*executionStreamSubGuard
	snapshot []executionStreamEntry
	dirty    bool
}

// rebuildSnapshot regenerates the immutable snapshot from the current members
// and clears the dirty flag. It always allocates a fresh backing array so
// previously published readers keep observing a stable view. Callers must hold
// the hub write lock.
func (g *executionStreamGroup) rebuildSnapshot() {
	g.dirty = false
	if len(g.members) == 0 {
		g.snapshot = nil
		return
	}
	snapshot := make([]executionStreamEntry, 0, len(g.members))
	for ch, guard := range g.members {
		snapshot = append(snapshot, executionStreamEntry{ch: ch, guard: guard})
	}
	g.snapshot = snapshot
}

type ExecutionStreamHub struct {
	mu              sync.RWMutex
	subs            map[string]*executionStreamGroup
	subscriberCount int
}

func NewExecutionStreamHub() *ExecutionStreamHub {
	return &ExecutionStreamHub{subs: make(map[string]*executionStreamGroup)}
}

func (h *ExecutionStreamHub) Subscribe(execID string) (ExecutionStreamSubscriber, func(), error) {
	if h == nil {
		return nil, func() {}, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscriberCount >= MaxSubscribers {
		return nil, nil, ErrMaxSubscribers
	}
	sub := make(ExecutionStreamSubscriber, 128)
	group := h.subs[execID]
	if group == nil {
		group = &executionStreamGroup{members: make(map[ExecutionStreamSubscriber]*executionStreamSubGuard)}
		h.subs[execID] = group
	}
	group.members[sub] = &executionStreamSubGuard{}
	group.dirty = true
	h.subscriberCount++
	unsubscribe := func() { h.Unsubscribe(execID, sub) }
	return sub, unsubscribe, nil
}

func (h *ExecutionStreamHub) Unsubscribe(execID string, sub ExecutionStreamSubscriber) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	var guard *executionStreamSubGuard
	if group := h.subs[execID]; group != nil {
		guard = group.members[sub]
		if guard != nil {
			delete(group.members, sub)
			h.subscriberCount--
			if len(group.members) == 0 {
				delete(h.subs, execID)
			} else {
				group.dirty = true
			}
		}
	}
	h.mu.Unlock()
	if guard != nil {
		guard.mu.Lock()
		if !guard.closed {
			guard.closed = true
			close(sub)
		}
		guard.mu.Unlock()
	}
}

func (h *ExecutionStreamHub) Publish(event ExecutionStreamEvent) {
	if h == nil || event.ExecID == "" {
		return
	}
	h.mu.RLock()
	group := h.subs[event.ExecID]
	var snapshot []executionStreamEntry
	if group != nil && !group.dirty {
		snapshot = group.snapshot
	}
	h.mu.RUnlock()
	if group != nil && snapshot == nil {
		// Membership changed since the last publish (or this is the first
		// publish); rebuild the immutable snapshot once under the write lock.
		h.mu.Lock()
		if group = h.subs[event.ExecID]; group != nil {
			if group.dirty {
				group.rebuildSnapshot()
			}
			snapshot = group.snapshot
		}
		h.mu.Unlock()
	}
	for _, e := range snapshot {
		e.guard.mu.Lock()
		if !e.guard.closed {
			select {
			case e.ch <- event:
			default:
				// Guard the variadic log call so the drop path allocates
				// nothing (boxing args into []any allocates even when the
				// debug level suppresses the message) and stays reproducible.
				if applog.IsDebug() {
					applog.Debugf("[events] execution stream subscriber slow exec=%s type=%s offset=%d", event.ExecID, event.Type, event.Offset)
				}
			}
		}
		e.guard.mu.Unlock()
	}
}

func (h *ExecutionStreamHub) Close(execID string, event ExecutionStreamEvent) {
	if h == nil || execID == "" {
		return
	}
	if event.ExecID == "" {
		event.ExecID = execID
	}
	h.mu.Lock()
	group := h.subs[execID]
	delete(h.subs, execID)
	if group != nil {
		h.subscriberCount -= len(group.members)
	}
	if h.subscriberCount < 0 {
		h.subscriberCount = 0
	}
	h.mu.Unlock()
	if group == nil {
		return
	}
	for sub, guard := range group.members {
		guard.mu.Lock()
		if !guard.closed {
			select {
			case sub <- event:
			default:
				if applog.IsDebug() {
					applog.Debugf("[events] execution stream terminal dropped for slow subscriber exec=%s type=%s", execID, event.Type)
				}
			}
			guard.closed = true
			close(sub)
		}
		guard.mu.Unlock()
	}
}

func (h *ExecutionStreamHub) SubscriberCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.subscriberCount
}
