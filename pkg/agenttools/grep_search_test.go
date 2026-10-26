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

func TestGrepSearchPreservesLineBoundaries(t *testing.T) {
	cases := []struct {
		name, content, pattern, want string
	}{
		{name: "single line", content: "target", pattern: "target", want: "file.txt:1: target\n"},
		{name: "multi line", content: "before\ntarget\nafter", pattern: "target", want: "file.txt:2: target\n"},
		{name: "trailing newline", content: "target\n", pattern: "target", want: "file.txt:1: target\n"},
		{name: "no trailing newline", content: "before\ntarget", pattern: "target", want: "file.txt:2: target\n"},
		{name: "CRLF keeps carriage return", content: "first\r\ntarget\r\nlast", pattern: "target", want: "file.txt:2: target\r\n"},
		{name: "empty line includes trailing empty element", content: "\n", pattern: "^$", want: "file.txt:1: \nfile.txt:2: \n"},
		{name: "empty file is one empty element", content: "", pattern: "^$", want: "file.txt:1: \n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "file.txt"), tc.content)
			got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput(tc.pattern, "", ""))
			if err != nil {
				t.Fatalf("grep_search: %v", err)
			}
			if got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGrepSearchMissingPathReturnsPathError(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"missing-relative",
		filepath.Join(root, "missing-absolute"),
	} {
		t.Run(path, func(t *testing.T) {
			got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", path, ""))
			if err == nil {
				t.Fatalf("grep_search returned %q, want an error for missing path %q", got, path)
			}
			if !strings.Contains(err.Error(), filepath.Base(path)) {
				t.Fatalf("error %q does not identify requested path %q", err, path)
			}
			if strings.Contains(got, "No matches found") {
				t.Fatalf("missing path was reported as a successful search: %q", got)
			}
		})
	}
}

func TestGrepSearchEmptyDirectoryReturnsNoMatches(t *testing.T) {
	root := t.TempDir()
	emptyDir := filepath.Join(root, "empty")
	if err := os.Mkdir(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", "empty", ""))
	if err != nil {
		t.Fatalf("grep_search: %v", err)
	}
	if want := `No matches found for pattern "target"`; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGrepSearchUnreadableRootReturnsPathError(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0755)

	if _, err := os.ReadDir(root); err == nil {
		if err := os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		t.Skip("directory permissions do not prevent reads for this user")
	}

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", "", ""))
	if err == nil {
		t.Fatalf("grep_search returned %q, want an access error for %q", got, root)
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error %q does not identify requested path %q", err, root)
	}
	if strings.Contains(got, "No matches found") {
		t.Fatalf("unreadable path was reported as a successful search: %q", got)
	}
}

func TestGrepSearchUnreadableFileRootReturnsPathError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unreadable.txt")
	writeTestFile(t, path, "target\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)

	file, err := os.Open(path)
	if err == nil {
		file.Close()
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
		t.Skip("file permissions do not prevent reads for this user")
	}

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", path, ""))
	if err == nil {
		t.Fatalf("grep_search returned %q, want an access error for %q", got, path)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not identify requested path %q", err, path)
	}
	if strings.Contains(got, "No matches found") {
		t.Fatalf("unreadable file was reported as a successful search: %q", got)
	}
}

func TestGrepSearchIgnoresUnreadableDescendant(t *testing.T) {
	root := t.TempDir()
	unreadableDir := filepath.Join(root, "unreadable")
	writeTestFile(t, filepath.Join(unreadableDir, "match.txt"), "target\n")
	if err := os.Chmod(unreadableDir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unreadableDir, 0755)

	if _, err := os.ReadDir(unreadableDir); err == nil {
		if err := os.Chmod(unreadableDir, 0755); err != nil {
			t.Fatal(err)
		}
		t.Skip("directory permissions do not prevent reads for this user")
	}

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", "", ""))
	if err != nil {
		t.Fatalf("grep_search should skip unreadable descendants: %v", err)
	}
	if want := `No matches found for pattern "target"`; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGrepSearchSupportsLongLines(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 70*1024) + "target"
	writeTestFile(t, filepath.Join(root, "long.txt"), line)

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target$", "", ""))
	if err != nil {
		t.Fatalf("grep_search: %v", err)
	}
	want := "long.txt:1: " + strings.Repeat("x", 200) + "...\n"
	if got != want {
		t.Fatalf("long-line output = %q, want %q", got, want)
	}
}

func TestGrepSearchCapsAtFirst100Matches(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 120; i++ {
		writeTestFile(t, filepath.Join(root, fmt.Sprintf("match-%03d.txt", i)), "target\n")
	}

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("target", "", ""))
	if err != nil {
		t.Fatalf("grep_search: %v", err)
	}
	var want strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&want, "match-%03d.txt:1: target\n", i)
	}
	want.WriteString("\n... (showing first 100 matches)")
	if got != want.String() {
		t.Fatalf("output differs from first 100 matches and truncation note:\n got: %q\nwant: %q", got, want.String())
	}
}

func TestGrepSearchSkipsBinaryFiles(t *testing.T) {
	for _, tc := range []struct {
		name, fileName, content string
	}{
		{name: "binary extension", fileName: "image.png", content: "hit\n"},
		{name: "NUL byte", fileName: "nul.txt", content: "hit\x00\nhit\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, tc.fileName), tc.content)

			got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("hit", "", ""))
			if err != nil {
				t.Fatalf("grep_search: %v", err)
			}
			if want := `No matches found for pattern "hit"`; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestGrepSearchSkipsTextExtensionWithLateNUL(t *testing.T) {
	root := t.TempDir()
	content := "hit\n" + strings.Repeat("padding\n", 1100) + "payload\x00\n" + "hit\n"
	writeTestFile(t, filepath.Join(root, "late-nul.txt"), content)

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("hit", "", ""))
	if err != nil {
		t.Fatalf("grep_search: %v", err)
	}
	if want := `No matches found for pattern "hit"`; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGrepSearchSkipsTrailingNULWithoutReturningEarlierMatches(t *testing.T) {
	root := t.TempDir()
	content := "hit\n" + strings.Repeat("padding\n", 16*1024) + "hit\x00"
	writeTestFile(t, filepath.Join(root, "trailing-nul.txt"), content)

	got, err := agenttools.Execute(context.Background(), root, "grep_search", rawGrepInput("hit", "", ""))
	if err != nil {
		t.Fatalf("grep_search: %v", err)
	}
	if want := `No matches found for pattern "hit"`; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestGrepSearchPathIncludeAndSkippedEntries(t *testing.T) {
	root := t.TempDir()
	searchDir := filepath.Join(root, "search")
	writeTestFile(t, filepath.Join(searchDir, "allowed.go"), "hit\n")
	writeTestFile(t, filepath.Join(searchDir, "excluded.txt"), "hit\n")
	writeTestFile(t, filepath.Join(searchDir, "image.png"), "hit\n")
	writeTestFile(t, filepath.Join(searchDir, "nul.txt"), "hit\x00\nhit\n")
	writeTestFile(t, filepath.Join(searchDir, ".hidden", "hidden.go"), "hit\n")
	writeTestFile(t, filepath.Join(searchDir, "node_modules", "dependency.go"), "hit\n")
	writeTestFile(t, filepath.Join(searchDir, "vendor", "vendored.go"), "hit\n")

	for _, path := range []string{"search", searchDir} {
		t.Run(path, func(t *testing.T) {
			input, err := json.Marshal(struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Include string `json:"include"`
			}{Pattern: "hit", Path: path, Include: "*.go"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := agenttools.Execute(context.Background(), root, "grep_search", input)
			if err != nil {
				t.Fatalf("grep_search: %v", err)
			}
			if got != "search/allowed.go:1: hit\n" {
				t.Fatalf("output = %q, want only the included visible text file", got)
			}
		})
	}
}

func TestGrepSearchReturnsPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := agenttools.Execute(ctx, t.TempDir(), "grep_search", rawGrepInput("hit", "", ""))
	if err != context.Canceled {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
}

func TestGrepSearchStopsDuringWalkOnCancellation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a-first.txt"), "no match")
	writeTestFile(t, filepath.Join(root, "b-second.txt"), "target\n")

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelOnErrContext{Context: base, cancel: cancel, cancelAt: 5}
	_, err := agenttools.Execute(ctx, root, "grep_search", rawGrepInput("target", "", ""))
	if err != context.Canceled {
		t.Fatalf("error = %v, want %v", err, context.Canceled)
	}
	if ctx.errCalls < ctx.cancelAt {
		t.Fatalf("context checked %d times, want at least %d", ctx.errCalls, ctx.cancelAt)
	}
}

type cancelOnErrContext struct {
	context.Context
	cancel   context.CancelFunc
	cancelAt int
	errCalls int
}

func (c *cancelOnErrContext) Err() error {
	c.errCalls++
	if c.errCalls == c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

func rawGrepInput(pattern, path, include string) json.RawMessage {
	input, err := json.Marshal(struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path,omitempty"`
		Include string `json:"include,omitempty"`
	}{Pattern: pattern, Path: path, Include: include})
	if err != nil {
		panic(err)
	}
	return input
}
