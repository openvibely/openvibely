//go:build darwin || linux

package testutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeBrowserTimeoutStopsGuardedGroup(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("/bin/sh", "-c", `sleep 60 & echo ready > "$1"; wait`, "sh", ready)
	ConfigureNativeBrowserStorage(t, cmd)
	GuardBrowserProcess(cmd)
	if !IsNativeBrowser(cmd) {
		t.Fatal("wrapper lost native host identity")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = forceStopNativeBrowser(cmd) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := stopNativeBrowser(cmd, done, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "graceful shutdown deadline") {
		t.Fatalf("expected timeout failure: %v", err)
	}
	if processGroupHasLiveMembers(cmd.Process.Pid) {
		t.Fatal("guarded native child survived timeout fallback")
	}
}

func TestNativeBrowserContextCancellationStopsGuardedGroup(t *testing.T) {
	for _, phase := range []string{"before-shutdown", "during-shutdown"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 60 & echo ready > "$1"; wait`, "sh", ready)
			ConfigureNativeBrowserStorage(t, cmd)
			GuardBrowserProcess(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			cleaned := false
			defer func() {
				if !cleaned {
					_ = forceStopNativeBrowser(cmd)
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("helper did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if phase == "before-shutdown" {
				cancel()
				// Ensure the first nonblocking receive sees an already-reaped wrapper.
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancelled wrapper exited successfully")
					}
					done <- err
				case <-time.After(5 * time.Second):
					t.Fatal("wrapper did not exit")
				}
				if !processGroupHasLiveMembers(cmd.Process.Pid) {
					t.Fatal("probe did not leave a child alive")
				}
			} else {
				// Cancel only after StopNativeBrowser has sent its graceful request.
				cancelled := make(chan struct{})
				defer func() { <-cancelled }()
				go func() {
					defer close(cancelled)
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(nativeShutdownPath(cmd)); err == nil {
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
				}()
			}
			err := StopNativeBrowser(cmd, done)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("lost wrapper exit error: %v", err)
			}
			if processGroupHasLiveMembers(cmd.Process.Pid) {
				t.Fatal("native child survived context cancellation")
			}
			cleaned = true
		})
	}
}
