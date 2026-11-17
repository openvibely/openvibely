//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBrowserAndShellShareFilesWithoutSharingCancellation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise task identities")
	}
	t.Setenv("OPENVIBELY_ISOLATED", "1")
	t.Setenv("OPENVIBELY_DESKTOP_UID", "20000")
	dir, err := os.MkdirTemp("/tmp", "ov-shared-user-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err = os.Chown(dir, 20000, 20000); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "download.txt")
	if out, err := desktopCommand(context.Background(), "/bin/sh", "-c", fmt.Sprintf("umask 077; printf downloaded > %q", file)).CombinedOutput(); err != nil {
		t.Fatalf("desktop write: %s %v", out, err)
	}
	shell, err := shellCommand(context.Background(), fmt.Sprintf("cat %q; printf edited >> %q; id -u", file, file), dir)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := shell.CombinedOutput(); err != nil || string(out) != "downloaded20000\n" {
		t.Fatalf("shell cannot use desktop file as same UID: %s %v", out, err)
	}
	if out, err := desktopCommand(context.Background(), "cat", file).CombinedOutput(); err != nil || string(out) != "downloadededited" {
		t.Fatalf("desktop cannot read shell edit: %s %v", out, err)
	}
	desktop := desktopCommand(context.Background(), "sleep", "60")
	if err = desktop.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { desktop.Process.Kill(); desktop.Wait() }()
	if shellProcessesRunning() {
		t.Fatal("desktop counted as shell work")
	}
	shell, err = shellCommand(context.Background(), "exec setsid sleep 60", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = shell.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { shell.Process.Kill(); shell.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for !shellProcessesRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !shellProcessesRunning() {
		t.Fatal("shell job was not detected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = stopShellProcesses(ctx); err != nil {
		t.Fatal(err)
	}
	if err = desktop.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("cancelling shell killed desktop: %v", err)
	}
}

func TestTaskEnvironmentUsesOneUser(t *testing.T) {
	env := strings.Join(taskEnvironment("task-shared-user"), "\n")
	if strings.Contains(env, "OPENVIBELY_SHELL_UID=") {
		t.Fatal("task still configures a separate shell user")
	}
	if !strings.Contains(env, fmt.Sprintf("OPENVIBELY_DESKTOP_UID=%d", desktopUID("task-shared-user"))) {
		t.Fatal("missing task user")
	}
}
