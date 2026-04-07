package anthropicclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecReadFile(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("basic read", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"file_path": "test.txt"})
		out, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "line1") || !strings.Contains(out, "line5") {
			t.Errorf("expected file contents, got: %s", out)
		}
	})

	t.Run("with offset", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"file_path": "test.txt", "offset": 2})
		out, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "     1\t") {
			t.Error("should not contain line 1")
		}
		if !strings.Contains(out, "line3") {
			t.Errorf("expected line3, got: %s", out)
		}
	})

	t.Run("with limit", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"file_path": "test.txt", "limit": 2})
		out, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "line1") {
			t.Error("expected line1")
		}
		if strings.Contains(out, "line3") {
			t.Error("should not contain line3 with limit=2")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"file_path": "nonexistent.txt"})
		_, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("absolute path", func(t *testing.T) {
		absPath := filepath.Join(dir, "test.txt")
		input, _ := json.Marshal(map[string]interface{}{"file_path": absPath})
		out, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "line1") {
			t.Error("expected line1 with absolute path")
		}
	})
}

func TestExecWriteFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("create new file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file_path": "new.txt",
			"content":   "hello world",
		})
		out, err := ExecuteTool(context.Background(), dir, "write_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Successfully wrote") {
			t.Errorf("unexpected output: %s", out)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
		if string(data) != "hello world" {
			t.Errorf("file content mismatch: %s", string(data))
		}
	})

	t.Run("create with subdirectories", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file_path": "sub/dir/file.txt",
			"content":   "nested",
		})
		_, err := ExecuteTool(context.Background(), dir, "write_file", input)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "sub/dir/file.txt"))
		if string(data) != "nested" {
			t.Errorf("expected 'nested', got: %s", string(data))
		}
	})

	t.Run("overwrite existing", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"file_path": "new.txt",
			"content":   "overwritten",
		})
		_, err := ExecuteTool(context.Background(), dir, "write_file", input)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
		if string(data) != "overwritten" {
			t.Errorf("expected 'overwritten', got: %s", string(data))
		}
	})
}

func TestExecEditFile(t *testing.T) {
	dir := t.TempDir()

	setup := func(content string) {
		os.WriteFile(filepath.Join(dir, "edit.txt"), []byte(content), 0644)
	}

	t.Run("basic replace", func(t *testing.T) {
		setup("hello world\nfoo bar\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":  "edit.txt",
			"old_string": "foo bar",
			"new_string": "baz qux",
		})
		out, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Successfully edited") {
			t.Errorf("unexpected output: %s", out)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
		if !strings.Contains(string(data), "baz qux") {
			t.Error("expected replacement")
		}
	})

	t.Run("not found", func(t *testing.T) {
		setup("hello world\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":  "edit.txt",
			"old_string": "nonexistent",
			"new_string": "replacement",
		})
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Error("expected error for string not found")
		}
	})

	t.Run("whitespace tolerant multiline replacement", func(t *testing.T) {
		setup("if ready {\n\tfoo()\n\tbar()\n}\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":  "edit.txt",
			"old_string": "if ready {\n    foo()\n    bar()\n}\n",
			"new_string": "if ready {\n\tfoo()\n\tbaz()\n}\n",
		})
		out, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Successfully edited") {
			t.Errorf("unexpected output: %s", out)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
		if !strings.Contains(string(data), "baz()") {
			t.Errorf("expected tolerant replacement to apply: %s", string(data))
		}
	})

	t.Run("whitespace tolerant multiple matches without replace_all", func(t *testing.T) {
		setup("if ready {\n\tfoo()\n\tbar()\n}\n\nif ready {\n  foo()\n  bar()\n}\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":  "edit.txt",
			"old_string": "if ready {\n    foo()\n    bar()\n}\n",
			"new_string": "if ready {\n  changed()\n}\n",
		})
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Fatal("expected duplicate-match error")
		}
		if !strings.Contains(err.Error(), "2 times") {
			t.Errorf("error = %q, expected duplicate count", err.Error())
		}
	})

	t.Run("multiple matches without replace_all", func(t *testing.T) {
		setup("aaa\naaa\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":  "edit.txt",
			"old_string": "aaa",
			"new_string": "bbb",
		})
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Error("expected error for multiple matches")
		}
	})

	t.Run("replace_all", func(t *testing.T) {
		setup("aaa\naaa\n")
		input, _ := json.Marshal(map[string]interface{}{
			"file_path":   "edit.txt",
			"old_string":  "aaa",
			"new_string":  "bbb",
			"replace_all": true,
		})
		out, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "2 occurrences") {
			t.Errorf("expected 2 occurrences message, got: %s", out)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
		if strings.Contains(string(data), "aaa") {
			t.Error("expected all aaa replaced")
		}
	})
}

func TestExecBash(t *testing.T) {
	dir := t.TempDir()

	t.Run("simple command", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"command": "echo hello"})
		out, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "hello") {
			t.Errorf("expected 'hello', got: %s", out)
		}
	})

	t.Run("working directory", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"command": "pwd"})
		out, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, dir) {
			t.Errorf("expected dir %s in output: %s", dir, out)
		}
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"command": "exit 1"})
		out, err := ExecuteTool(context.Background(), dir, "bash", input)
		// err should be nil - non-zero exit is not an execution error
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(out, "exit") {
			t.Errorf("expected exit code info, got: %s", out)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{
			"command": "sleep 10",
			"timeout": 1,
		})
		_, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err == nil {
			t.Error("expected timeout error")
		}
	})

	t.Run("strips CLAUDECODE env", func(t *testing.T) {
		t.Setenv("CLAUDECODE", "test-value")
		input, _ := json.Marshal(map[string]interface{}{"command": "echo $CLAUDECODE"})
		out, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		out = strings.TrimSpace(out)
		if out != "" {
			t.Errorf("CLAUDECODE should be stripped, got: %q", out)
		}
	})
}

func TestExecListFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte(""), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "subdir", "c.go"), []byte(""), 0644)

	t.Run("basic list", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{})
		out, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.txt") || !strings.Contains(out, "subdir/") {
			t.Errorf("missing entries: %s", out)
		}
	})

	t.Run("pattern filter", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"pattern": "*.go"})
		out, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "a.go") {
			t.Error("expected a.go")
		}
		if strings.Contains(out, "b.txt") {
			t.Error("should not contain b.txt")
		}
	})

	t.Run("recursive", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"recursive": true})
		out, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "c.go") {
			t.Errorf("expected subdir/c.go in recursive listing: %s", out)
		}
	})
}

func TestExecGrepSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "util.go"), []byte("package main\n\nfunc helper() string {\n\treturn \"world\"\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# README\nThis is a test.\n"), 0644)

	t.Run("basic search", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"pattern": "func.*\\("})
		out, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "main.go") || !strings.Contains(out, "util.go") {
			t.Errorf("expected matches in both files: %s", out)
		}
	})

	t.Run("include filter", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"pattern": "main", "include": "*.go"})
		out, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "readme.md") {
			t.Error("should not search .md files with include=*.go")
		}
	})

	t.Run("no matches", func(t *testing.T) {
		input, _ := json.Marshal(map[string]interface{}{"pattern": "nonexistent_pattern_xyz"})
		out, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "No matches") {
			t.Errorf("expected no matches message: %s", out)
		}
	})
}

func TestDefaultTools(t *testing.T) {
	tools := DefaultTools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
		// Verify input_schema is valid JSON
		var schema map[string]interface{}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("tool %s has invalid input_schema: %v", tool.Name, err)
		}
	}

	expected := []string{"read_file", "write_file", "edit_file", "bash", "list_files", "grep_search"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestUnknownTool(t *testing.T) {
	_, err := ExecuteTool(context.Background(), ".", "nonexistent_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}
