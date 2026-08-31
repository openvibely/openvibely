//go:build windows

package update

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

func waitForProcessExitPlatform(ctx context.Context, pid int, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("open parent process: %w", err)
	}
	defer windows.CloseHandle(handle)

	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("timed out waiting for parent process to exit")
		}
		wait := remaining
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		result, err := windows.WaitForSingleObject(handle, uint32(wait.Milliseconds()))
		if err != nil {
			return fmt.Errorf("wait for parent process: %w", err)
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			return nil
		case uint32(windows.WAIT_TIMEOUT):
			continue
		default:
			return fmt.Errorf("wait for parent process returned status 0x%x", result)
		}
	}
}
