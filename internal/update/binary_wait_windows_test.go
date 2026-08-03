//go:build windows

package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWaitForProcessExitWindowsDoesNotTreatLiveProcessAsExited(t *testing.T) {
	err := waitForProcessExit(context.Background(), os.Getpid(), 20*time.Millisecond)
	if err == nil || err.Error() != "timed out waiting for parent process to exit" {
		t.Fatalf("wait for live process = %v, want timeout", err)
	}
}

func TestWaitForProcessExitWindowsObservesChildExit(t *testing.T) {
	if os.Getenv("OPENVIBELY_TEST_WAIT_CHILD") == "1" {
		time.Sleep(150 * time.Millisecond)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestWaitForProcessExitWindowsObservesChildExit")
	cmd.Env = append(os.Environ(), "OPENVIBELY_TEST_WAIT_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForProcessExit(ctx, cmd.Process.Pid, 4*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestStopStartedProcessWindowsTerminatesAndReapsSuccessor(t *testing.T) {
	if os.Getenv("OPENVIBELY_TEST_STOP_CHILD") == "1" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestStopStartedProcessWindowsTerminatesAndReapsSuccessor")
	cmd.Env = append(os.Environ(), "OPENVIBELY_TEST_STOP_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := stopStartedProcess(cmd); err != nil {
		t.Fatal(err)
	}
	if err := waitForProcessExit(context.Background(), cmd.Process.Pid, time.Second); err != nil {
		t.Fatalf("stopped successor remained active: %v", err)
	}
}

func TestWaitForProcessExitWindowsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForProcessExit(ctx, os.Getpid(), time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation = %v, want context canceled", err)
	}
}
