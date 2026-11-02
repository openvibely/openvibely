//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func startBrowserProcess(t testing.TB, cmd *exec.Cmd) error {
	testutil.ConfigureBrowserStorage(t, cmd)
	testutil.GuardBrowserProcess(cmd)
	return cmd.Start()
}

func stopBrowserProcess(cmd *exec.Cmd) {
	testutil.StopBrowserProcessGroup(cmd)
}
