//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package handler

import (
	"os/exec"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func startHandlerBrowserProcess(t testing.TB, cmd *exec.Cmd) error {
	testutil.ConfigureBrowserStorage(t, cmd)
	return cmd.Start()
}

func killHandlerBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func stopHandlerBrowserProcess(cmd *exec.Cmd) {
	killHandlerBrowserProcess(cmd)
	_ = cmd.Wait()
}

func stopHandlerBrowserProcessWithWait(cmd *exec.Cmd, waited <-chan struct{}) {
	killHandlerBrowserProcess(cmd)
	<-waited
}
