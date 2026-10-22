//go:build !windows

package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessStopKillsDescendants(t *testing.T) {
	m := testProcessManager(t)
	dir := t.TempDir()
	// A descendant would create this file after the owning shell is stopped
	// unless the whole process group is terminated.
	out, err := m.start(context.Background(), dir, "(sleep 1; echo leaked > child-survived) & wait", 10, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.interact(context.Background(), dir, processID(t, out), "", false, true, time.Second)
	if err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "child-survived")); !os.IsNotExist(err) {
		t.Fatalf("descendant survived: %v", err)
	}
}

func TestExecutionCancellationKillsDescendants(t *testing.T) {
	m := testProcessManager(t)
	dir := t.TempDir()
	owner, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := m.start(WithProcessLifetime(owner), dir, "(sleep 1; echo leaked > child-survived) & wait", 10, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	session := m.sessions[processID(t, out)]
	m.mu.Unlock()
	cancel()
	select {
	case <-session.done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not stop")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "child-survived")); !os.IsNotExist(err) {
		t.Fatalf("descendant survived cancellation: %v", err)
	}
}
