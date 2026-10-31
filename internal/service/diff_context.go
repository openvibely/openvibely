package service

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Bound pipe cleanup too: a Git child may inherit stdout after Git is killed.
func diffGitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = time.Second
	return cmd
}

func diffGitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := diffGitCommand(ctx, args...)
	cmd.Dir = dir
	return cmd.Output()
}

// TryRLock avoids leaving a goroutine queued on a lifecycle lock after timeout.
func diffReadLock(ctx context.Context, lock *sync.RWMutex) bool {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if lock.TryRLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
