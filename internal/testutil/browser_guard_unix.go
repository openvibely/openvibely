//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// browserGuardScript runs the browser in the wrapper's process group and kills that
// group if the test process disappears, so a killed or timed-out test cannot leave a
// headless browser polling a port that a later test server may reuse.
const browserGuardScript = `"$@" &
child=$!
while kill -0 "$OPENVIBELY_TEST_PARENT_PID" 2>/dev/null && kill -0 "$child" 2>/dev/null; do sleep 0.5; done
if kill -0 "$child" 2>/dev/null; then kill -KILL -- -$$; fi
wait "$child"`

// GuardBrowserProcess rewrites an unstarted browser command so the browser and its
// children die with the current test process. Callers still stop the process group
// normally; cmd.Process then refers to the wrapper, which leads the group.
func GuardBrowserProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process != nil || cmd.Err != nil || len(cmd.Args) == 0 {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = append(env, "OPENVIBELY_TEST_PARENT_PID="+strconv.Itoa(os.Getpid()))
	cmd.Args = append([]string{"/bin/sh", "-c", browserGuardScript, "sh", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
}
