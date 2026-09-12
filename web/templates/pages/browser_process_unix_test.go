//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"
	"sync"
	"syscall"

	"github.com/openvibely/openvibely/internal/testutil"
)

var browserProcessSlots sync.Map

func startBrowserProcess(cmd *exec.Cmd) error {
	slot, err := testutil.AcquireBrowserSlot()
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		slot.Release()
		return err
	}
	browserProcessSlots.Store(cmd, slot)
	return nil
}

func stopBrowserProcess(cmd *exec.Cmd) {
	defer releaseBrowserProcessSlot(cmd)
	if cmd.Process == nil {
		return
	}
	// Chrome uses child processes that can continue writing to the profile after
	// the browser process exits. Kill the isolated process group before TempDir
	// cleanup so those writes cannot race RemoveAll.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func releaseBrowserProcessSlot(cmd *exec.Cmd) {
	if slot, ok := browserProcessSlots.LoadAndDelete(cmd); ok {
		slot.(*testutil.BrowserSlot).Release()
	}
}
