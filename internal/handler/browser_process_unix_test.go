//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package handler

import (
	"os/exec"
	"sync"
	"syscall"

	"github.com/openvibely/openvibely/internal/testutil"
)

var handlerBrowserProcessSlots sync.Map

func startHandlerBrowserProcess(cmd *exec.Cmd) error {
	slot, err := testutil.AcquireBrowserSlot()
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		slot.Release()
		return err
	}
	handlerBrowserProcessSlots.Store(cmd, slot)
	return nil
}

func runHandlerBrowserProcess(cmd *exec.Cmd) ([]byte, error) {
	slot, err := testutil.AcquireBrowserSlot()
	if err != nil {
		return nil, err
	}
	defer slot.Release()
	return cmd.CombinedOutput()
}

func stopHandlerBrowserProcess(cmd *exec.Cmd) {
	defer releaseHandlerBrowserProcessSlot(cmd)
	if cmd.Process == nil {
		return
	}
	// Chrome leaves child processes behind that can keep writing to the profile.
	// Stop the isolated process group before t.TempDir cleanup runs.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func releaseHandlerBrowserProcessSlot(cmd *exec.Cmd) {
	if slot, ok := handlerBrowserProcessSlots.LoadAndDelete(cmd); ok {
		slot.(*testutil.BrowserSlot).Release()
	}
}
