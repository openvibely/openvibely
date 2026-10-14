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
