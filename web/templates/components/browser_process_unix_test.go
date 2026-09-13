//go:build !windows

package components

import (
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
)

var testBrowserProcessSlots sync.Map

func configureTestBrowserProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startTestBrowserProcess(cmd *exec.Cmd) error {
	slot, err := testutil.AcquireBrowserSlot()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		slot.Release()
		return err
	}
	testBrowserProcessSlots.Store(cmd, slot)
	return nil
}

func stopTestBrowserProcess(cmd *exec.Cmd) {
	defer releaseTestBrowserProcessSlot(cmd)
	if cmd == nil || cmd.Process == nil {
		return
	}

	processGroup := -cmd.Process.Pid
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	_ = syscall.Kill(processGroup, syscall.SIGTERM)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(processGroup, 0); err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(processGroup, syscall.SIGKILL)
	<-waited
}

func releaseTestBrowserProcessSlot(cmd *exec.Cmd) {
	if slot, ok := testBrowserProcessSlots.LoadAndDelete(cmd); ok {
		slot.(*testutil.BrowserSlot).Release()
	}
}
