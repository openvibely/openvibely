package attachmentsession

import "sync"

type sessionLock struct {
	mu   sync.Mutex
	refs int
}

var sessionLocks = struct {
	sync.Mutex
	byID map[string]*sessionLock
}{
	byID: make(map[string]*sessionLock),
}

// Lock serializes pending-file publication and cleanup for one attachment
// session. Callers must release the returned function exactly once.
func Lock(sessionID string) func() {
	sessionLocks.Lock()
	entry := sessionLocks.byID[sessionID]
	if entry == nil {
		entry = &sessionLock{}
		sessionLocks.byID[sessionID] = entry
	}
	entry.refs++
	sessionLocks.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		sessionLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(sessionLocks.byID, sessionID)
		}
		sessionLocks.Unlock()
	}
}
