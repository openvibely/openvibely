//go:build !windows

package update

import (
	"os/exec"
	"testing"
)

func TestConfigureDetachedHelperCreatesIndependentSession(t *testing.T) {
	cmd := exec.Command("true")
	configureDetachedHelper(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("detached helper attributes = %#v", cmd.SysProcAttr)
	}
}
