//go:build windows

package components

import "os/exec"

func configureTestBrowserProcess(_ *exec.Cmd) {}

func stopTestBrowserProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
