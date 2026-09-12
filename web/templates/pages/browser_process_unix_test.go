//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"
	"sync"
	"syscall"
	"time"

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

func releaseBrowserProcessSlot(cmd *exec.Cmd) {
	if slot, ok := browserProcessSlots.LoadAndDelete(cmd); ok {
		slot.(*testutil.BrowserSlot).Release()
	}
}
