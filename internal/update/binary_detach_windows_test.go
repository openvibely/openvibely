//go:build windows

package update

import (
	"errors"
	"os/exec"
	"strings"
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

func TestStartDetachedHelperRetriesWithoutBreakaway(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureDetachedHelper(cmd)
	var attempts int
	err := startDetachedHelper(cmd, func(command *exec.Cmd) error {
		attempts++
		if attempts == 1 {
			if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags&createBreakawayFromJob == 0 {
				t.Fatalf("first attempt did not request breakaway: %#v", command.SysProcAttr)
			}
			return errors.New("breakaway denied")
		}
		if command.SysProcAttr == nil || command.SysProcAttr.CreationFlags&createBreakawayFromJob != 0 {
			t.Fatalf("retry still requested breakaway: %#v", command.SysProcAttr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestStartDetachedHelperReportsBothStartFailures(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit", "0")
	configureDetachedHelper(cmd)
	err := startDetachedHelper(cmd, func(*exec.Cmd) error {
		return errors.New("start failed")
	})
	if err == nil || !strings.Contains(err.Error(), "start failed") {
		t.Fatalf("error = %v", err)
	}
}
