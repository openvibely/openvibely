//go:build windows

package gitidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsWrapperRejectsAliases(t *testing.T) {
	rt, err := Prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Dir(rt.path)
	script := filepath.Join(bin, "git-wrapper.ps1")
	installed, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	boundary := strings.Index(string(installed), windowsIdentityFallbackScript)
	if boundary < 0 {
		t.Fatal("missing identity fallback boundary")
	}
	// Exercise the installed guard, but stop before identity lookups or delegation.
	// Even a missing/broken guard can only return success, never start recursion.
	probe := filepath.Join(bin, "check-guard.ps1")
	if err := os.WriteFile(probe, []byte(string(installed[:boundary])+"\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, target string, reject bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", probe)
		cmd.WaitDelay = time.Second
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "OV_GIT_REAL_EXECUTABLE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "OV_GIT_REAL_EXECUTABLE="+target)
		out, err := cmd.CombinedOutput()
		if !reject {
			if err != nil {
				t.Fatalf("non-wrapper rejected: %v: %s", err, out)
			}
			return
		}
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 125 || !strings.Contains(string(out), "cannot use itself") {
			t.Fatalf("alias accepted or guard failed: %v: %s", err, out)
		}
	}
	for _, name := range []string{"git.cmd", "git-wrapper.ps1"} {
		t.Run(name, func(t *testing.T) {
			original := filepath.Join(bin, name)
			run(t, original, true)
			link := filepath.Join(bin, "hardlink-"+name)
			if err := os.Link(original, link); err != nil {
				t.Fatal(err)
			}
			run(t, link, true)
			data, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			copy := filepath.Join(bin, "copy-"+name)
			if err := os.WriteFile(copy, data, 0600); err != nil {
				t.Fatal(err)
			}
			run(t, copy, true)
			// Matching length alone must not reject an unrelated executable.
			other := filepath.Join(bin, "other-"+name)
			if err := os.WriteFile(other, []byte(strings.Repeat("x", len(data))), 0600); err != nil {
				t.Fatal(err)
			}
			run(t, other, false)
			t.Run("symlink", func(t *testing.T) {
				alias := filepath.Join(bin, "symlink-"+name)
				if err := os.Symlink(original, alias); err != nil {
					t.Skipf("symlink permission unavailable: %v", err)
				}
				run(t, alias, true)
			})
		})
	}
	real, err := realGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	run(t, real, false)
}
