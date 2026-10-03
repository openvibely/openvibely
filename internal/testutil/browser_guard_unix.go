//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// browserGuardScript runs the browser in the wrapper's process group and kills that
// group if the test process disappears, so a killed or timed-out test cannot leave a
// headless browser polling a port that a later test server may reuse. On SIGTERM the
// wrapper stays alive until the browser is reaped, so callers waiting on the wrapper
// do not return while the browser is still flushing its profile directory.
const browserGuardScript = `stopping=
trap 'stopping=1' TERM
"$@" &
child=$!
while [ -z "$stopping" ] && kill -0 "$OPENVIBELY_TEST_PARENT_PID" 2>/dev/null && kill -0 "$child" 2>/dev/null; do sleep 0.5; done
if [ -z "$stopping" ] && kill -0 "$child" 2>/dev/null; then kill -KILL -- -$$; fi
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
	// Without a mock keychain, macOS Chrome blocks startup on securityd, which stalls for
	// many seconds when several test browsers launch concurrently.
	cmd.Args = append([]string{"/bin/sh", "-c", browserGuardScript, "sh", cmd.Path, "--use-mock-keychain"}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
}

// StopBrowserProcessGroup terminates a browser started via GuardBrowserProcess and
// returns only once no live member of its process group remains. kill(-pgid, 0)
// stops matching processes that are still exiting, and such processes can finish an
// in-flight write into the profile after a caller has started removing it.
func StopBrowserProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if !waitForProcessGroup(pgid, 2*time.Second) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		waitForProcessGroup(pgid, 5*time.Second)
	}
	<-waited
}

func waitForProcessGroup(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil && !processGroupHasLiveMembers(pgid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func processGroupHasLiveMembers(pgid int) bool {
	out, err := exec.Command("ps", "-A", "-o", "pgid=,stat=").Output()
	if err != nil {
		return false
	}
	want := strconv.Itoa(pgid)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == want && !strings.HasPrefix(fields[1], "Z") {
			return true
		}
	}
	return false
}
