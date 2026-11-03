//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package handler

import (
	"os"
	"os/exec"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func startHandlerBrowserProcess(t testing.TB, cmd *exec.Cmd) error {
	if native := os.Getenv("OPENVIBELY_NATIVE_TEST_HOST"); native != "" && cmd.Path == native {
		testutil.ConfigureNativeBrowserStorage(t, cmd)
	} else {
		testutil.ConfigureBrowserStorage(t, cmd)
	}
	return cmd.Start()
}

func killHandlerBrowserProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func stopHandlerBrowserProcess(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	if testutil.IsNativeBrowser(cmd) {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		if err := testutil.StopNativeBrowser(cmd, done); err != nil {
			t.Errorf("stop native host: %v", err)
		}
		return
	}
	killHandlerBrowserProcess(cmd)
	_ = cmd.Wait()
}

func stopHandlerBrowserProcessWithWait(cmd *exec.Cmd, waited <-chan struct{}) {
	killHandlerBrowserProcess(cmd)
	<-waited
}
