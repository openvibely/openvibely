//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package pages

import "os/exec"

func startBrowserProcess(cmd *exec.Cmd) error {
	return cmd.Start()
}

func stopBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
