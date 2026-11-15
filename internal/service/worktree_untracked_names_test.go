package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureWorktreeUntracked_SpecialNames(t *testing.T) {
	dir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("config", "core.quotePath", "true")
	runGit("commit", "--allow-empty", "-m", "initial")

	for _, name := range []string{"résumé.txt", " leading.txt", "trailing.txt ", "tab\tfile.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("AUDIT_CONTENT\n"), 0644); err != nil {
			t.Fatal(err)
		}
		result := captureWorktreeUntracked(dir)
		if !strings.Contains(result, "+AUDIT_CONTENT") {
			t.Errorf("expected content of untracked file %q in diff, got %q", name, result)
		}
		if strings.Contains(name, "\t") {
			want := `diff --git "a/tab\tfile.txt" "b/tab\tfile.txt"`
			if !strings.Contains(result, want) {
				t.Errorf("expected escaped header %q in diff, got %q", want, result)
			}
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
