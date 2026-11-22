package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupExecutableOwnership(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "test", "openvibely")
	unrelated := filepath.Join(root, "real", "openvibely")
	for _, path := range []string{owned, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !sameCleanupExecutable(owned, owned) {
		t.Fatal("owned executable rejected")
	}
	if sameCleanupExecutable(unrelated, owned) {
		t.Fatal("same basename must not authorize cleanup")
	}
	if sameCleanupExecutable("openvibely", owned) {
		t.Fatal("relative executable must not authorize cleanup")
	}
	if sameCleanupExecutable(filepath.Join(root, "missing"), owned) {
		t.Fatal("missing executable must fail closed")
	}
}
