//go:build windows

package update

import (
	"os/exec"
	"testing"
)

func TestConfigureDetachedHelperCreatesIndependentProcess(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureDetachedHelper(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&(createNewProcessGroup|detachedProcess) != createNewProcessGroup|detachedProcess {
		t.Fatalf("detached helper attributes = %#v", cmd.SysProcAttr)
	}
}
