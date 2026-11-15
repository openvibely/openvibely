// Package tooltest runs the same behavioral checks against each tool backend.
package tooltest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type Execute func(context.Context, string, json.RawMessage) (string, error)

func call(t *testing.T, execute Execute, name string, input any) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out, err := execute(ctx, name, data)
	if err != nil {
		t.Fatalf("%s: %v (%s)", name, err, out)
	}
	return out
}
func Files(t *testing.T, execute Execute) {
	t.Helper()
	call(t, execute, "write_file", map[string]any{"file_path": "nested/deep/sample.go", "content": "  hello\nworld\n"})
	call(t, execute, "edit_file", map[string]any{"file_path": "nested/deep/sample.go", "old_string": "hello\n world\n", "new_string": "changed\n"})
	if out := call(t, execute, "read_file", map[string]any{"file_path": "nested/deep/sample.go"}); out != "1\tchanged\n2\t\n" {
		t.Fatalf("read/edit: %q", out)
	}
	call(t, execute, "write_file", map[string]any{"file_path": "nested/deep/sample.txt", "content": "changed"})
	if out := call(t, execute, "list_files", map[string]any{"pattern": "*.go", "recursive": true}); out != "nested/\nnested/deep/\nnested/deep/sample.go\n" {
		t.Fatalf("listing: %q", out)
	}
	if out := call(t, execute, "grep_search", map[string]any{"pattern": "changed", "include": "*.go"}); out != "nested/deep/sample.go:1: changed\n" {
		t.Fatalf("search: %q", out)
	}
	call(t, execute, "write_file", map[string]any{"file_path": "large.txt", "content": strings.Repeat("line\n", 11000)})
	for _, tc := range []struct{ limit, want int }{{0, 5000}, {1500, 1500}, {20000, 10000}} {
		out := call(t, execute, "read_file", map[string]any{"file_path": "large.txt", "limit": tc.limit})
		if !strings.Contains(out, fmt.Sprintf("%d\tline\n", tc.want)) || strings.Contains(out, fmt.Sprintf("%d\tline\n", tc.want+1)) || !strings.Contains(out, "truncated") {
			t.Fatalf("line limit %d incorrect", tc.limit)
		}
	}
	if out := call(t, execute, "read_file", map[string]any{"file_path": "large.txt", "offset": 1499, "limit": 1}); !strings.HasPrefix(out, "1500\tline\n") {
		t.Fatalf("offset: %q", out)
	}
}
func sessionID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "session_id: ") {
			return strings.TrimPrefix(line, "session_id: ")
		}
	}
	t.Fatalf("missing session: %s", out)
	return ""
}
func Processes(t *testing.T, execute Execute) {
	t.Helper()
	out := call(t, execute, "shell", map[string]any{"command": "printf first; read -r value; printf 'received:%s' \"$value\"", "yield_time_ms": 250})
	id := sessionID(t, out)
	// A loaded runner may not schedule the child during the initial yield.
	// Poll for its handshake before sending input; this also verifies that the
	// output is consumed before the incremental-output assertion below.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out, "first") {
		if strings.Contains(out, "status: completed") || time.Now().After(deadline) {
			t.Fatalf("missing initial output: %s", out)
		}
		out += call(t, execute, "process", map[string]any{"session_id": id, "yield_time_ms": 250})
	}
	out = call(t, execute, "process", map[string]any{"session_id": id, "input": "hello\n", "close_stdin": true})
	deadline = time.Now().Add(10 * time.Second)
	for !strings.Contains(out, "status: completed") {
		if time.Now().After(deadline) {
			t.Fatalf("command did not complete: %s", out)
		}
		out += call(t, execute, "process", map[string]any{"session_id": id, "yield_time_ms": 250})
	}
	if !strings.Contains(out, "received:hello") || strings.Contains(out, "first") {
		t.Fatalf("stdin/incremental output: %s", out)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatal(out)
	}
	data, _ := json.Marshal(map[string]any{"session_id": id})
	if _, err := execute(t.Context(), "process", data); err == nil {
		t.Fatal("completed session was retained after final read")
	}
	out = call(t, execute, "shell", map[string]any{"command": "sleep 30", "yield_time_ms": 250})
	id = sessionID(t, out)
	out = call(t, execute, "process", map[string]any{"session_id": id, "terminate": true})
	if !strings.Contains(out, "status: completed") {
		t.Fatal(out)
	}
	out = call(t, execute, "shell", map[string]any{"command": "sleep 30", "timeout": 1, "yield_time_ms": 250})
	id = sessionID(t, out)
	out = call(t, execute, "process", map[string]any{"session_id": id})
	if !strings.Contains(out, "command timed out after 1s") {
		t.Fatalf("timeout: %s", out)
	}

}
