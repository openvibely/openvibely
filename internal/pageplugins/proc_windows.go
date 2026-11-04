//go:build windows

package pageplugins

import "os/exec"

func configureProcAttr(cmd *exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
