//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func forceStopNativeBrowser(cmd *exec.Cmd) error {
	// GuardBrowserProcess owns a new process group. Killing just its shell would
	// leave the native host and its children running after a shutdown timeout.
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid && cmd.SysProcAttr.Pgid == 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waitForProcessGroup(cmd.Process.Pid, 5*time.Second) {
			return fmt.Errorf("native process group did not exit after forced termination")
		}
		return nil
	}
	return cmd.Process.Kill()
}

func cleanupNativeBrowserAfterExit(cmd *exec.Cmd) error {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid && cmd.SysProcAttr.Pgid == 0 {
		return forceStopNativeBrowser(cmd)
	}
	// The directly launched process has already been reaped; do not signal it.
	return nil
}
