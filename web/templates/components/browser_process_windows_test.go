//go:build windows

package components

import (
	"os/exec"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func configureTestBrowserProcess(_ *exec.Cmd) {}

func startTestBrowserProcess(t testing.TB, cmd *exec.Cmd) error {
	testutil.ConfigureBrowserStorage(t, cmd)
	return cmd.Start()
}

func stopTestBrowserProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
