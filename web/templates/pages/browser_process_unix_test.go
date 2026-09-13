//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package pages

import (
	"os/exec"
	"syscall"
	"time"
)

func startBrowserProcess(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func stopBrowserProcess(cmd *exec.Cmd) {
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
