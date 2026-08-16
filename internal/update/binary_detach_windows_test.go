//go:build windows

package update

import (
	"os/exec"
	"testing"
)

func TestConfigureDetachedHelperCreatesIndependentProcess(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureDetachedHelper(cmd)
	want := uint32(createNewProcessGroup | detachedProcess | createBreakawayFromJob)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&want != want {
		t.Fatalf("detached helper attributes = %#v", cmd.SysProcAttr)
	}
}
