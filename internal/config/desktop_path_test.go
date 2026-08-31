package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeDesktopPATHUsesShellPATHBeforeCurrentPATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	shellGo := filepath.Join(t.TempDir(), "custom", "go", "bin")
	shellTools := filepath.Join(t.TempDir(), "tools", "bin")
	currentPATH := strings.Join([]string{"/usr/bin", "/bin"}, sep)
	shellPATH := strings.Join([]string{shellGo, shellTools, "/usr/bin"}, sep)

	got := mergeDesktopPATH(currentPATH, shellPATH, false)
	parts := strings.Split(got, sep)

	if parts[0] != shellGo {
		t.Fatalf("expected shell PATH entry first, got PATH=%q", got)
	}
	if parts[1] != shellTools {
		t.Fatalf("expected second shell PATH entry, got PATH=%q", got)
	}
	if strings.Count(got, "/usr/bin") != 1 {
		t.Fatalf("expected duplicate current PATH entry to be removed, got PATH=%q", got)
	}
	if !strings.HasSuffix(got, sep+"/bin") {
		t.Fatalf("expected inherited current PATH entries to be preserved at end, got PATH=%q", got)
	}
}

func TestMergeDesktopPATHHandlesEmptyCurrentPATH(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")

	got := mergeDesktopPATH("", dir, false)
	if got != dir {
		t.Fatalf("expected PATH=%q got %q", dir, got)
	}
}

func TestMergeDesktopPATHHandlesEmptyShellPATH(t *testing.T) {
	currentPATH := strings.Join([]string{"/usr/bin", "/bin"}, string(os.PathListSeparator))

	got := mergeDesktopPATH(currentPATH, "", false)
	if got != currentPATH {
		t.Fatalf("expected PATH unchanged, got %q want %q", got, currentPATH)
	}
}

func TestMergeDesktopPATHUpdatesProcessEnv(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	dir := filepath.Join(t.TempDir(), "bin")

	newPATH := mergeDesktopPATH(os.Getenv("PATH"), dir, true)

	if !strings.HasPrefix(newPATH, dir) {
		t.Fatalf("expected new PATH to start with shell path %q, got %q", dir, newPATH)
	}
	if got := os.Getenv("PATH"); got != newPATH {
		t.Fatalf("expected process PATH to be updated to %q, got %q", newPATH, got)
	}
}

func TestExtractMarkedPATH(t *testing.T) {
	output := "startup noise\n" + desktopPathBeginMarker + "/custom/go/bin:/usr/bin" + desktopPathEndMarker + "\nmore noise\n"

	got, ok := extractMarkedPATH(output)
	if !ok {
		t.Fatalf("expected PATH marker to be found")
	}
	if got != "/custom/go/bin:/usr/bin" {
		t.Fatalf("unexpected PATH %q", got)
	}
}

func TestExtractMarkedPATHUsesLastMarker(t *testing.T) {
	output := desktopPathBeginMarker + "/old" + desktopPathEndMarker + "\n" +
		desktopPathBeginMarker + "/new" + desktopPathEndMarker

	got, ok := extractMarkedPATH(output)
	if !ok {
		t.Fatalf("expected PATH marker to be found")
	}
	if got != "/new" {
		t.Fatalf("expected last marked PATH, got %q", got)
	}
}

func TestExtractMarkedPATHRejectsUnmarkedOutput(t *testing.T) {
	if got, ok := extractMarkedPATH("/custom/go/bin:/usr/bin"); ok {
		t.Fatalf("expected unmarked output to be rejected, got %q", got)
	}
}

func TestDefaultDesktopShellAndReadShellPATHFailures(t *testing.T) {
	if runtime.GOOS != "windows" && defaultDesktopShell() == "" {
		t.Fatal("expected a default shell on non-Windows systems")
	}
	if _, err := readShellPATH(t.Context(), filepath.Join(t.TempDir(), "missing-shell")); err == nil {
		t.Fatal("expected readShellPATH to fail for missing shell")
	}
}

func TestReadShellPATHReadsUserShellEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell PATH bootstrap is not used on windows")
	}

	tmp := t.TempDir()
	shell := filepath.Join(tmp, "shell")
	customGoBin := filepath.Join(tmp, "wherever", "go", "bin")
	script := "#!/bin/sh\nPATH=\"" + customGoBin + ":$PATH\"\nfor arg do cmd=\"$arg\"; done\nexec /bin/sh -c \"$cmd\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}

	got, err := readShellPATH(t.Context(), shell)
	if err != nil {
		t.Fatalf("readShellPATH returned error: %v", err)
	}
	if !strings.HasPrefix(got, customGoBin+string(os.PathListSeparator)) {
		t.Fatalf("expected shell-initialized PATH to include custom Go dir first, got %q", got)
	}
}

func TestEnsureDesktopPATHMergesUserShellPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell PATH bootstrap is not used on windows")
	}

	tmp := t.TempDir()
	shell := filepath.Join(tmp, "shell")
	customGoBin := filepath.Join(tmp, "custom", "go", "bin")
	script := "#!/bin/sh\nPATH=\"" + customGoBin + ":$PATH\"\nfor arg do cmd=\"$arg\"; done\nexec /bin/sh -c \"$cmd\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatalf("write shell: %v", err)
	}

	minimal := strings.Join([]string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator))
	t.Setenv("PATH", minimal)
	t.Setenv("SHELL", shell)

	got := EnsureDesktopPATH()
	if !strings.HasPrefix(got, customGoBin+string(os.PathListSeparator)) {
		t.Fatalf("expected desktop PATH to be derived from user shell, got %q", got)
	}
	if os.Getenv("PATH") != got {
		t.Fatalf("expected process PATH to be updated, got %q want %q", os.Getenv("PATH"), got)
	}
}
