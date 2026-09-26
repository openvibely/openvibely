//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package testutil

import "os/exec"

// GuardBrowserProcess is a no-op on platforms without POSIX process groups.
func GuardBrowserProcess(cmd *exec.Cmd) {}
