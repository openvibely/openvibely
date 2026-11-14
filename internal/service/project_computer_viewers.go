package service

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
)

type computerViewer struct {
	User, Token string
	Control     bool
	Cancel      context.CancelFunc
}

func (s *ProjectComputerService) viewerPrefix(id string) string {
	return "viewer:" + id + ":" + s.taskID + ":"
}
func (s *ProjectComputerService) viewerLock(id string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore("control:"+id+":"+s.taskID, &sync.Mutex{})
	return value.(*sync.Mutex)
}
func (s *ProjectComputerService) RegisterViewer(id, user, session string, control bool, replacements ...context.CancelFunc) (string, func(), error) {
	lock := s.viewerLock(id)
	lock.Lock()
	defer lock.Unlock()
	key := s.viewerPrefix(id) + session
	viewer := &computerViewer{User: user, Token: rand.Text(), Control: control}
	if len(replacements) > 0 {
		viewer.Cancel = replacements[0]
	}
	if previous, loaded := s.active.Load(key); loaded {
		old := previous.(*computerViewer)
		if old.User != user {
			return "", nil, errors.New("viewer belongs to another user")
		}
		if old.Cancel != nil {
			old.Cancel()
		}
	}
	s.active.Store(key, viewer)
	return viewer.Token, func() { s.active.CompareAndDelete(key, viewer) }, nil
}
func (s *ProjectComputerService) hasControlViewer(id string) bool {
	found := false
	s.active.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), s.viewerPrefix(id)) && value.(*computerViewer).Control {
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
	return true, s.repo.SetTakeoverPending(ctx, id, false)
}
