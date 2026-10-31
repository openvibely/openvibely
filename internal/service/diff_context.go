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

// Queue with RLock so back-to-back writers cannot starve the reader. On
// cancellation the queued goroutine releases the lock as soon as it gets it.
func diffReadLock(ctx context.Context, lock *sync.RWMutex) bool {
	if ctx.Err() != nil {
		return false
	}
	acquired := make(chan struct{})
	go func() {
		lock.RLock()
		close(acquired)
	}()
	select {
	case <-acquired:
		return true
	case <-ctx.Done():
		go func() {
			<-acquired
			lock.RUnlock()
		}()
		return false
	}
}
