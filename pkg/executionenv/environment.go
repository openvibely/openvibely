// Package executionenv describes the local environment used by coding tools.
package executionenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type Shell struct{ Name, Path string }

// Resolve caches one selection so descriptions, context, and execution agree.
var resolve = sync.OnceValues(func() (Shell, error) {
	return detect(runtime.GOOS, os.Getenv("SHELL"), exec.LookPath, os.Getenv)
})

func Resolve() (Shell, error) { return resolve() }

func detect(goos, preferred string, lookup func(string) (string, error), getenv func(string) string) (Shell, error) {
	type candidate struct{ name, path string }
	var choices []candidate
	if goos == "windows" {
		root := getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		programFiles := getenv("ProgramFiles")
		if programFiles == "" {
			programFiles = `C:\Program Files`
		}
		choices = []candidate{{"powershell", "pwsh"}, {"powershell", filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")}, {"powershell", "powershell"}, {"powershell", filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")}, {"cmd", "cmd"}, {"cmd", filepath.Join(root, "System32", "cmd.exe")}}
	} else {
		name := filepath.Base(preferred)
		if name == "bash" || name == "zsh" || name == "sh" {
			choices = append(choices, candidate{name, preferred})
		}
		names := []string{"bash", "zsh", "sh"}
		if goos == "darwin" {
			names = []string{"zsh", "bash", "sh"}
		}
		for _, name := range names {
			for _, path := range []string{name, "/bin/" + name, "/usr/bin/" + name} {
				choices = append(choices, candidate{name, path})
			}
		}
	}
	for _, c := range choices {
		if p, err := lookup(c.path); err == nil {
			return Shell{c.name, p}, nil
		}
	}
	return Shell{}, fmt.Errorf("no supported shell installed for %s (install Bash, Zsh or sh on Unix; PowerShell or CMD on Windows)", goos)
}

func (s Shell) Args(command string) []string {
	switch s.Name {
	case "powershell":
		// Keep stdin prompts available to interact_process.
		return []string{"-NoProfile", "-Command", command}
	case "cmd":
		return []string{"/d", "/s", "/c", command}
	default:
		return []string{"-c", command}
	}
}

// DisplayName identifies the shell dialect without exposing its executable path.
func (s Shell) DisplayName() string {
	switch s.Name {
	case "bash":
		return "Bash"
	case "zsh":
		return "Zsh"
	case "sh":
		return "POSIX sh"
	case "powershell":
		name := filepath.Base(strings.ReplaceAll(s.Path, `\`, "/"))
		if strings.EqualFold(name, "pwsh") || strings.EqualFold(name, "pwsh.exe") {
			return "PowerShell (pwsh)"
		}
		return "Windows PowerShell"
	case "cmd":
		return "CMD"
	default:
		return s.Name
	}
}

func (s Shell) Instruction() string {
	if s.Name == "sh" {
		return "Use POSIX sh syntax; Bash extensions are unavailable."
	}
	return "Use " + s.DisplayName() + " syntax."
}

const Marker = "<openvibely_execution_environment>"

func Context(workDir string) string {
	if workDir == "" {
		workDir = "."
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	shell, err := Resolve()
	info := "Shell: unavailable (" + fmt.Sprint(err) + ")"
	if err == nil {
		info = fmt.Sprintf("Shell: %s\n%s", shell.DisplayName(), shell.Instruction())
	}
	return fmt.Sprintf("%s\nOS: %s\n%s\nWorking directory: %s\n%s\n</openvibely_execution_environment>", Marker, runtime.GOOS, info, workDir, WorktreeContext(workDir))
}

func WorktreeContext(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	clean := filepath.Clean(workDir)
	if filepath.Base(filepath.Dir(clean)) != ".worktrees" || !strings.HasPrefix(filepath.Base(clean), "task_") {
		return ""
	}
	return "You are operating in an isolated git worktree at " + workDir + ". Treat this path as the repository root for this run. The app automatically commits changes in your task worktree when the task completes. You do not need to run git commit unless the user explicitly requests it."
}

// Ensure appends a context update only when the latest retained environment
// differs. Calling it at every model boundary also restores context after compaction.
func Ensure[T any](messages []T, workDir string, read func(T) (string, string), makeMessage func(string) T) []T {
	current := Context(workDir)
	found := false
	for i := len(messages) - 1; i >= 0; i-- {
		role, text := read(messages[i])
		if role == "user" && strings.HasPrefix(text, Marker+"\n") {
			found = true
			if text == current {
				return messages
			}
			break
		}
	}
	// Plain-text history replay can omit app-generated messages. Seed before
	// the first user request so reconstructing a conversation keeps its prefix
	// stable. If an environment was retained, append changes near the new turn.
	if !found {
		for i, message := range messages {
			role, text := read(message)
			if role == "user" && text != "" {
				out := make([]T, 0, len(messages)+1)
				out = append(out, messages[:i]...)
				out = append(out, makeMessage(current))
				return append(out, messages[i:]...)
			}
		}
	}
	// Keep an ordinary user request last. Never insert between an assistant
	// tool call and its structured tool results (which have no plain text here).
	at := len(messages)
	if at > 0 {
		role, text := read(messages[at-1])
		if role == "user" && text != "" && !strings.HasPrefix(text, Marker+"\n") {
			at--
		}
	}
	out := make([]T, 0, len(messages)+1)
	out = append(out, messages[:at]...)
	out = append(out, makeMessage(current))
	return append(out, messages[at:]...)
}
