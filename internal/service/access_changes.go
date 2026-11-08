package service

import "sync"

// AccessChange names what changed. An empty UserID means every user of the
// project; an empty ProjectID means every project of the user.
type AccessChange struct {
	UserID           string
	ProjectID        string
	CloseConnections bool
}

// AccessWatch receives only the changes that can affect its user.
type AccessWatch struct {
	hub         *accessHub
	userID      string
	signal      chan struct{}
	mu          sync.Mutex
	pending     []AccessChange
	projects    map[string]bool
	allProjects bool
}

// SetProjects limits project-policy notifications to active subscriptions.
func (w *AccessWatch) SetProjects(projects map[string]bool, all bool) {
	w.hub.mu.Lock()
	w.projects, w.allProjects = projects, all
	w.hub.mu.Unlock()
}

func (w *AccessWatch) C() <-chan struct{} { return w.signal }

// Drain returns and clears the changes received since the last call.
func (w *AccessWatch) Drain() []AccessChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.pending
	w.pending = nil
	return out
}

func (w *AccessWatch) Close() {
	if w == nil || w.hub == nil {
		return
	}
	w.hub.mu.Lock()
	delete(w.hub.watches, w)
	w.hub.mu.Unlock()
}

type accessHub struct {
	mu      sync.Mutex
	watches map[*AccessWatch]struct{}
}

func (h *accessHub) watch(userID string) *AccessWatch {
	w := &AccessWatch{hub: h, userID: userID, signal: make(chan struct{}, 1)}
	h.mu.Lock()
	if h.watches == nil {
		h.watches = map[*AccessWatch]struct{}{}
	}
	h.watches[w] = struct{}{}
	h.mu.Unlock()
	return w
}

func (h *accessHub) notify(change AccessChange) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for w := range h.watches {
		if change.UserID != "" && change.UserID != w.userID {
			continue
		}
		if change.ProjectID != "" && w.projects != nil && !w.allProjects && !w.projects[change.ProjectID] {
			continue
		}
		w.mu.Lock()
		if len(w.pending) < 64 {
			w.pending = append(w.pending, change)
		} else {
			closeConnections := change.CloseConnections
			for _, pending := range w.pending {
				closeConnections = closeConnections || pending.CloseConnections
			}
			w.pending = []AccessChange{{UserID: w.userID, CloseConnections: closeConnections}}
		}
		w.mu.Unlock()
		select {
		case w.signal <- struct{}{}:
		default:
		}
	}
}
