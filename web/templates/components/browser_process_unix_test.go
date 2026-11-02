//go:build !windows

package components

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func configureTestBrowserProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startTestBrowserProcess(t testing.TB, cmd *exec.Cmd) error {
	testutil.ConfigureBrowserStorage(t, cmd)
	testutil.GuardBrowserProcess(cmd)
	return cmd.Start()
}

func stopTestBrowserProcess(cmd *exec.Cmd) {
	testutil.StopBrowserProcessGroup(cmd)
}
