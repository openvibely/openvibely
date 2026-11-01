//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomePathBoundaries(t *testing.T) {
	for _, path := range []string{"../etc/passwd", "/etc/passwd", ".browser/Cookies", "/home/ubuntu/.browser/Default/Cookies"} {
		if _, err := homePath(path); err == nil {
			t.Fatal(path)
		}
	}
	if path, err := homePath("/home/ubuntu/Files/result.csv"); err != nil || path != "Files/result.csv" {
		t.Fatal(path, err)
	}
}
func TestShellOutputBounded(t *testing.T) {
	out := &limitedOutput{}
	out.Write([]byte(strings.Repeat("x", 70000)))
	out.Write([]byte("tail"))
	if len(out.text) != 65536 || !out.truncated {
		t.Fatal("output not capped")
	}
}

func TestNativeFilesStayRootedAndEditPrecisely(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx := context.Background()
	p := nativeFileRequest{Operation: "write_file"}
	p.Input.FilePath = "nested/result.txt"
	p.Input.Content = "same same"
	if _, err = nativeFileOperation(ctx, root, p); err != nil {
		t.Fatal(err)
	}
	p.Operation = "edit_file"
	p.Input.Old = "same"
	p.Input.New = "changed"
	if _, err = nativeFileOperation(ctx, root, p); err == nil {
		t.Fatal("ambiguous replacement accepted")
	}
	p.Input.All = true
	if _, err = nativeFileOperation(ctx, root, p); err != nil {
		t.Fatal(err)
	}
	p.Operation = "read_file"
	out, err := nativeFileOperation(ctx, root, p)
	if err != nil || out != "changed changed" {
		t.Fatal(out, err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(rootPath, "Files", "escape")); err != nil {
		t.Fatal(err)
	}
	p.Operation = "write_file"
	p.Input.FilePath = "escape/out.txt"
	if _, err = nativeFileOperation(ctx, root, p); err == nil {
		t.Fatal("symlink escaped root")
	}
	p.Input.FilePath = "/etc/passwd"
	if _, err = nativeFileOperation(ctx, root, p); err == nil {
		t.Fatal("absolute host path accepted")
	}
}

func TestBackgroundShellTimeoutPolicy(t *testing.T) {
	if got := computerShellTimeout(600, true); got != 600 {
		t.Fatal(got)
	}
	if got := computerShellTimeout(600, false); got != 180 {
		t.Fatal(got)
	}
	if got := computerShellTimeout(7200, true); got != 3600 {
		t.Fatal(got)
	}
}

func TestTaskDesktopFileRoots(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-fixture")
	if got := computerHome(); got != "/home/ubuntu/.desktops/task-fixture/home" {
		t.Fatal(got)
	}
	for _, path := range []string{"../other", "/etc/passwd", "/home/ubuntu/.browser/Cookies", "/home/ubuntu/.desktops/other/browser/Cookies"} {
		if got, err := homePath(path); err == nil {
			t.Fatalf("allowed private or outside path %s -> %s", path, got)
		}
	}
	for _, path := range []string{"Files/test.txt", "/home/ubuntu/Files/test.txt", computerHome() + "/Files/test.txt"} {
		got, err := homePath(path)
		if err != nil || got != "Files/test.txt" {
			t.Fatalf("%s => %s %v", path, got, err)
		}
	}
}
