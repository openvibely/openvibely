package executionenv

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellSelection(t *testing.T) {
	for _, tc := range []struct{ name, os, preferred, installed, want string }{
		{"mac bash user", "darwin", "/custom/bash", "/custom/bash,zsh,bash", "bash"},
		{"mac default", "darwin", "/bin/fish", "zsh,bash", "zsh"},
		{"mac bash fallback", "darwin", "/missing/zsh", "bash,sh", "bash"},
		{"linux zsh user", "linux", "/custom/zsh", "/custom/zsh,bash", "zsh"},
		{"linux default", "linux", "/bin/fish", "bash,zsh,sh", "bash"},
		{"linux zsh fallback", "linux", "", "zsh,sh", "zsh"},
		{"unix sh only", "linux", "", "/bin/sh", "sh"},
		{"mac sh only", "darwin", "", "/bin/sh", "sh"},
		{"windows pwsh", "windows", "bash", "pwsh,powershell,cmd", "powershell"},
		{"windows legacy powershell", "windows", "bash", "powershell,cmd", "powershell"},
		{"windows cmd", "windows", "bash", "cmd", "cmd"},
		{"missing", "linux", "/bin/fish", "fish", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, error) {
				for _, p := range strings.Split(tc.installed, ",") {
					if name == p {
						return p, nil
					}
				}
				return "", fmt.Errorf("not found")
			}
			got, err := detect(tc.os, tc.preferred, lookup, func(string) string { return "" })
			if got.Name != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tc.name == "windows legacy powershell" && got.DisplayName() != "Windows PowerShell" {
				t.Fatalf("legacy shell mislabeled: %+v", got)
			}
			if tc.name == "windows pwsh" && (got.Path != "pwsh" || got.DisplayName() != "PowerShell (pwsh)") {
				t.Fatalf("did not prefer pwsh: %+v", got)
			}
		})
	}
}

func TestNativeShellExecution(t *testing.T) {
	shell, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	command := "printf 'shell-ok\\n'; pwd"
	switch shell.Name {
	case "powershell":
		command = "Write-Output 'shell-ok'; (Get-Location).Path"
	case "cmd":
		command = "echo shell-ok & cd"
	}
	cmd := exec.CommandContext(ctx, shell.Path, shell.Args(command)...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	// macOS may resolve /var to /private/var.
	if !strings.Contains(string(output), "shell-ok") || !strings.Contains(string(output), filepath.Base(dir)) {
		t.Fatalf("unexpected output: %s", output)
	}
	cmd = exec.CommandContext(ctx, shell.Path, shell.Args("exit 7")...)
	if err = cmd.Run(); err == nil {
		t.Fatal("exit status lost")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 {
		t.Fatalf("exit error: %v", err)
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	type message struct{ role, text string }
	read := func(m message) (string, string) { return m.role, m.text }
	makeMessage := func(s string) message { return message{"user", s} }
	firstDir := filepath.Join(t.TempDir(), ".worktrees", "task_123")
	history := Ensure([]message{{"assistant", "ready"}}, firstDir, read, makeMessage)
	if len(history) != 2 || !strings.Contains(history[1].text, "automatically commits") {
		t.Fatal(history)
	}
	history = Ensure(history, firstDir, read, makeMessage)
	if len(history) != 2 {
		t.Fatal("duplicated unchanged environment")
	}
	newDir := t.TempDir()
	history = Ensure(history, newDir, read, makeMessage)
	if len(history) != 3 || !strings.Contains(history[2].text, newDir) || strings.Contains(history[2].text, "automatically commits") {
		t.Fatal(history)
	}
	compacted := []message{{"assistant", "compacted summary"}}
	compacted = Ensure(compacted, newDir, read, makeMessage)
	if len(compacted) != 2 || compacted[1].text != history[2].text {
		t.Fatal("environment not restored after compaction")
	}
	shell, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compacted[1].text, "Shell: "+shell.DisplayName()) {
		t.Fatal("context/executor mismatch")
	}
	if strings.Contains(compacted[1].text, "Shell executable:") {
		t.Fatal("context includes executable path")
	}
}

func TestReconstructedHistoryKeepsEnvironmentPrefix(t *testing.T) {
	type message struct{ role, text string }
	read := func(m message) (string, string) { return m.role, m.text }
	makeMessage := func(text string) message { return message{"user", text} }
	dir := t.TempDir()
	first := Ensure([]message{{"system", "instructions"}, {"user", "first"}}, dir, read, makeMessage)
	second := Ensure([]message{{"system", "instructions"}, {"user", "first"}, {"assistant", "answer"}, {"user", "second"}}, dir, read, makeMessage)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("prefix changed: %#v -> %#v", first, second)
		}
	}
}
