//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"
	"syscall"
)

func startBrowserProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func stopBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Chrome uses child processes that can continue writing to the profile after
	// the browser process exits. Kill the isolated process group before TempDir
	// cleanup so those writes cannot race RemoveAll.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}
