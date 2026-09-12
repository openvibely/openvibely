//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package handler

import "os/exec"

func startHandlerBrowserProcess(cmd *exec.Cmd) error {
	return cmd.Start()
}

func runHandlerBrowserProcess(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

func stopHandlerBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
