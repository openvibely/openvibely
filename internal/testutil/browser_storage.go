package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ConfigureBrowserStorage isolates each Chrome launch's persistent and scratch
// data. Call before GuardBrowserProcess/Start, and stop the browser before test
// cleanup. Native Wails and other non-Chrome test executables are left alone.
func ConfigureBrowserStorage(t testing.TB, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process != nil || len(cmd.Args) == 0 {
		return
	}
	name := strings.ToLower(filepath.Base(cmd.Path))
	if !strings.Contains(name, "chrome") && !strings.Contains(name, "chromium") {
		return
	}
	root := t.TempDir()
	profile, cache, scratch := filepath.Join(root, "profile"), filepath.Join(root, "cache"), filepath.Join(root, "tmp")
	if runtime.GOOS == "linux" {
		// Chrome adds com.google.Chrome.XXXXXX/SingletonSocket to TMPDIR.
		// Linux Unix socket paths are limited to 108 bytes, so neither a test
		// name nor an inherited (potentially long) TMPDIR can be used here.
		var err error
		scratch, err = os.MkdirTemp("/tmp", "ov-chrome-")
		if err != nil {
			t.Fatalf("create browser scratch directory: %v", err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(scratch); err != nil {
				t.Errorf("remove browser scratch directory: %v", err)
			}
		})
	}
	for _, dir := range []string{profile, cache, scratch} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("create browser storage: %v", err)
		}
	}
	// Replace existing storage flags, including the two-argument spelling, so
	// callers cannot accidentally retain a shared profile or cache.
	args := []string{cmd.Args[0], "--user-data-dir=" + profile, "--disk-cache-dir=" + cache}
	for i := 1; i < len(cmd.Args); i++ {
		arg := cmd.Args[i]
		if arg == "--user-data-dir" || arg == "--disk-cache-dir" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--user-data-dir=") || strings.HasPrefix(arg, "--disk-cache-dir=") {
			continue
		}
		args = append(args, arg)
	}
	cmd.Args = args
	env := cmd.Environ()
	filtered := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "TMPDIR") || strings.EqualFold(key, "TMP") || strings.EqualFold(key, "TEMP") || strings.EqualFold(key, "MAC_CHROMIUM_TMPDIR") {
			continue
		}
		filtered = append(filtered, entry)
	}
	// macOS Chromium uses MAC_CHROMIUM_TMPDIR rather than TMPDIR.
	// See Chromium base/files/file_util_apple.mm:GetTempDir.
	// Set only the child's environment; parallel tests must not mutate the parent.
	cmd.Env = append(filtered, "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch, "MAC_CHROMIUM_TMPDIR="+scratch)
}
