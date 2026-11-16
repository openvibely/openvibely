package gitidentity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNestedEnvironmentUsesRealGit(t *testing.T) {
	real, err := realGitExecutable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(testRuntime.path)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OV_GIT_REAL_EXECUTABLE", real)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	rt, err := Prepare(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx = WithResolver(WithRuntime(ctx, rt), func(context.Context) (Identity, error) { return Identity{}, nil })
	env, err := Environment(ctx, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	// Check before executing: a regression must not unleash recursive shells.
	found := false
	for _, entry := range env {
		if strings.HasPrefix(entry, "OV_GIT_REAL_EXECUTABLE=") {
			if entry != "OV_GIT_REAL_EXECUTABLE="+real {
				t.Fatalf("wrong backend: %s", entry)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing real Git")
	}
	dir := t.TempDir()
	gitRun(t, dir, ctx, "init")
	gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "fallback")
	if got := gitRun(t, dir, ctx, "log", "-1", "--format=%an <%ae>"); got != "OpenVibely Bot <bot@openvibely.ai>" {
		t.Fatalf("identity: %s", got)
	}
}

func TestRealGitRejectsWrapperOnlyPath(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(testRuntime.path))
	if _, err := realGitExecutable(); err == nil {
		t.Fatal("accepted wrapper as real Git")
	}
}
