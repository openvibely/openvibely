package handler

import (
	"runtime"
	"strings"
	"testing"
)

func TestProjectFolderPickerCommandForGOOS(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		name, args, ok := projectFolderPickerCommandForGOOS("darwin")
		if !ok {
			t.Fatal("expected darwin picker command to be available")
		}
		if name != "osascript" {
			t.Fatalf("expected osascript, got %q", name)
		}
		if len(args) == 0 {
			t.Fatal("expected darwin picker args")
		}
	})

	t.Run("windows", func(t *testing.T) {
		name, args, ok := projectFolderPickerCommandForGOOS("windows")
		if !ok {
			t.Fatal("expected windows picker command to be available")
		}
		if name != "powershell" {
			t.Fatalf("expected powershell, got %q", name)
		}
		if len(args) == 0 || !strings.Contains(args[len(args)-1], "FolderBrowserDialog") {
			t.Fatal("expected windows powershell folder dialog script")
		}
	})

	t.Run("linux or unavailable", func(t *testing.T) {
		name, _, ok := projectFolderPickerCommandForGOOS("linux")
		if ok && name == "" {
			t.Fatal("expected linux picker command name when available")
		}
		if !ok && name != "" {
			t.Fatal("expected empty command name when linux picker unavailable")
		}
	})
}

func TestNormalizePickedProjectFolderPath(t *testing.T) {
	t.Run("trim trailing slash", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("non-windows path expectation")
		}
		path, err := normalizePickedProjectFolderPath("/tmp/myrepo/\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if path != "/tmp/myrepo" {
			t.Fatalf("expected /tmp/myrepo, got %q", path)
		}
	})

	t.Run("normalize home shorthand", func(t *testing.T) {
		path, err := normalizePickedProjectFolderPath("~/go/src/repo")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(path, "~") {
			t.Fatalf("expected tilde to be expanded, got %q", path)
		}
	})

	t.Run("reject non absolute", func(t *testing.T) {
		_, err := normalizePickedProjectFolderPath("relative/path")
		if err == nil {
			t.Fatal("expected non-absolute path error")
		}
		if !strings.Contains(err.Error(), "non-absolute") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
