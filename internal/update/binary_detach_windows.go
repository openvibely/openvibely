//go:build windows

package update

import (
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
	createBreakawayFromJob = 0x01000000
)

func configureDetachedHelper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess | createBreakawayFromJob, HideWindow: true}
}

func relaxDetachedHelperBreakaway(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createBreakawayFromJob == 0 {
		return false
	}
	cmd.SysProcAttr.CreationFlags &^= createBreakawayFromJob
	return true
}
