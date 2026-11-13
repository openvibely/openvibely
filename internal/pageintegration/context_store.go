package pageintegration

import (
	"sync"
	"time"
)

// ContextStore keeps the latest explicitly shared context per (principal, page).
// Keys include the principal and the server-resolved page ref, so one user's or
// one project's context can never be read through another page.
type ContextStore struct {
	mu       sync.Mutex
	entries  map[string]Snapshot
	revision int64
}

func NewContextStore() *ContextStore { return &ContextStore{entries: map[string]Snapshot{}} }

func contextKey(principalID string, ref PageRef) string {
	return principalID + "\x00" + ref.ProjectID + "\x00" + ref.Key()
}

func (s *ContextStore) Set(principalID string, ref PageRef, c Context, source string) (Snapshot, error) {
	if err := ValidateContext(c); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	snap := Snapshot{Page: ref, Context: c, Source: source, CapturedAt: time.Now().UTC(), Revision: s.revision}
	s.entries[contextKey(principalID, ref)] = snap
	return snap, nil
}

func (s *ContextStore) Get(principalID string, ref PageRef) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.entries[contextKey(principalID, ref)]
	return snap, ok
}

// ForgetInstance drops every stored context for an instance (disable/remove/project change).
func (s *ContextStore) ForgetInstance(instanceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, snap := range s.entries {
		if snap.Page.InstanceID == instanceID {
			delete(s.entries, k)
		}
	}
}
