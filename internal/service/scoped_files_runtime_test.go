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

func TestScopedFilesRuntimeNestedExplicitScopeRoutesWriteAndRead(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs", "configs", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "configs", "secrets", "token.md"), []byte("wrong-scope"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{
			{Directory: "docs", Permissions: []string{"read_write"}},
			{Directory: "configs/secrets", Permissions: []string{"read_write"}},
		},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "configs/secrets/token.md", "content": "nested-secret"})
	out, handled, isErr, err := rt.Executor(context.Background(), "write_file", writeInput)
	if err != nil || isErr || !handled {
		t.Fatalf("write failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if !strings.Contains(out, "to token.md") {
		t.Fatalf("write reported path outside nested scope root: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(repo, "configs", "secrets", "token.md"))
	if err != nil {
		t.Fatalf("read nested target: %v", err)
	}
	if string(data) != "nested-secret" {
		t.Fatalf("nested scope target content = %q", data)
	}
	data, err = os.ReadFile(filepath.Join(repo, "docs", "configs", "secrets", "token.md"))
	if err != nil {
		t.Fatalf("read fallback target: %v", err)
	}
	if string(data) != "wrong-scope" {
		t.Fatalf("write routed through fallback docs scope, content = %q", data)
	}

	if err := os.WriteFile(filepath.Join(repo, "configs", "secrets", "token.md"), []byte("authoritative"), 0o644); err != nil {
		t.Fatal(err)
	}
	readInput, _ := json.Marshal(map[string]string{"file_path": "configs/secrets/token.md"})
	out, handled, isErr, err = rt.Executor(context.Background(), "read_file", readInput)
	if err != nil || isErr || !handled {
		t.Fatalf("read failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if !strings.Contains(out, "authoritative") || strings.Contains(out, "wrong-scope") {
		t.Fatalf("read did not use nested scope root: %q", out)
	}
}

func TestScopedFilesRuntimeNestedExplicitScopeRoutesEditFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "docs", "configs", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "configs", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "configs", "secrets", "name.txt"), []byte("wrong old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "configs", "secrets", "name.txt"), []byte("right old"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{
			{Directory: "docs", Permissions: []string{"read_write"}},
			{Directory: "configs/secrets", Permissions: []string{"read_write"}},
		},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}

	editInput, _ := json.Marshal(map[string]any{"file_path": "configs/secrets/name.txt", "old_string": "right old", "new_string": "right new"})
	out, handled, isErr, err := rt.Executor(context.Background(), "edit_file", editInput)
	if err != nil || isErr || !handled {
		t.Fatalf("edit failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if !strings.Contains(out, "name.txt") || strings.Contains(out, "configs/secrets/name.txt") {
		t.Fatalf("edit reported path outside nested scope root: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(repo, "configs", "secrets", "name.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "right new" {
		t.Fatalf("nested file content = %q", data)
	}
	data, err = os.ReadFile(filepath.Join(repo, "docs", "configs", "secrets", "name.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "wrong old" {
		t.Fatalf("fallback file was edited: %q", data)
	}
}

func TestScopedFilesRuntimeParentAndChildScopesUseLongestExplicitPrefix(t *testing.T) {
	repo := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{
			{Directory: "configs", Permissions: []string{"read"}},
			{Directory: "configs/secrets", Permissions: []string{"read_write"}},
		},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "configs/secrets/token.md", "content": "nested"})
	out, handled, isErr, err := rt.Executor(context.Background(), "write_file", writeInput)
	if err != nil || isErr || !handled {
		t.Fatalf("write should choose nested scope over read-only parent handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if !strings.Contains(out, "to token.md") {
		t.Fatalf("write reported parent-relative path instead of nested-relative path: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(repo, "configs", "secrets", "token.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "nested" {
		t.Fatalf("nested file content = %q", data)
	}
}

func TestScopedFilesRuntimeExplicitNestedScopePermissionDeniedUsesSelectedScope(t *testing.T) {
	repo := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{
			{Directory: "configs", Permissions: []string{"read_write"}},
			{Directory: "configs/secrets", Permissions: []string{"read"}},
		},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "configs/secrets/token.md", "content": "blocked"})
	_, handled, isErr, err := rt.Executor(context.Background(), "write_file", writeInput)
	if !handled || !isErr || err == nil {
		t.Fatalf("expected selected nested scope write permission error handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if !strings.Contains(err.Error(), "configs/secrets") {
		t.Fatalf("permission error did not name nested scope: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "configs", "secrets", "token.md")); !os.IsNotExist(statErr) {
		t.Fatalf("write should not create file through writable parent, stat err=%v", statErr)
	}
}

func TestScopedFilesRuntimeUnprefixedPathUsesFirstPermittedScope(t *testing.T) {
	repo := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{
			{Directory: "docs", Permissions: []string{"read_write"}},
			{Directory: "configs/secrets", Permissions: []string{"read_write"}},
		},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "note.md", "content": "fallback"})
	_, handled, isErr, err := rt.Executor(context.Background(), "write_file", writeInput)
	if err != nil || isErr || !handled {
		t.Fatalf("fallback write failed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "docs", "note.md"))
	if err != nil {
		t.Fatalf("read first scope target: %v", err)
	}
	if string(data) != "fallback" {
		t.Fatalf("first scope target content = %q", data)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "configs", "secrets", "note.md")); !os.IsNotExist(statErr) {
		t.Fatalf("fallback should not use later nested scope, stat err=%v", statErr)
	}
}

func TestScopedFilesRuntimeExplicitNestedScopeRejectsSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	_, rt, err := buildScopedFilesRuntimeTools(context.Background(), "p1", repo, models.AgentToolConfig{
		ScopedFiles: []models.ScopedFilesConfig{{Directory: "configs/secrets", Permissions: []string{"read_write"}}},
	})
	if err != nil {
		t.Fatalf("build scoped runtime: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "configs", "secrets", "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	writeInput, _ := json.Marshal(map[string]string{"file_path": "configs/secrets/link/token.md", "content": "escaped"})
	_, handled, isErr, err := rt.Executor(context.Background(), "write_file", writeInput)
	if !handled || !isErr || err == nil {
		t.Fatalf("expected symlink escape error handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "token.md")); !os.IsNotExist(statErr) {
		t.Fatalf("write escaped through symlink, stat err=%v", statErr)
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
