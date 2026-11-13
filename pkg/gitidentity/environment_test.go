package gitidentity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func identityContext(name string) context.Context {
	return testResolver(context.Background(), func(context.Context) (Identity, error) {
		return Identity{Name: name, Email: strings.ToLower(name) + "@example.com", Token: name + "-token", Host: "github.com"}, nil
	})
}
func gitRun(t *testing.T, dir string, ctx context.Context, args ...string) string {
	t.Helper()
	cmd := Command(ctx, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestConcurrentCommitIdentitiesDoNotChangeConfig(t *testing.T) {
	for _, name := range []string{"Alice", "Bob"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			ctx := identityContext(name)
			gitRun(t, dir, ctx, "init")
			gitRun(t, dir, context.Background(), "config", "user.name", "Server Owner")
			gitRun(t, dir, context.Background(), "config", "user.email", "server@example.com")
			before, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte(name), 0600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, dir, ctx, "add", ".")
			gitRun(t, dir, ctx, "commit", "-m", "test")
			got := gitRun(t, dir, ctx, "log", "-1", "--format=%an <%ae>|%cn <%ce>")
			want := name + " <" + strings.ToLower(name) + "@example.com>"
			if got != want+"|"+want {
				t.Fatalf("identity: %q", got)
			}
			after, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if string(before) != string(after) {
				t.Fatal("modified repository config")
			}
		})
	}
}
func TestRebasePreservesAuthorAndUsesActingCommitter(t *testing.T) {
	dir := t.TempDir()
	alice := identityContext("Alice")
	bob := identityContext("Bob")
	gitRun(t, dir, alice, "init", "-b", "main")
	gitRun(t, dir, alice, "commit", "--allow-empty", "-m", "base")
	gitRun(t, dir, alice, "checkout", "-b", "topic")
	os.WriteFile(filepath.Join(dir, "alice"), []byte("alice"), 0600)
	gitRun(t, dir, alice, "add", ".")
	gitRun(t, dir, alice, "commit", "-m", "alice")
	gitRun(t, dir, bob, "checkout", "main")
	os.WriteFile(filepath.Join(dir, "bob"), []byte("bob"), 0600)
	gitRun(t, dir, bob, "add", ".")
	gitRun(t, dir, bob, "commit", "-m", "bob")
	gitRun(t, dir, bob, "checkout", "topic")
	gitRun(t, dir, bob, "rebase", "main")
	if got := gitRun(t, dir, bob, "log", "-1", "--format=%an|%cn"); got != "Alice|Bob" {
		t.Fatalf("rebase identity %q", got)
	}
}
func TestEnvironmentDropsAmbientCredentialsAndScopesToken(t *testing.T) {
	env, err := Environment(identityContext("Alice"), []string{"PATH=" + os.Getenv("PATH"), "GH_TOKEN=server-secret", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=server-secret", "GIT_AUTHOR_EMAIL=server@example.com", "GIT_SSH_COMMAND=server-ssh"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range env {
		if strings.Contains(v, "server-secret") || strings.Contains(v, "server@example") || strings.Contains(v, "server-ssh") {
			t.Fatal("ambient credentials survived")
		}
	}
	cmd := exec.Command("git", "config", "--get-urlmatch", "http.extraHeader", "https://other.example/repo")
	cmd.Env = env
	out, _ := cmd.Output()
	if strings.Contains(string(out), "AUTHORIZATION") {
		t.Fatal("credential leaked to other host")
	}
	cmd = exec.Command("git", "config", "--get-urlmatch", "http.extraHeader", "https://github.com/org/repo")
	cmd.Env = env
	out, err = cmd.Output()
	if err != nil || !strings.Contains(string(out), "AUTHORIZATION") {
		t.Fatalf("missing scoped credential: %s %v", out, err)
	}
}

func TestAgentShellGitUsesIsolatedHome(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX shell integration")
	}
	ambient := t.TempDir()
	if err := os.WriteFile(filepath.Join(ambient, ".netrc"), []byte("machine github.com login server password private"), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := Environment(identityContext("Alice"), append(os.Environ(), "HOME="+ambient))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `git -c 'alias.check-home=!printf "%s" "$HOME"' check-home`)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell git: %v: %s", err, out)
	}
	home := string(out)
	if home == ambient || home == "" {
		t.Fatalf("Git inherited ambient home: %q", home)
	}
	if _, err := os.Stat(filepath.Join(home, ".netrc")); !os.IsNotExist(err) {
		t.Fatal("Git home exposes a netrc file")
	}
}
