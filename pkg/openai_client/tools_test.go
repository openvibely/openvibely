package openaiclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultTools_AllPresent(t *testing.T) {
	tools := DefaultTools()
	expected := map[string]bool{
		"read_file":   false,
		"write_file":  false,
		"edit_file":   false,
		"bash":        false,
		"list_files":  false,
		"grep_search": false,
	}

	for _, tool := range tools {
		name := tool.Name
		if _, ok := expected[name]; !ok {
			t.Errorf("unexpected tool: %s", name)
		}
		expected[name] = true
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestDefaultTools_ValidJSON(t *testing.T) {
	for _, tool := range DefaultTools() {
		// Verify the parameters field is valid JSON
		var params map[string]any
		if err := json.Unmarshal(tool.Parameters, &params); err != nil {
			t.Errorf("tool %s: invalid parameters JSON: %v", tool.Name, err)
		}
		if params["type"] != "object" {
			t.Errorf("tool %s: parameters.type = %v, want object", tool.Name, params["type"])
		}
	}
}

func TestExecReadFile(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("basic", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "test.txt"}`)
		result, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "line1") || !strings.Contains(result, "line5") {
			t.Errorf("expected all lines in result, got: %s", result)
		}
	})

	t.Run("with offset", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "test.txt", "offset": 2}`)
		result, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "line3") {
			t.Errorf("expected line3 after offset 2, got: %s", result)
		}
	})

	t.Run("with limit", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "test.txt", "limit": 2}`)
		result, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(result), "\n")
		if len(lines) > 2 {
			// Should be limited (may include truncation message)
			if !strings.Contains(result, "truncated") && len(lines) > 2 {
				t.Errorf("expected at most 2 lines, got %d", len(lines))
			}
		}
	})

	t.Run("missing file", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "nonexistent.txt"}`)
		_, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("missing file_path", func(t *testing.T) {
		input := json.RawMessage(`{}`)
		_, err := ExecuteTool(context.Background(), dir, "read_file", input)
		if err == nil {
			t.Error("expected error for missing file_path")
		}
	})
}

func TestExecWriteFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("basic", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "out.txt", "content": "hello world"}`)
		result, err := ExecuteTool(context.Background(), dir, "write_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Successfully wrote") {
			t.Errorf("unexpected result: %s", result)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "out.txt"))
		if string(data) != "hello world" {
			t.Errorf("file content = %q", string(data))
		}
	})

	t.Run("creates directories", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "sub/dir/file.txt", "content": "nested"}`)
		_, err := ExecuteTool(context.Background(), dir, "write_file", input)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "sub", "dir", "file.txt"))
		if string(data) != "nested" {
			t.Errorf("file content = %q", string(data))
		}
	})
}

func TestExecEditFile(t *testing.T) {
	dir := t.TempDir()
	content := "hello world\nfoo bar\nbaz qux\n"
	if err := os.WriteFile(filepath.Join(dir, "edit.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("single replacement", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "edit.txt", "old_string": "foo bar", "new_string": "FOO BAR"}`)
		result, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Successfully edited") {
			t.Errorf("unexpected result: %s", result)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
		if !strings.Contains(string(data), "FOO BAR") {
			t.Errorf("edit not applied: %s", string(data))
		}
	})

	t.Run("old_string not found", func(t *testing.T) {
		input := json.RawMessage(`{"file_path": "edit.txt", "old_string": "nonexistent", "new_string": "x"}`)
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Error("expected error for missing old_string")
		}
	})

	t.Run("whitespace tolerant multiline replacement", func(t *testing.T) {
		src := "if ready {\n\tfoo()\n\tbar()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "ws.txt"), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		input := json.RawMessage(`{"file_path":"ws.txt","old_string":"if ready {\n    foo()\n    bar()\n}\n","new_string":"if ready {\n\tfoo()\n\tbaz()\n}\n"}`)
		result, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "Successfully edited") {
			t.Errorf("unexpected result: %s", result)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "ws.txt"))
		if !strings.Contains(string(data), "baz()") {
			t.Errorf("expected tolerant replacement to apply: %s", string(data))
		}
	})

	t.Run("whitespace tolerant multiple matches without replace_all", func(t *testing.T) {
		src := "if ready {\n\tfoo()\n\tbar()\n}\n\nif ready {\n  foo()\n  bar()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "ws_dup.txt"), []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
		input := json.RawMessage(`{"file_path":"ws_dup.txt","old_string":"if ready {\n    foo()\n    bar()\n}\n","new_string":"if ready {\n  changed()\n}\n"}`)
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Fatal("expected duplicate-match error")
		}
		if !strings.Contains(err.Error(), "2 times") {
			t.Errorf("error = %q, expected duplicate count", err.Error())
		}
	})

	t.Run("multiple matches without replace_all", func(t *testing.T) {
		dup := "aaa\naaa\naaa\n"
		if err := os.WriteFile(filepath.Join(dir, "dup.txt"), []byte(dup), 0644); err != nil {
			t.Fatal(err)
		}
		input := json.RawMessage(`{"file_path": "dup.txt", "old_string": "aaa", "new_string": "bbb"}`)
		_, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err == nil {
			t.Error("expected error for multiple matches")
		}
		if !strings.Contains(err.Error(), "3 times") {
			t.Errorf("error = %q, expected to mention count", err.Error())
		}
	})

	t.Run("replace_all", func(t *testing.T) {
		dup := "aaa\naaa\naaa\n"
		if err := os.WriteFile(filepath.Join(dir, "dup2.txt"), []byte(dup), 0644); err != nil {
			t.Fatal(err)
		}
		input := json.RawMessage(`{"file_path": "dup2.txt", "old_string": "aaa", "new_string": "bbb", "replace_all": true}`)
		result, err := ExecuteTool(context.Background(), dir, "edit_file", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "3 occurrences") {
			t.Errorf("result = %q", result)
		}
	})
}

func TestExecBash(t *testing.T) {
	dir := t.TempDir()

	t.Run("echo", func(t *testing.T) {
		input := json.RawMessage(`{"command": "echo 'hello bash'"}`)
		result, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "hello bash") {
			t.Errorf("result = %q", result)
		}
	})

	t.Run("exit code", func(t *testing.T) {
		input := json.RawMessage(`{"command": "exit 42"}`)
		result, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "exit") {
			t.Errorf("expected exit code info: %s", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		input := json.RawMessage(`{"command": "sleep 30", "timeout": 1}`)
		_, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err == nil {
			t.Error("expected timeout error")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error = %q", err.Error())
		}
	})

	t.Run("working directory", func(t *testing.T) {
		input := json.RawMessage(`{"command": "pwd"}`)
		result, err := ExecuteTool(context.Background(), dir, "bash", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, dir) {
			t.Errorf("pwd = %q, expected to contain %q", result, dir)
		}
	})
}

func TestExecListFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("text"), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)

	t.Run("basic", func(t *testing.T) {
		input := json.RawMessage(`{}`)
		result, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "a.go") || !strings.Contains(result, "b.txt") || !strings.Contains(result, "subdir/") {
			t.Errorf("result = %q", result)
		}
	})

	t.Run("pattern filter", func(t *testing.T) {
		input := json.RawMessage(`{"pattern": "*.go"}`)
		result, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "a.go") {
			t.Errorf("result should contain a.go: %s", result)
		}
		if strings.Contains(result, "b.txt") {
			t.Errorf("result should not contain b.txt: %s", result)
		}
	})

	t.Run("recursive", func(t *testing.T) {
		os.WriteFile(filepath.Join(dir, "subdir", "c.go"), []byte("package c"), 0644)
		input := json.RawMessage(`{"recursive": true}`)
		result, err := ExecuteTool(context.Background(), dir, "list_files", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "subdir/") {
			t.Errorf("result should contain subdir/: %s", result)
		}
	})
}

func TestExecGrepSearch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\nfunc helper() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "lib.go"), []byte("package main\nfunc libFunc() {}\n"), 0644)

	t.Run("basic pattern", func(t *testing.T) {
		input := json.RawMessage(`{"pattern": "func "}`)
		result, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "main.go") || !strings.Contains(result, "func main") {
			t.Errorf("result = %q", result)
		}
	})

	t.Run("include filter", func(t *testing.T) {
		input := json.RawMessage(`{"pattern": "func ", "include": "lib.go"}`)
		result, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "lib.go") {
			t.Errorf("result should contain lib.go: %s", result)
		}
		if strings.Contains(result, "main.go") {
			t.Errorf("result should not contain main.go: %s", result)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		input := json.RawMessage(`{"pattern": "nonexistent_pattern_xyz"}`)
		result, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result, "No matches found") {
			t.Errorf("result = %q", result)
		}
	})

	t.Run("invalid regex", func(t *testing.T) {
		input := json.RawMessage(`{"pattern": "[invalid"}`)
		_, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err == nil {
			t.Error("expected error for invalid regex")
		}
	})

	t.Run("missing pattern", func(t *testing.T) {
		input := json.RawMessage(`{}`)
		_, err := ExecuteTool(context.Background(), dir, "grep_search", input)
		if err == nil {
			t.Error("expected error for missing pattern")
		}
	})
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	_, err := ExecuteTool(context.Background(), t.TempDir(), "unknown_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		workDir  string
		filePath string
		want     string
	}{
		{"/home/user", "file.txt", "/home/user/file.txt"},
		{"/home/user", "sub/dir/file.txt", "/home/user/sub/dir/file.txt"},
		{"/home/user", "/abs/path.txt", "/abs/path.txt"},
	}

	for _, tt := range tests {
		got := resolvePath(tt.workDir, tt.filePath)
		if got != tt.want {
			t.Errorf("resolvePath(%q, %q) = %q, want %q", tt.workDir, tt.filePath, got, tt.want)
		}
	}
}
