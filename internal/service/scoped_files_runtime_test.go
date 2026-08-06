package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestScopedFilesRuntimeRejectsEscapesAndHonorsPermissions(t *testing.T) {
	repo := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read"}}},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}
	if rt == nil {
		t.Fatal("expected runtime tools")
	}

	if err := os.WriteFile(filepath.Join(repo, "docs", "guide.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	readInput, _ := json.Marshal(map[string]string{"file_path": "guide.md"})
	out, handled, isErr, err := rt.Executor(context.Background(), "read_file", readInput)
	if err != nil || isErr || !handled || !strings.Contains(out, "hello") {
		t.Fatalf("read failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "guide.md", "content": "updated"})
	_, handled, isErr, err = rt.Executor(context.Background(), "write_file", writeInput)
	if !handled || !isErr || err == nil {
		t.Fatalf("expected write permission error handled=%v isErr=%v err=%v", handled, isErr, err)
	}

	escapeInput, _ := json.Marshal(map[string]string{"file_path": "../outside.md"})
	_, handled, isErr, err = rt.Executor(context.Background(), "read_file", escapeInput)
	if !handled || !isErr || err == nil {
		t.Fatalf("expected escape error handled=%v isErr=%v err=%v", handled, isErr, err)
	}
}

func TestScopedFilesRuntimeReadFileUsesCompactLinePrefixes(t *testing.T) {
	repo := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read"}}},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "lines.txt"), []byte("  first\nsecond\nthird\nfourth\nfifth\nsixth\nseventh\neighth\nninth\nlast\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, handled, isErr, err := rt.Executor(context.Background(), "read_file", json.RawMessage(`{"file_path":"lines.txt","limit":10}`))
	if err != nil || isErr || !handled {
		t.Fatalf("read failed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if !strings.HasPrefix(out, "1\t  first\n") || !strings.Contains(out, "10\tlast\n") {
		t.Fatalf("compact single/double-digit line prefixes or source indentation lost: %q", out)
	}
	if strings.Contains(out, "     1\t") || strings.Contains(out, "    10\t") {
		t.Fatalf("read output retains fixed-width line-number padding: %q", out)
	}

	if err := os.WriteFile(filepath.Join(repo, "docs", "wide.txt"), []byte(strings.Repeat("\n", 99999)+"  wide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "read_file", json.RawMessage(`{"file_path":"wide.txt","offset":99999,"limit":1}`))
	if err != nil || isErr || !handled {
		t.Fatalf("wide read failed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if !strings.HasPrefix(out, "100000\t  wide\n") {
		t.Fatalf("wide line prefix or source indentation changed: %q", out)
	}
}

func TestScopedFilesRuntimeRejectsAbsoluteConfiguredDirectory(t *testing.T) {
	_, _, err := buildScopedFilesRuntimeTools(context.Background(), "p1", t.TempDir(), models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{{Directory: filepath.Join(string(filepath.Separator), "tmp"), Permissions: []string{"read"}}},
	})
	if err == nil {
		t.Fatal("expected absolute directory to be rejected")
	}
}

func TestScopedFilesRuntimeGrepSkipsSymlinkEscapes(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("top-secret-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read"}}},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(repo, "docs", "secret.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	grepInput, _ := json.Marshal(map[string]string{"pattern": "top-secret-token"})
	out, handled, isErr, err := rt.Executor(context.Background(), "grep_search", grepInput)
	if err != nil || isErr || !handled {
		t.Fatalf("grep failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if strings.Contains(out, "top-secret-token") || strings.Contains(out, "secret.md") {
		t.Fatalf("grep leaked symlink target: %q", out)
	}
}
