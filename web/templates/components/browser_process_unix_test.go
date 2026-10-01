//go:build !windows

package components

import (
	"os/exec"
	"syscall"

	"github.com/openvibely/openvibely/internal/testutil"
)

func configureTestBrowserProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startTestBrowserProcess(cmd *exec.Cmd) error {
	testutil.GuardBrowserProcess(cmd)
	return cmd.Start()
}

func stopTestBrowserProcess(cmd *exec.Cmd) {
	testutil.StopBrowserProcessGroup(cmd)
}
