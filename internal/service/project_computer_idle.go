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
	return root.repo.SetIdleMinutes(ctx, id, minutes)
}

func (s *ProjectComputerService) startIdleMonitor(id string) {
	// Only real runtimes implement the supervisor protocol. Avoid starting timers
	// for lightweight injected provisioners used by tests and integrations.
	switch s.provisioner.(type) {
	case *LocalComputerProvisioner, *HostedComputerProvisioner:
	default:
		return
	}
	key := "idle-monitor:" + id
	if _, loaded := s.active.LoadOrStore(key, true); loaded {
		return
	}
	root := *s
	root.taskID = ""
	root.repo = s.projects.Computers()
	go func() {
		defer root.active.Delete(key)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			_, err := root.provisioner.Resolve(ctx, id)
			if err == nil {
				_ = root.reapIdleDesktops(ctx, id)
			}
			cancel()
			// A stopped/deleted project should not retain a permanent timer. Any later
			// computer connection starts the monitor again. Failures never delete data.
			if err != nil {
				return
			}
		}
	}()
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
	if state.IdleMinutes == 0 || state.Held || state.TakeoverPending || state.HandoffKind != "" {
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
