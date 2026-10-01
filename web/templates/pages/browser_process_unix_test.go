//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"

	"github.com/openvibely/openvibely/internal/testutil"
)

func startBrowserProcess(cmd *exec.Cmd) error {
	testutil.GuardBrowserProcess(cmd)
	return cmd.Start()
}

func stopBrowserProcess(cmd *exec.Cmd) {
	testutil.StopBrowserProcessGroup(cmd)
}
