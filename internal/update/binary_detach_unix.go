//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

func configureDetachedHelper(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
