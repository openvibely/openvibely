package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

func (s *ProjectComputerService) SetIdleMinutes(ctx context.Context, id string, minutes int) error {
	if minutes < 0 || minutes > 10080 {
		return errors.New("choose 0 (Never) or an idle timeout of 1–10080 minutes")
	}
	root := *s
	root.taskID = ""
	root.repo = s.projects.Computers()
	gate := root.lifecycle(id)
	gate.Lock()
	defer gate.Unlock()
	if err := root.repo.Configure(ctx, id, s.provisioner.Provider()); err != nil {
		return err
	}
	if err := root.repo.SetIdleMinutes(ctx, id, minutes); err != nil {
		return err
	}
	return root.updateViewerPolicy(ctx, id, minutes)
}

// StartIdleMaintenance reconciles existing computers on startup and retries
// transient failures on subsequent sweeps. Its lifetime follows the server.
func (s *ProjectComputerService) StartIdleMaintenance(ctx context.Context) {
	go s.runIdleMaintenance(ctx, time.Minute)
}
func (s *ProjectComputerService) runIdleMaintenance(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		projects, err := s.projects.List(ctx)
		if err == nil {
			for _, project := range projects {
				id := project.ID
				key := "idle-maintenance:" + id
				if _, busy := s.active.LoadOrStore(key, true); busy {
					continue
				}
				go func() {
					defer s.active.Delete(key)
					sweep, cancel := context.WithTimeout(ctx, 45*time.Second)
					defer cancel()
					_ = s.reconcileDeletedDesktops(sweep, id)
					_ = s.reapIdleDesktops(sweep, id)
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *ProjectComputerService) reapIdleDesktops(ctx context.Context, id string) error {
	root := *s
	root.taskID = ""
	root.repo = s.projects.Computers()
	gate := root.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	state, err := root.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !state.Enabled || state.IdleMinutes == 0 || state.Held || state.TakeoverPending || state.HandoffKind != "" {
		return nil
	}
	ids, err := root.repo.IdleDesktopCandidates(ctx, id)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(ids))
	for _, task := range ids {
		// Never wait behind a live operation: takeover must still be able to
		// cancel it promptly. Hold control and task locks only for idle tasks.
		value, _ := root.locks.LoadOrStore("control:"+id+":"+task, &sync.Mutex{})
		control := value.(*sync.Mutex)
		if !control.TryLock() {
			continue
		}
		scoped := root
		scoped.taskID = task
		taskLock := scoped.lock(id)
		if !taskLock.TryLock() {
			control.Unlock()
			continue
		}
		repo, err := root.repo.ForTask(ctx, id, task)
		if err != nil {
			taskLock.Unlock()
			control.Unlock()
			continue
		}
		current, err := repo.Get(ctx, id)
		if err != nil || current.Held || current.TakeoverPending || current.HandoffKind != "" || (current.Owner != "" && current.LeaseUntil >= time.Now().Unix()) {
			taskLock.Unlock()
			control.Unlock()
			continue
		}
		defer control.Unlock()
		defer taskLock.Unlock()
		names = append(names, ComputerDesktopID(task))
	}
	conn, err := root.provisioner.Resolve(ctx, id)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"idle_seconds": state.IdleMinutes * 60, "candidates": names})
	_, err = root.request(ctx, conn, "POST", "/desktop-reap", body)
	return err
}
