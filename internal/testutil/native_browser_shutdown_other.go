//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package testutil

import "os/exec"

func forceStopNativeBrowser(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func cleanupNativeBrowserAfterExit(cmd *exec.Cmd) error { return nil }
