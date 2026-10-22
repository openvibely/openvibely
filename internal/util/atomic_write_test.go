package util_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/util"
)

func TestWriteFileAtomicAppliesRequestedModeToNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	want := []byte("new contents")

	if err := util.WriteFileAtomic(path, want, 0o640); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	assertAtomicWriteContentsAndMode(t, path, want, 0o640)
	assertNoAtomicWriteTemps(t, dir)
}

func TestWriteFileAtomicPreservesExistingRegularFileMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o755} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "existing")
			if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
				t.Fatalf("seed file: %v", err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatalf("set existing mode: %v", err)
			}

			want := []byte("replacement contents")
			if err := util.WriteFileAtomic(path, want, 0o600); err != nil {
				t.Fatalf("WriteFileAtomic: %v", err)
			}
			assertAtomicWriteContentsAndMode(t, path, want, mode)
			assertNoAtomicWriteTemps(t, dir)
		})
	}
}

func TestWriteFileAtomicCleansTemporaryFileAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target-directory")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}

	if err := util.WriteFileAtomic(target, []byte("contents"), 0o644); err == nil {
		t.Fatal("WriteFileAtomic succeeded when replacing a directory")
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("target directory changed: info=%v err=%v", info, err)
	}
	assertNoAtomicWriteTemps(t, dir)
}

func assertAtomicWriteContentsAndMode(t *testing.T, path string, want []byte, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replaced file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat replaced file: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != mode {
		t.Fatalf("file mode = %04o, want %04o", gotMode, mode)
	}
}

func assertNoAtomicWriteTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".atomic-write-") {
			t.Errorf("temporary file remains: %s", entry.Name())
		}
	}
}
