package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

// legacyReadFile is the previous fmt-per-line formatter; output must not change.
func legacyReadFile(data string, offset, limit int) string {
	lines := strings.Split(data, "\n")
	if offset > 0 {
		if offset >= len(lines) {
			return fmt.Sprintf("(file has %d lines, offset %d is past end)", len(lines), offset)
		}
		lines = lines[offset:]
	}
	truncated := false
	if len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}
	var sb strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&sb, "%d\t%s\n", offset+1+i, line)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n... (truncated, showing %d of %d lines from offset %d)", limit, len(strings.Split(data, "\n")), offset)
	}
	return sb.String()
}

func TestReadFileOutputMatchesLegacyFormat(t *testing.T) {
	contents := []string{"", "\n", "one", "one\n", "one\ntwo\nthree", "a\r\nb\r\n", "ünï\ncödé\n\n\n", strings.Repeat("line\n", 25)}
	params := [][2]int{{0, 0}, {0, 1}, {0, 3}, {1, 2}, {2, 0}, {3, 1}, {4, 0}, {30, 0}, {-1, 0}, {0, 20000}}
	dir := t.TempDir()
	for ci, content := range contents {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", ci))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, p := range params {
			input, _ := json.Marshal(map[string]any{"file_path": path, "offset": p[0], "limit": p[1]})
			got, err := agenttools.Execute(context.Background(), dir, "read_file", input)
			if err != nil {
				t.Fatal(err)
			}
			limit := p[1]
			if limit <= 0 {
				limit = 5000
			}
			if limit > 10000 {
				limit = 10000
			}
			if want := legacyReadFile(content, p[0], limit); got != want {
				t.Fatalf("content %q offset %d limit %d:\n got %q\nwant %q", content, p[0], p[1], got, want)
			}
		}
	}
}

func BenchmarkReadFile512KiB(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat(strings.Repeat("x", 63)+"\n", 8192)), 0o644); err != nil {
		b.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"file_path": path})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := agenttools.Execute(context.Background(), dir, "read_file", input); err != nil {
			b.Fatal(err)
		}
	}
}
