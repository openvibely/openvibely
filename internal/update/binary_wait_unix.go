//go:build !windows

package update

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

func waitForProcessExitPlatform(ctx context.Context, pid int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("timed out waiting for parent process to exit")
		case <-ticker.C:
		}
	}
}
