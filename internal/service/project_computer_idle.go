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
	return nil
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
	conn, err := root.provisioner.Resolve(ctx, id)
	if err != nil {
		return err
	}
	// Refresh protection once for the full candidate set. Per-desktop requests
	// must not refresh the idle clocks of unrelated desktops.
	names := make([]string, 0, len(ids))
	for _, task := range ids {
		names = append(names, ComputerDesktopID(task))
	}
	body, _ := json.Marshal(map[string]any{"idle_seconds": state.IdleMinutes * 60, "candidates": names})
	if _, err = root.request(ctx, conn, "POST", "/desktop-idle-protection", body); err != nil {
		return err
	}
	var failures []error
	for _, task := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := root.reapIdleTask(ctx, id, task, conn, state.IdleMinutes); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *ProjectComputerService) reapIdleTask(ctx context.Context, id, task string, conn ComputerConnection, minutes int) error {
	value, _ := s.locks.LoadOrStore("control:"+id+":"+task, &sync.Mutex{})
	control := value.(*sync.Mutex)
	if !control.TryLock() {
		return nil
	}
	defer control.Unlock()
	scoped := *s
	scoped.taskID = task
	lock := scoped.lock(id)
	if !lock.TryLock() {
		return nil
	}
	defer lock.Unlock()
	// Re-read eligibility after acquiring this task's locks: it may have
	// started work or entered takeover while another desktop was stopping.
	ids, err := s.repo.IdleDesktopCandidates(ctx, id)
	if err != nil {
		return err
	}
	eligible := false
	for _, candidate := range ids {
		if candidate == task {
			eligible = true
			break
		}
	}
	if !eligible {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"idle_seconds": minutes * 60, "candidates": []string{ComputerDesktopID(task)}})
	_, err = s.request(ctx, conn, "POST", "/desktop-reap-one", body)
	return err
}
