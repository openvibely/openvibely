package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

type computerViewer struct {
	User, Token string
	TaskID      string
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
	viewer := &computerViewer{User: user, Token: rand.Text(), Control: control, TaskID: s.taskID}
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
func (s *ProjectComputerService) ViewerActivity(ctx context.Context, id, user, session string) error {
	value, ok := s.active.Load(s.viewerPrefix(id) + session)
	if !ok || value.(*computerViewer).User != user {
		return errors.New("viewer disconnected")
	}
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"id": value.(*computerViewer).Token})
	_, err = s.request(ctx, conn, "POST", "/viewer-activity", body)
	return err
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

func (s *ProjectComputerService) updateViewerPolicy(ctx context.Context, id string, _ int) error {
	var viewers []*computerViewer
	s.active.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), "viewer:"+id+":") {
			viewers = append(viewers, value.(*computerViewer))
		}
		return true
	})
	if len(viewers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := s.provisioner.Resolve(ctx, id)
	if err != nil {
		return errors.New("timeout saved; reconnect viewers to apply it")
	}
	for _, v := range viewers {
		scoped := conn
		if v.TaskID != "" {
			scoped.Endpoint += "/desktops/" + ComputerDesktopID(v.TaskID)
		}
		// Keep older helpers from expiring a visible viewer when settings change.
		body, _ := json.Marshal(map[string]any{"id": v.Token, "idle_seconds": 0})
		if _, err := s.request(ctx, scoped, "POST", "/viewer-policy", body); err != nil {
			return errors.New("timeout saved; reconnect viewers to apply it")
		}
	}
	return nil
}
