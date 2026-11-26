//go:build !windows

package update

import (
	"os/exec"
	"testing"
)

func killExecutablePlatform(t *testing.T, executables ...string) bool { return false }

func registerPackagedCleanupDiagnostics(t *testing.T, root string) {}

func startPackagedApp(cmd *exec.Cmd) (func() error, error) {
	return func() error { return nil }, cmd.Start()
}
