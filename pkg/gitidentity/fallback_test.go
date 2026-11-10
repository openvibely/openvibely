package gitidentity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitIdentityFallback(t *testing.T) {
	for _, tc := range []struct{ name, appName, appEmail, repoName, repoEmail, envName, wantName, wantEmail string }{
		{name: "global", wantName: "Global User", wantEmail: "global@example.com"},
		{name: "repository", repoName: "Repo User", repoEmail: "repo@example.com", wantName: "Repo User", wantEmail: "repo@example.com"},
		{name: "environment", repoName: "Repo User", envName: "Environment User", wantName: "Environment User", wantEmail: "global@example.com"},
		{name: "app-name", appName: "App User", repoEmail: "repo@example.com", envName: "Environment User", wantName: "App User", wantEmail: "repo@example.com"},
		{name: "app-email", appEmail: "app@example.com", repoName: "Repo User", wantName: "Repo User", wantEmail: "app@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := filepath.Join(t.TempDir(), "gitconfig")
			if err := os.WriteFile(global, []byte("[user]\n name = Global User\n email = global@example.com\n[credential]\n helper = !exit 99\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
				t.Setenv(key, tc.envName)
				if tc.envName == "" {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, key := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL", "EMAIL"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			gitRun(t, dir, context.Background(), "init", "-b", "main")
			if tc.repoName != "" {
				gitRun(t, dir, context.Background(), "config", "user.name", tc.repoName)
			}
			if tc.repoEmail != "" {
				gitRun(t, dir, context.Background(), "config", "user.email", tc.repoEmail)
			}
			before, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{Name: tc.appName, Email: tc.appEmail}, nil })
			// -C must select the actual repository for local and conditional config.
			gitRun(t, t.TempDir(), ctx, "-C", dir, "commit", "--allow-empty", "-m", "fallback")
			want := tc.wantName + " <" + tc.wantEmail + ">"
			if got := gitRun(t, dir, ctx, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != want+"|"+want {
				t.Fatalf("identity: %q, want %q", got, want)
			}
			after, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if string(before) != string(after) {
				t.Fatal("modified repository config")
			}
			if got := gitRun(t, dir, ctx, "config", "--get-all", "credential.helper"); got != "" {
				t.Fatalf("ambient helper survived: %q", got)
			}
			gitRun(t, dir, ctx, "checkout", "-b", "topic")
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "topic")
			gitRun(t, dir, ctx, "checkout", "main")
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "main")
			gitRun(t, dir, ctx, "merge", "--no-edit", "topic")
		})
	}
}

func TestCommitIdentityPreservesGitConfigPrecedence(t *testing.T) {
	for _, source := range []string{"repository", "include", "environment-count", "environment-parameters"} {
		t.Run(source, func(t *testing.T) {
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			dir := t.TempDir()
			gitRun(t, dir, context.Background(), "init", "-b", "main")
			fields := [][2]string{{"user.name", "Base User"}, {"user.email", "base@example.com"}, {"author.name", "Actual Author"}, {"author.email", "author@example.com"}, {"committer.name", "Actual Committer"}, {"committer.email", "committer@example.com"}}
			prefix := []string{}
			switch source {
			case "repository":
				for _, field := range fields {
					gitRun(t, dir, context.Background(), "config", field[0], field[1])
				}
			case "include":
				include := filepath.Join(t.TempDir(), "identity's config")
				for _, field := range fields {
					gitRun(t, dir, context.Background(), "config", "--file", include, field[0], field[1])
				}
				prefix = []string{"-C", dir, "-c", "include.path=" + include}
			case "environment-count":
				t.Setenv("GIT_CONFIG_COUNT", "7")
				for i, field := range append(fields, [2]string{"credential.helper", "!exit 99"}) {
					t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), field[0])
					t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), field[1])
				}
			case "environment-parameters":
				t.Setenv("GIT_CONFIG_PARAMETERS", configParameters(append(fields, [2]string{"credential.helper", "!exit 99"})))
			}
			ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{}, nil })
			gitRun(t, dir, ctx, append(prefix, "commit", "--allow-empty", "-m", "identity")...)
			if got := gitRun(t, dir, ctx, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != "Actual Author <author@example.com>|Actual Committer <committer@example.com>" {
				t.Fatalf("incorrect role identities: %s", got)
			}
			if got := gitRun(t, dir, ctx, "config", "--get-all", "credential.helper"); got != "" {
				t.Fatalf("ambient helper survived: %q", got)
			}
		})
	}
}

func TestCommitIdentityGlobalOptionsSelectTargetRepository(t *testing.T) {
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_PAGER", "cat")
	base, target := t.TempDir(), t.TempDir()
	for _, repo := range []struct{ dir, name, email string }{{base, "Base User", "base@example.com"}, {target, "Target User", "target@example.com"}} {
		gitRun(t, repo.dir, context.Background(), "init", "-b", "main")
		gitRun(t, repo.dir, context.Background(), "config", "user.name", repo.name)
		gitRun(t, repo.dir, context.Background(), "config", "user.email", repo.email)
	}
	for _, option := range []string{"-P", "-p", "--no-pager", "--no-replace-objects", "--no-optional-locks"} {
		for _, appName := range []string{"", "App User"} {
			t.Run(option+"/"+appName, func(t *testing.T) {
				ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{Name: appName}, nil })
				gitRun(t, base, ctx, option, "-C", target, "commit", "--allow-empty", "-m", "target identity")
				name := "Target User"
				if appName != "" {
					name = appName
				}
				want := name + " <target@example.com>"
				if got := gitRun(t, target, context.Background(), "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != want+"|"+want {
					t.Fatalf("identity = %q, want %q", got, want+"|"+want)
				}
			})
		}
	}
}

func TestCommitIdentityEnvironmentConditionalInclude(t *testing.T) {
	for _, source := range []string{"count", "parameters"} {
		t.Run(source, func(t *testing.T) {
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			base, target := t.TempDir(), t.TempDir()
			for _, dir := range []string{base, target} {
				gitRun(t, dir, context.Background(), "init", "-b", "main")
				gitRun(t, dir, context.Background(), "config", "user.name", "Base User")
				gitRun(t, dir, context.Background(), "config", "user.email", "base@example.com")
			}
			include := filepath.Join(t.TempDir(), "identity.conf")
			if err := os.WriteFile(include, []byte("[user]\n name = Included User\n email = included@example.com\n[credential]\n helper = !exit 99\n[http]\n extraHeader = private-test-header\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Resolve symlinks so gitdir matches Git's canonical repository path on macOS.
			canonical, err := filepath.EvalSymlinks(target)
			if err != nil {
				t.Fatal(err)
			}
			conditional := filepath.Join(t.TempDir(), "conditional.conf")
			gitRun(t, base, context.Background(), "config", "--file", conditional, "includeIf.gitdir:"+filepath.ToSlash(canonical)+"/.git.path", filepath.ToSlash(include))
			if source == "count" {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "include.path")
				t.Setenv("GIT_CONFIG_VALUE_0", conditional)
			} else {
				t.Setenv("GIT_CONFIG_PARAMETERS", configParameters([][2]string{{"include.path", conditional}}))
			}
			ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{}, nil })
			for _, tc := range []struct{ dir, want string }{{target, "Included User <included@example.com>"}, {base, "Base User <base@example.com>"}} {
				gitRun(t, base, ctx, "-P", "-C", tc.dir, "commit", "--allow-empty", "-m", "conditional identity")
				if got := gitRun(t, tc.dir, ctx, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != tc.want+"|"+tc.want {
					t.Fatalf("identity = %q, want %q", got, tc.want)
				}
				if got := gitRun(t, tc.dir, ctx, "config", "--get-all", "credential.helper"); got != "" {
					t.Fatalf("included helper survived: %q", got)
				}
				if got := gitRun(t, tc.dir, ctx, "config", "--get-all", "http.extraHeader"); got != "" {
					t.Fatalf("included header survived: %q", got)
				}
			}
		})
	}
}

func TestCommitIdentityEmailEnvironmentFallback(t *testing.T) {
	for _, tc := range []struct{ name, repoEmail, appEmail, want string }{
		{name: "email-fallback", want: "environment@example.com"},
		{name: "repository-takes-priority", repoEmail: "repo@example.com", want: "repo@example.com"},
		{name: "app-takes-priority", repoEmail: "repo@example.com", appEmail: "app@example.com", want: "app@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("EMAIL", "environment@example.com")
			dir := t.TempDir()
			gitRun(t, dir, context.Background(), "init", "-b", "main")
			gitRun(t, dir, context.Background(), "config", "user.name", "Test User")
			if tc.repoEmail != "" {
				gitRun(t, dir, context.Background(), "config", "user.email", tc.repoEmail)
			}
			ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{Email: tc.appEmail}, nil })
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "EMAIL fallback")
			if got := gitRun(t, dir, ctx, "log", "-1", "--format=%ae|%ce"); got != tc.want+"|"+tc.want {
				t.Fatalf("emails = %q, want %q", got, tc.want+"|"+tc.want)
			}
		})
	}
}

func TestCommitIdentityBotFallback(t *testing.T) {
	for _, tc := range []struct{ name, appName, email, author, wantAuthor, wantCommitter string }{
		{name: "missing", wantAuthor: "OpenVibely Bot <bot@openvibely.ai>", wantCommitter: "OpenVibely Bot <bot@openvibely.ai>"},
		{name: "partial-app", appName: "App User", wantAuthor: "App User <bot@openvibely.ai>", wantCommitter: "App User <bot@openvibely.ai>"},
		{name: "environment-email", email: "environment@example.com", wantAuthor: "OpenVibely Bot <environment@example.com>", wantCommitter: "OpenVibely Bot <environment@example.com>"},
		{name: "author-only", author: "Existing Author", wantAuthor: "Existing Author <bot@openvibely.ai>", wantCommitter: "OpenVibely Bot <bot@openvibely.ai>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			if tc.email != "" {
				t.Setenv("EMAIL", tc.email)
			}
			dir := t.TempDir()
			gitRun(t, dir, context.Background(), "init", "-b", "main")
			// Explicit config-only mode still permits our deliberate bot defaults.
			gitRun(t, dir, context.Background(), "config", "user.useConfigOnly", "true")
			if tc.author != "" {
				gitRun(t, dir, context.Background(), "config", "author.name", tc.author)
			}
			before, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := WithResolver(t.Context(), func(context.Context) (Identity, error) { return Identity{Name: tc.appName}, nil })
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "base")
			gitRun(t, dir, ctx, "checkout", "-b", "topic")
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "topic")
			gitRun(t, dir, ctx, "checkout", "main")
			gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "main")
			gitRun(t, dir, ctx, "merge", "--no-edit", "topic")
			if got := gitRun(t, dir, ctx, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != tc.wantAuthor+"|"+tc.wantCommitter {
				t.Fatalf("identity = %q, want %q", got, tc.wantAuthor+"|"+tc.wantCommitter)
			}
			after, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("fallback modified repository config")
			}
		})
	}
}
