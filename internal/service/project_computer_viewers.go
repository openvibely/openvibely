package service

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openvibely/openvibely/internal/persistence"
)

type computerViewer struct {
	User, Token  string
	Control      bool
	Adaptive     bool
	inputEnabled atomic.Bool
	Cancel       context.CancelFunc
	input        sync.RWMutex
	revoked      bool
}

func (s *ProjectComputerService) viewerPrefix(id string) string {
	return "viewer:" + id + ":" + s.taskID + ":"
}
func (s *ProjectComputerService) viewerLock(id string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore("control:"+id+":"+s.taskID, &sync.Mutex{})
	return value.(*sync.Mutex)
}
func (s *ProjectComputerService) RegisterViewer(ctx context.Context, id, user, session string, control bool, replacements ...context.CancelFunc) (string, func(), error) {
	return s.registerViewer(ctx, id, user, session, control, false, replacements...)
}

// RegisterAdaptiveViewer keeps its display connection while input is toggled.
// It starts with input disabled and is revoked like any control-capable viewer.
func (s *ProjectComputerService) RegisterAdaptiveViewer(ctx context.Context, id, user, session string, cancel context.CancelFunc) (string, func(), error) {
	return s.registerViewer(ctx, id, user, session, true, true, cancel)
}

func (s *ProjectComputerService) registerViewer(ctx context.Context, id, user, session string, control, adaptive bool, replacements ...context.CancelFunc) (string, func(), error) {
	lock := s.viewerLock(id)
	// Takeover may wait for agent cleanup. Reject reconnects during that
	// transition instead of waiting behind it with an open HTTP request.
	if !lock.TryLock() {
		return "", nil, persistence.ErrComputerBusy
	}
	defer lock.Unlock()
	gate := s.viewerGate(id)
	gate.Lock()
	defer gate.Unlock()
	if control && !adaptive {
		if err := s.checkViewerPermission(ctx, id); err != nil {
			return "", nil, err
		}
	}
	key := s.viewerPrefix(id) + session
	viewer := &computerViewer{User: user, Token: rand.Text(), Control: control, Adaptive: adaptive}
	if len(replacements) > 0 {
		viewer.Cancel = replacements[0]
	}
	if previous, loaded := s.active.Load(key); loaded {
		old := previous.(*computerViewer)
		if old.User != user {
			return "", nil, errors.New("viewer belongs to another user")
		}
		old.revoke()
	}
	s.active.Store(key, viewer)
	return viewer.Token, func() { s.active.CompareAndDelete(key, viewer) }, nil
}
func (s *ProjectComputerService) hasControlViewer(id string) bool {
	found := false
	s.active.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), s.viewerPrefix(id)) && value.(*computerViewer).controlling() {
			found = true
			return false
		}
		return true
	})
	return found
}

// Serialized with takeover and viewer registration, so an old connection cannot
// release a replacement viewer's control. Private handoffs always stay paused.
func (s *ProjectComputerService) ReleaseUnviewed(ctx context.Context, id string, explicit ...bool) (bool, error) {
	lock := s.viewerLock(id)
	lock.Lock()
	defer lock.Unlock()
	if s.hasControlViewer(id) {
		return false, nil
	}
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return false, err
	}
	allowPending := len(explicit) > 0 && explicit[0]
	if (!state.Held && !state.TakeoverPending) || (state.TakeoverPending && !allowPending) || state.HandoffKind != "" {
		return false, nil
	}
	if err = s.Hold(ctx, id, false); err != nil {
		return false, err
	}
	defer s.publishStatus(id, false)
	return true, s.repo.SetTakeoverPending(ctx, id, false)
}

// viewerGate serializes registration with permission changes.
// It is separate from the agent/lifecycle locks: ordinary input may run alongside tools.
func (s *ProjectComputerService) viewerGate(id string) *sync.RWMutex {
	value, _ := s.locks.LoadOrStore("viewer-gate:"+id, &sync.RWMutex{})
	return value.(*sync.RWMutex)
}

// changeViewerPermission revokes existing control connections before changing
// durable state. Registration cannot race the change; failed changes require a
// fresh permission check too. View-only connections are unaffected.
func (s *ProjectComputerService) changeViewerPermission(id string, allTasks bool, change func() error) error {
	defer s.publishStatus(id, allTasks)
	gate := s.viewerGate(id)
	gate.Lock()
	defer gate.Unlock()
	prefix := s.viewerPrefix(id)
	if allTasks {
		prefix = "viewer:" + id + ":"
	}
	s.active.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			viewer := value.(*computerViewer)
			if viewer.Control {
				s.active.CompareAndDelete(key, viewer)
				viewer.revoke()
			}
		}
		return true
	})
	return change()
}

// ForwardViewerInput only checks connection membership in memory. Permissions
// were checked at registration; state changes remove and cancel that connection.
func (s *ProjectComputerService) ForwardViewerInput(id, session, token string, write func() error) error {
	value, ok := s.active.Load(s.viewerPrefix(id) + session)
	if !ok {
		return errors.New("computer control connection revoked")
	}
	viewer := value.(*computerViewer)
	viewer.input.RLock()
	defer viewer.input.RUnlock()
	if viewer.revoked || !viewer.controlling() || viewer.Token != token {
		return errors.New("computer control connection revoked")
	}
	return write()
}

// Cancel first to interrupt a blocked network write, then drain in-flight input.
// Each connection has its own lock so a slow viewer cannot stall other desktops.
func (v *computerViewer) revoke() {
	if v.Cancel != nil {
		v.Cancel()
	}
	v.input.Lock()
	defer v.input.Unlock()
	v.revoked = true
}

func (v *computerViewer) controlling() bool {
	return v.Control && (!v.Adaptive || v.inputEnabled.Load())
}

// SetViewerInput is called only by the owning stream. The token also protects
// a replacement connection using the same viewer session from stale requests.
func (s *ProjectComputerService) SetViewerInput(ctx context.Context, id, session, token string, enabled bool) error {
	lock := s.viewerLock(id)
	if !lock.TryLock() {
		return persistence.ErrComputerBusy
	}
	defer lock.Unlock()
	gate := s.viewerGate(id)
	gate.RLock()
	defer gate.RUnlock()
	value, ok := s.active.Load(s.viewerPrefix(id) + session)
	if !ok {
		return errors.New("viewer disconnected")
	}
	v := value.(*computerViewer)
	v.input.Lock()
	defer v.input.Unlock()
	if v.revoked || !v.Adaptive || v.Token != token {
		return errors.New("viewer unavailable")
	}
	if enabled {
		if err := s.checkViewerPermission(ctx, id); err != nil {
			return err
		}
	}
	v.inputEnabled.Store(enabled)
	return nil
}
