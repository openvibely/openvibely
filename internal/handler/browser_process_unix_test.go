//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package handler

import (
	"os/exec"
	"syscall"

	"github.com/openvibely/openvibely/internal/testutil"
)

func startHandlerBrowserProcess(cmd *exec.Cmd) error {
	testutil.GuardBrowserProcess(cmd)
	return cmd.Start()
}

func runHandlerBrowserProcess(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func stopHandlerBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Chrome leaves child processes behind that can keep writing to the profile.
	// Stop the isolated process group before t.TempDir cleanup runs.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}
