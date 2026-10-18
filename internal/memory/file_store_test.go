package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *FileStore {
	t.Helper()
	base := t.TempDir()
	res, err := NewPathResolver("", "")
	if err != nil {
		t.Fatalf("NewPathResolver: %v", err)
	}
	if err := res.SetProjectDirOverride("p1", filepath.Join(base, "memory")); err != nil {
		t.Fatalf("SetProjectDirOverride: %v", err)
	}
	return NewFileStore(res)
}

func TestFileStore_EnsureProjectCreatesDirectoryAndIndexWhenMissing(t *testing.T) {
	s := newStore(t)
	dir, err := s.EnsureProject("p1")
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	idx := filepath.Join(dir, IndexFileName)
	data, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(data) != defaultIndexContents {
		t.Fatalf("unexpected default index contents:\nwant: %q\ngot:  %q", defaultIndexContents, string(data))
	}
}

func TestFileStore_EnsureProjectMigratesLegacyIndex(t *testing.T) {
	s := newStore(t)
	dir, err := s.Resolver().EnsureProjectDir("p1")
	if err != nil {
		t.Fatalf("EnsureProjectDir: %v", err)
	}
	legacy := filepath.Join(dir, LegacyIndexFileName)
	contents := "# legacy index body\nuser preferences\n"
	if err := os.WriteFile(legacy, []byte(contents), 0o644); err != nil {
		t.Fatalf("seed legacy index: %v", err)
	}
	if _, err := s.EnsureProject("p1"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy index should be migrated away, got err=%v", err)
	}
	migrated, err := os.ReadFile(filepath.Join(dir, IndexFileName))
	if err != nil {
		t.Fatalf("read migrated index: %v", err)
	}
	if string(migrated) != contents {
		t.Fatalf("migrated body changed:\nwant: %q\ngot:  %q", contents, string(migrated))
	}
}

func TestFileStore_EnsureProjectDoesNotRewriteExistingIndex(t *testing.T) {
	s := newStore(t)
	dir, err := s.EnsureProject("p1")
	if err != nil {
		t.Fatalf("EnsureProject (initial): %v", err)
	}
	idx := filepath.Join(dir, IndexFileName)
	custom := "# Hand-edited index\n- topic A\n"
	if err := os.WriteFile(idx, []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom index: %v", err)
	}
	if _, err := s.EnsureProject("p1"); err != nil {
		t.Fatalf("EnsureProject (idempotent): %v", err)
	}
	got, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(got) != custom {
		t.Fatalf("EnsureProject overwrote existing index:\nwant: %q\ngot:  %q", custom, string(got))
	}
}
