package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureGitDiff_NonGitDir(t *testing.T) {

	tmpDir := t.TempDir()
	result := Capture(tmpDir)
	if result != "" {
		t.Errorf("expected empty diff for non-git dir, got %q", result)
	}
}

func TestCaptureGitDiff_GitRepoWithChanges(t *testing.T) {
	tmpDir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")

	if err := os.WriteFile(filepath.Join(tmpDir, "hello.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "hello.go")
	runGit("commit", "-m", "initial")

	result := Capture(tmpDir)

	if strings.Contains(result, "diff --git") {
		t.Errorf("expected no diff before changes, got %q", result)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "hello.go"), []byte("package main\n\nimport \"fmt\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result = Capture(tmpDir)
	if !strings.Contains(result, "diff --git") {
		t.Errorf("expected diff after changes, got %q", result)
	}
	if !strings.Contains(result, "+import \"fmt\"") {
		t.Errorf("expected diff to contain added import, got %q", result)
	}
}

func TestCaptureGitDiff_EmptyDir(t *testing.T) {
	result := Capture("")
	if result != "" {
		t.Errorf("expected empty result for empty workDir, got %q", result)
	}
}

func TestCaptureGitDiff_UntrackedFiles(t *testing.T) {
	tmpDir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	if err := os.WriteFile(filepath.Join(tmpDir, "initial.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "initial.go")
	runGit("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(tmpDir, "newfile.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result := Capture(tmpDir)
	if !strings.Contains(result, "newfile.go") {
		t.Errorf("expected untracked file in output, got %q", result)
	}

	if !strings.Contains(result, "diff --git a/newfile.go b/newfile.go") {
		t.Errorf("expected proper diff header for untracked file, got %q", result)
	}
	if !strings.Contains(result, "+++ b/newfile.go") {
		t.Errorf("expected +++ header for untracked file, got %q", result)
	}
	if !strings.Contains(result, "--- /dev/null") {
		t.Errorf("expected --- /dev/null for new file, got %q", result)
	}
	if !strings.Contains(result, "+package main") {
		t.Errorf("expected file content as additions, got %q", result)
	}
}

func TestCaptureGitDiff_UntrackedFilesParseable(t *testing.T) {

	tmpDir := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	if err := os.WriteFile(filepath.Join(tmpDir, "initial.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "initial.go")
	runGit("commit", "-m", "initial")

	if err := os.WriteFile(filepath.Join(tmpDir, "newfile.txt"), []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "docs", "test.txt"), []byte("doc content\nline 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "initial.go"), []byte("package main\n\nfunc init() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result := Capture(tmpDir)

	if !strings.Contains(result, "initial.go") {
		t.Errorf("expected modified file in diff output")
	}

	if !strings.Contains(result, "diff --git a/newfile.txt b/newfile.txt") {
		t.Errorf("expected diff header for newfile.txt, got:\n%s", result)
	}
	if !strings.Contains(result, "diff --git a/docs/test.txt b/docs/test.txt") {
		t.Errorf("expected diff header for docs/test.txt, got:\n%s", result)
	}

	if !strings.Contains(result, "+hello world") {
		t.Errorf("expected newfile.txt content as additions")
	}
	if !strings.Contains(result, "+doc content") {
		t.Errorf("expected docs/test.txt content as additions")
	}
}
