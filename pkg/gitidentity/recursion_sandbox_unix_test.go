//go:build darwin || linux

package gitidentity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Invoked before TestMain initializes anything. Apply limits only to this child,
// then replace it with the wrapper. A limit of one forbids additional forks even
// when the identity guard is removed. Never lower the parent test runner's limit.
func runRecursionSandboxHelper() {
	target := os.Getenv("OV_TEST_RECURSION_EXECUTABLE")
	if target == "" {
		return
	}
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "process limits are not enforced for root")
		os.Exit(124)
	}
	if err := unix.Setrlimit(unix.RLIMIT_NPROC, &unix.Rlimit{Cur: 1, Max: 1}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(124)
	}
	if err := syscall.Exec(target, []string{target, "--version"}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(124)
	}
}

func runIsolatedRecursionCommand(cmd *exec.Cmd) ([]byte, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = killGroup
	// Bound pipe draining too: a descendant must not keep CombinedOutput blocked.
	cmd.WaitDelay = time.Second
	defer killGroup()
	return cmd.CombinedOutput()
}

func runLimitedWrapper(t *testing.T, target, real string) ([]byte, error) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("RLIMIT_NPROC does not constrain root")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = []string{
		"OV_TEST_RECURSION_EXECUTABLE=" + target,
		"OV_GIT_REAL_EXECUTABLE=" + real,
		"PATH=" + os.Getenv("PATH"),
	}
	return runIsolatedRecursionCommand(cmd)
}

func TestWrapperRejectsItselfBeforeIdentityLookup(t *testing.T) {
	alias := filepath.Join(t.TempDir(), "git-alias")
	if err := os.Symlink(testRuntime.path, alias); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{testRuntime.path, alias} {
		out, err := runLimitedWrapper(t, testRuntime.path, target)
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 125 || !strings.Contains(string(out), "cannot use itself") {
			t.Fatalf("self target %s: %v: %s", target, err, out)
		}
	}
}

func TestRecursionSandboxPreventsChildProcesses(t *testing.T) {
	script := filepath.Join(t.TempDir(), "try-one-child")
	// Attempt exactly one child, never recursion. Both branches leave a command
	// after the attempt so the shell cannot optimize it into an exec.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif /bin/sh -c 'exit 0'; then exit 0; else exit 42; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := runLimitedWrapper(t, script, "unused")
	exit, ok := err.(*exec.ExitError)
	// Shells report fork refusal differently (dash: 2, macOS sh: 128).
	if !ok || (exit.ExitCode() != 2 && exit.ExitCode() != 42 && exit.ExitCode() != 128) {
		t.Fatalf("expected shell fork refusal, got %v: %s", err, out)
	}
}

func TestRecursionSandboxDeadlineKillsChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	// One known child, without the process limit, to verify group cancellation.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 30 & echo "$!"; wait`)
	out, err := runIsolatedRecursionCommand(cmd)
	if ctx.Err() != context.DeadlineExceeded || err == nil {
		t.Fatalf("expected deadline: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("child did not report its PID: %q", out)
	}
	// A terminated child may briefly remain as a zombie until the OS reaps it.
	deadline := time.Now().Add(time.Second)
	for {
		state, _ := exec.Command("ps", "-p", strconv.Itoa(child), "-o", "stat=").Output()
		if value := strings.TrimSpace(string(state)); value == "" || strings.HasPrefix(value, "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived group cancellation", child)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
