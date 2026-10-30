//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestGuardBrowserProcessAddsMockKeychainOnlyForChrome(t *testing.T) {
	for _, test := range []struct {
		name         string
		path         string
		wantMockFlag bool
	}{
		{name: "Chrome", path: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", wantMockFlag: true},
		{name: "Chromium", path: "/usr/bin/chromium-browser", wantMockFlag: true},
		{name: "helper binary", path: "/tmp/handler.test", wantMockFlag: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := &exec.Cmd{Path: test.path, Args: []string{test.path, "original-argument"}}
			GuardBrowserProcess(cmd)
			if got := slices.Contains(cmd.Args, "--use-mock-keychain"); got != test.wantMockFlag {
				t.Fatalf("mock-keychain flag present = %v, want %v; args = %q", got, test.wantMockFlag, cmd.Args)
			}
			if !slices.Contains(cmd.Args, "original-argument") {
				t.Fatalf("original argument lost: %q", cmd.Args)
			}
		})
	}
}

func TestStopBrowserProcessGroupWithExistingWait(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	// Leave a child that ignores graceful shutdown in the browser's group.
	cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; sleep 60 & echo ready > "$1"; wait`, "sh", ready)
	GuardBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() { StopBrowserProcessGroupWithWait(cmd, waited) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	StopBrowserProcessGroupWithWait(cmd, waited)
	select {
	case <-waited:
	default:
		t.Fatal("returned before command Wait completed")
	}
	if processGroupHasLiveMembers(cmd.Process.Pid) {
		t.Fatal("browser children survived cleanup")
	}
}
