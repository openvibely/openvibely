package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/testutil/nativehost"
)

// StopNativeBrowser requests app.Quit and waits for the caller's cmd.Wait result.
// On timeout it kills the host and reports failure, rather than silently treating
// forced termination as a successful graceful shutdown.
func StopNativeBrowser(cmd *exec.Cmd, done <-chan error) error {
	return stopNativeBrowser(cmd, done, 5*time.Second)
}

// IsNativeBrowser uses the shutdown metadata attached before any process wrapper.
func IsNativeBrowser(cmd *exec.Cmd) bool { return nativeShutdownPath(cmd) != "" }

func nativeShutdownPath(cmd *exec.Cmd) string {
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, nativehost.ShutdownFileEnv+"=") {
			return strings.TrimPrefix(entry, nativehost.ShutdownFileEnv+"=")
		}
	}
	return ""
}

func stopNativeBrowser(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	if cmd.Process == nil {
		return nil
	}
	select {
	case err := <-done:
		return nativeBrowserExitResult(cmd, err)
	default:
	}
	path := nativeShutdownPath(cmd)
	requestErr := fmt.Errorf("native shutdown path missing")
	if path != "" {
		requestErr = os.WriteFile(path, []byte("quit\n"), 0600)
	}
	if requestErr == nil {
		select {
		case err := <-done:
			return nativeBrowserExitResult(cmd, err)
		case <-time.After(timeout):
		}
	}
	forceErr := forceStopNativeBrowser(cmd)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("native host did not exit after forced termination")
	}
	if forceErr != nil {
		return forceErr
	}
	if requestErr != nil {
		return fmt.Errorf("request native shutdown: %w", requestErr)
	}
	return fmt.Errorf("native host exceeded graceful shutdown deadline; forcibly terminated")
}

// A cancelled CommandContext can reap only the guard shell while its children
// remain alive. Preserve the exit error, but clean up its owned group first.
func nativeBrowserExitResult(cmd *exec.Cmd, err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(err, cleanupNativeBrowserAfterExit(cmd))
}
