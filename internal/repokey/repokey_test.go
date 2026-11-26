package repokey

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/foo.git":      "github.com/acme/foo",
		"https://GitHub.com/acme/foo/":         "github.com/acme/foo",
		"http://user@github.com/acme/foo":      "github.com/acme/foo",
		"git@github.com:acme/foo.git":          "github.com/acme/foo",
		"ssh://git@GitHub.com:22/acme/foo.git": "github.com/acme/foo",
		"git@gitlab.example.com:group/sub/foo": "gitlab.example.com/group/sub/foo",
		"  https://github.com/acme/foo.git\n":  "github.com/acme/foo",
		"/Users/me/src/foo":                    "",
		"file:///Users/me/src/foo":             "",
		"../foo":                               "",
		"":                                     "",
		"https://github.com/":                  "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForPrefersSavedURLThenUpstreamThenOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	if got := For("", dir); got != "" {
		t.Fatalf("no remotes = %q", got)
	}
	run("remote", "add", "origin", "git@github.com:me/foo.git")
	if got := For("", dir); got != "github.com/me/foo" {
		t.Fatalf("origin = %q", got)
	}
	run("remote", "add", "upstream", "https://github.com/acme/foo.git")
	if got := For("", dir); got != "github.com/acme/foo" {
		t.Fatalf("upstream = %q", got)
	}
	if got := For("https://github.com/other/bar", dir); got != "github.com/other/bar" {
		t.Fatalf("saved URL = %q", got)
	}
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	run("worktree", "add", "-q", wt)
	if got := For("", wt); got != "github.com/acme/foo" {
		t.Fatalf("worktree = %q", got)
	}
	if got := For("", filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing = %q", got)
	}
	_ = os.RemoveAll(wt)
}

func TestIdentityUsesGitConfigSyntax(t *testing.T) {
	for _, mode := range []string{"quoted", "include", "invalid", "no-remote"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			config := "[remote \"origin\"]\n url = \"https://github.com/team/repo.git\"\n"
			switch mode {
			case "include":
				if err := os.WriteFile(filepath.Join(dir, ".git", "remotes.conf"), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
				config = "[include]\n path = remotes.conf\n"
			case "invalid":
				config = "[broken\n"
			case "no-remote":
				config = "[core]\n bare = false\n"
			}
			if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := resolveIdentity("", dir)
			if mode == "invalid" {
				if err == nil {
					t.Fatal("invalid config must return an error for cache retry")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "github.com/team/repo"
			if mode == "no-remote" {
				want = ""
			}
			if got != want {
				t.Fatalf("identity = %q, want %q", got, want)
			}
		})
	}
}
