package agenttools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/agenttools"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

type providerExecutor struct {
	name string
	exec func(context.Context, string, string, json.RawMessage) (string, error)
}

var providerExecutors = []providerExecutor{
	{name: "openai", exec: openaiclient.ExecuteTool},
	{name: "anthropic", exec: anthropicclient.ExecuteTool},
}

func TestProviderWrappersShareLocalToolBehavior(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		input  json.RawMessage
		setup  func(t *testing.T, dir string)
		verify func(t *testing.T, dir string)
	}{
		{
			name:  "read_file line output",
			tool:  "read_file",
			input: json.RawMessage(`{"file_path":"file.txt","offset":1,"limit":2}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "file.txt"), "one\ntwo\nthree\nfour\n")
			},
		},
		{
			name:  "write_file creates parents",
			tool:  "write_file",
			input: json.RawMessage(`{"file_path":"nested/out.txt","content":"alpha\nbeta"}`),
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "nested", "out.txt"))
				if err != nil {
					t.Fatalf("read written file: %v", err)
				}
				if string(data) != "alpha\nbeta" {
					t.Fatalf("written content = %q", string(data))
				}
			},
		},
		{
			name:  "edit_file exact replacement",
			tool:  "edit_file",
			input: json.RawMessage(`{"file_path":"edit.txt","old_string":"target","new_string":"changed"}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "edit.txt"), "before\ntarget\nafter\n")
			},
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "edit.txt"))
				if err != nil {
					t.Fatalf("read edited file: %v", err)
				}
				if !strings.Contains(string(data), "changed") || strings.Contains(string(data), "target") {
					t.Fatalf("edit content = %q", string(data))
				}
			},
		},
		{
			name:  "edit_file duplicate rejection",
			tool:  "edit_file",
			input: json.RawMessage(`{"file_path":"dup.txt","old_string":"same","new_string":"changed"}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "dup.txt"), "same\nsame\n")
			},
		},
		{
			name:  "edit_file replace all",
			tool:  "edit_file",
			input: json.RawMessage(`{"file_path":"all.txt","old_string":"same","new_string":"changed","replace_all":true}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "all.txt"), "same\nsame\n")
			},
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "all.txt"))
				if err != nil {
					t.Fatalf("read replace-all file: %v", err)
				}
				if strings.Count(string(data), "changed") != 2 || strings.Contains(string(data), "same") {
					t.Fatalf("replace-all content = %q", string(data))
				}
			},
		},
		{
			name:  "edit_file tolerant unicode whitespace fallback",
			tool:  "edit_file",
			input: json.RawMessage(`{"file_path":"unicode.txt","old_string":"alpha - \"quote\"\nsecond line\n","new_string":"replacement\n"}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "unicode.txt"), "alpha — “quote”\nsecond line\n")
			},
			verify: func(t *testing.T, dir string) {
				data, err := os.ReadFile(filepath.Join(dir, "unicode.txt"))
				if err != nil {
					t.Fatalf("read unicode edit file: %v", err)
				}
				if string(data) != "replacement\n" {
					t.Fatalf("unicode fallback content = %q", string(data))
				}
			},
		},
		{
			name:  "list_files recursive caps and skips",
			tool:  "list_files",
			input: json.RawMessage(`{"recursive":true,"pattern":"*.go"}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n")
				writeTestFile(t, filepath.Join(dir, "note.txt"), "note\n")
				writeTestFile(t, filepath.Join(dir, ".hidden", "hidden.go"), "package hidden\n")
				writeTestFile(t, filepath.Join(dir, "node_modules", "dep.go"), "package dep\n")
				writeTestFile(t, filepath.Join(dir, "vendor", "vend.go"), "package vend\n")
				writeTestFile(t, filepath.Join(dir, "__pycache__", "cache.go"), "package cache\n")
				writeTestFile(t, filepath.Join(dir, "deep", "a", "b", "c", "d", "too_deep.go"), "package deep\n")
			},
		},
		{
			name:  "grep_search include binary skip and truncation",
			tool:  "grep_search",
			input: json.RawMessage(`{"pattern":"target","include":"*.go"}`),
			setup: func(t *testing.T, dir string) {
				writeTestFile(t, filepath.Join(dir, "main.go"), "package main\n// target "+strings.Repeat("x", 220)+"\n")
				writeTestFile(t, filepath.Join(dir, "note.txt"), "target in text\n")
				writeTestFile(t, filepath.Join(dir, "image.png"), "target in binary extension\n")
				writeTestFile(t, filepath.Join(dir, ".hidden", "hidden.go"), "target hidden\n")
				writeTestFile(t, filepath.Join(dir, "vendor", "vend.go"), "target vend\n")
			},
		},
		{
			name:  "grep_search invalid regex",
			tool:  "grep_search",
			input: json.RawMessage(`{"pattern":"[invalid"}`),
		},
		{
			name:  "unknown tool",
			tool:  "missing_tool",
			input: json.RawMessage(`{}`),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var outputs []string
			var errs []string
			for _, provider := range providerExecutors {
				dir := t.TempDir()
				if tt.setup != nil {
					tt.setup(t, dir)
				}
				out, err := provider.exec(context.Background(), dir, tt.tool, tt.input)
				outputs = append(outputs, out)
				if err != nil {
					errs = append(errs, err.Error())
				} else {
					errs = append(errs, "")
				}
				if tt.verify != nil && err == nil {
					tt.verify(t, dir)
				}
			}
			if outputs[0] != outputs[1] || errs[0] != errs[1] {
				t.Fatalf("provider wrapper mismatch\nopenai output: %q\nopenai error: %q\nanthropic output: %q\nanthropic error: %q", outputs[0], errs[0], outputs[1], errs[1])
			}
		})
	}
}

func TestNormalizeBashTimeoutPolicies(t *testing.T) {
	openAI := agenttools.BashPolicy{DefaultTimeoutSeconds: 120, MaxTimeoutSeconds: 600}
	if got := agenttools.NormalizeBashTimeout(0, openAI); got != 120 {
		t.Fatalf("OpenAI omitted timeout = %d, want 120", got)
	}
	if got := agenttools.NormalizeBashTimeout(999, openAI); got != 600 {
		t.Fatalf("OpenAI capped timeout = %d, want 600", got)
	}

	anthropic := agenttools.BashPolicy{DefaultTimeoutSeconds: 600}
	if got := agenttools.NormalizeBashTimeout(0, anthropic); got != 600 {
		t.Fatalf("Anthropic omitted timeout = %d, want 600", got)
	}
	if got := agenttools.NormalizeBashTimeout(999, anthropic); got != 999 {
		t.Fatalf("Anthropic positive timeout = %d, want 999", got)
	}
}

func TestProviderWrappersDelegateLocalToolExecution(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "openai_client", "tools.go"),
		filepath.Join("..", "anthropic_client", "tools.go"),
	} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			for _, forbidden := range []string{"func execReadFile", "func execWriteFile", "func execEditFile", "func execBash", "func execListFiles", "func execGrepSearch"} {
				if strings.Contains(source, forbidden) {
					t.Fatalf("%s still owns provider-local execution body %q", path, forbidden)
				}
			}
			if !strings.Contains(source, "agenttools.Execute") {
				t.Fatalf("%s does not delegate execution to agenttools.Execute", path)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directories: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
