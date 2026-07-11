package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestResolveRepoUsesUpstreamWhenOriginMissing(t *testing.T) {
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	cmd := exec.Command("git", "remote", "add", "upstream", "git@github.com:openvibely/openvibely.git")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add upstream: %v\n%s", err, out)
	}

	svc := NewGitHubService(nil, "", "", "", "")
	got, err := svc.ResolveRepo(ctx, "", repoDir)
	if err != nil {
		t.Fatalf("ResolveRepo returned error: %v", err)
	}
	if got.FullName != "openvibely/openvibely" {
		t.Fatalf("expected upstream GitHub repo, got %+v", got)
	}
}

func TestResolveRepoFallsBackToFirstGitHubRemote(t *testing.T) {
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	cmd := exec.Command("git", "remote", "add", "mirror", "https://gitlab.com/example/not-this.git")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add mirror: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "fork", "https://github.com/example/fork.git")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add fork: %v\n%s", err, out)
	}

	svc := NewGitHubService(nil, "", "", "", "")
	got, err := svc.ResolveRepo(ctx, "", repoDir)
	if err != nil {
		t.Fatalf("ResolveRepo returned error: %v", err)
	}
	if got.FullName != "example/fork" {
		t.Fatalf("expected fallback GitHub remote, got %+v", got)
	}
}

func TestParseGitHubRepoURL(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "https", raw: "https://github.com/openvibely/openvibely", wantOwner: "openvibely", wantRepo: "openvibely"},
		{name: "https .git", raw: "https://github.com/openvibely/openvibely.git", wantOwner: "openvibely", wantRepo: "openvibely"},
		{name: "ssh short", raw: "git@github.com:openvibely/openvibely.git", wantOwner: "openvibely", wantRepo: "openvibely"},
		{name: "ssh url", raw: "ssh://git@github.com/openvibely/openvibely.git", wantOwner: "openvibely", wantRepo: "openvibely"},
		{name: "owner repo", raw: "openvibely/openvibely", wantOwner: "openvibely", wantRepo: "openvibely"},
		{name: "invalid host", raw: "https://gitlab.com/openvibely/openvibely", wantErr: true},
		{name: "invalid shape", raw: "https://github.com/openvibely", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubRepoURL(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.raw, err)
			}
			if got.Owner != tt.wantOwner || got.Name != tt.wantRepo {
				t.Fatalf("unexpected parse result: owner=%q repo=%q", got.Owner, got.Name)
			}
			if got.HTMLURL != "https://github.com/"+tt.wantOwner+"/"+tt.wantRepo {
				t.Fatalf("unexpected HTML URL: %s", got.HTMLURL)
			}
			if got.CloneURL != got.HTMLURL+".git" {
				t.Fatalf("unexpected clone URL: %s", got.CloneURL)
			}
		})
	}
}

func TestNormalizeGitHubAuthMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "pat", want: GitHubAuthModePAT},
		{in: "PAT", want: GitHubAuthModePAT},
		{in: "app", want: GitHubAuthModeApp},
		{in: "APP", want: GitHubAuthModeApp},
		{in: "", want: GitHubAuthModePAT},
		{in: "unknown", want: GitHubAuthModePAT},
	}

	for _, tt := range tests {
		if got := NormalizeGitHubAuthMode(tt.in); got != tt.want {
			t.Fatalf("NormalizeGitHubAuthMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGitHubTokenEnv_UsesBasicAuthHeader(t *testing.T) {
	env := gitHubTokenEnv("ghp_example")
	var headerVal string
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_CONFIG_VALUE_0=") {
			headerVal = strings.TrimPrefix(item, "GIT_CONFIG_VALUE_0=")
			break
		}
	}
	if headerVal == "" {
		t.Fatal("expected GIT_CONFIG_VALUE_0 to be set")
	}
	if !strings.HasPrefix(headerVal, "AUTHORIZATION: Basic ") {
		t.Fatalf("expected Basic auth header, got %q", headerVal)
	}
	encoded := strings.TrimPrefix(headerVal, "AUTHORIZATION: Basic ")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("failed decoding header token: %v", err)
	}
	if string(raw) != "x-access-token:ghp_example" {
		t.Fatalf("unexpected decoded auth payload: %q", string(raw))
	}
}

func TestGitAuthEnvForRepo_PATMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_repo_scoped"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/openvibely/openvibely.git")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin failed: %v\n%s", err, out)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	env := svc.GitAuthEnvForRepo(ctx, repoDir)
	if len(env) == 0 {
		t.Fatal("expected auth env for github repo in PAT mode")
	}

	var headerVal string
	for _, item := range env {
		if strings.HasPrefix(item, "GIT_CONFIG_VALUE_0=") {
			headerVal = strings.TrimPrefix(item, "GIT_CONFIG_VALUE_0=")
			break
		}
	}
	if !strings.Contains(headerVal, "Basic ") {
		t.Fatalf("expected Basic header, got %q", headerVal)
	}
}

func TestEnsureGitSSLConfig(t *testing.T) {
	t.Run("already configured with GIT_SSL_CAINFO", func(t *testing.T) {
		env := []string{"GIT_SSL_CAINFO=/custom/ca.pem", "OTHER=value"}
		result := ensureGitSSLConfig(env)
		if len(result) != 2 {
			t.Fatalf("expected env unchanged when GIT_SSL_CAINFO already set, got %d items", len(result))
		}
		if result[0] != "GIT_SSL_CAINFO=/custom/ca.pem" {
			t.Fatalf("expected GIT_SSL_CAINFO preserved")
		}
	})

	t.Run("already configured with SSL_CERT_FILE", func(t *testing.T) {
		env := []string{"SSL_CERT_FILE=/custom/cert.pem"}
		result := ensureGitSSLConfig(env)
		if len(result) != 1 {
			t.Fatalf("expected env unchanged when SSL_CERT_FILE already set")
		}
	})

	t.Run("adds CA bundle if found or falls back to no-verify", func(t *testing.T) {
		env := []string{"PATH=/usr/bin"}
		result := ensureGitSSLConfig(env)
		// Should either add GIT_SSL_CAINFO or GIT_SSL_NO_VERIFY
		// We can't predict which CA bundle exists on the test system
		foundCAInfo := false
		foundNoVerify := false
		for _, e := range result {
			if strings.HasPrefix(e, "GIT_SSL_CAINFO=") {
				foundCAInfo = true
			}
			if strings.HasPrefix(e, "GIT_SSL_NO_VERIFY=") {
				foundNoVerify = true
			}
		}
		// One of them must be set
		if !foundCAInfo && !foundNoVerify {
			t.Fatal("expected either GIT_SSL_CAINFO or GIT_SSL_NO_VERIFY to be set")
		}
		if foundCAInfo {
			t.Logf("CA bundle found and configured: %v", result)
		} else {
			t.Logf("No CA bundle found, falling back to GIT_SSL_NO_VERIFY")
		}
	})

	t.Run("respects existing GIT_SSL_NO_VERIFY in env", func(t *testing.T) {
		env := []string{"GIT_SSL_NO_VERIFY=false"}
		result := ensureGitSSLConfig(env)
		if len(result) != 1 {
			t.Fatalf("expected env unchanged when GIT_SSL_NO_VERIFY already set")
		}
		if result[0] != "GIT_SSL_NO_VERIFY=false" {
			t.Fatalf("expected GIT_SSL_NO_VERIFY preserved")
		}
	})
}

func TestFormatGitHubAPIError(t *testing.T) {
	t.Run("formats message and nested errors", func(t *testing.T) {
		body := []byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"head","code":"invalid","message":"A pull request already exists for openvibely:task/x."}]}`)
		got := formatGitHubAPIError(body)
		if !strings.Contains(got, "Validation Failed") {
			t.Fatalf("expected top-level message, got %q", got)
		}
		if !strings.Contains(got, "A pull request already exists") {
			t.Fatalf("expected nested error detail, got %q", got)
		}
	})

	t.Run("falls back to raw body when not json", func(t *testing.T) {
		got := formatGitHubAPIError([]byte("plain-text-error"))
		if got != "plain-text-error" {
			t.Fatalf("expected raw body fallback, got %q", got)
		}
	})
}

func TestCloneProjectRepo_NoPATFallsBackToLocalGitCLI(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", root)
	var calls []struct {
		env  []string
		args []string
	}
	svc.runGit = func(_ context.Context, _ string, extraEnv []string, args ...string) ([]byte, error) {
		calls = append(calls, struct {
			env  []string
			args []string
		}{append([]string(nil), extraEnv...), append([]string(nil), args...)})
		return nil, nil
	}

	clonedPath, normalizedURL, err := svc.CloneProjectRepo(ctx, "project-1", "https://github.com/openvibely/openvibely")
	if err != nil {
		t.Fatalf("CloneProjectRepo returned error: %v", err)
	}
	if clonedPath == "" || !strings.HasSuffix(clonedPath, "project-1") {
		t.Fatalf("expected managed clone destination for project id, got %q", clonedPath)
	}
	if normalizedURL != "https://github.com/openvibely/openvibely" {
		t.Fatalf("unexpected normalized URL: %q", normalizedURL)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one local git clone call, got %d", len(calls))
	}
	if got := strings.Join(calls[0].args, " "); got != "clone https://github.com/openvibely/openvibely "+clonedPath {
		t.Fatalf("unexpected git args: %q", got)
	}
	if envContainsPrefix(calls[0].env, "GIT_CONFIG_VALUE_0=") {
		t.Fatalf("local fallback should not inject GitHub auth header, got env %v", calls[0].env)
	}
	assertLocalGitCloneEnv(t, calls[0].env)
}

func TestCloneProjectRepo_NoPATFallbackPreservesSSHCloneURL(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", root)
	var gotArgs []string
	var gotEnv []string
	svc.runGit = func(_ context.Context, _ string, extraEnv []string, args ...string) ([]byte, error) {
		gotEnv = append([]string(nil), extraEnv...)
		gotArgs = append([]string(nil), args...)
		return nil, nil
	}

	_, normalizedURL, err := svc.CloneProjectRepo(ctx, "project-ssh", "git@github.com:openvibely/openvibely.git")
	if err != nil {
		t.Fatalf("CloneProjectRepo returned error: %v", err)
	}
	if normalizedURL != "https://github.com/openvibely/openvibely" {
		t.Fatalf("unexpected normalized URL: %q", normalizedURL)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "clone" || gotArgs[1] != "git@github.com:openvibely/openvibely.git" {
		t.Fatalf("expected local fallback to preserve SSH clone URL, got args %v", gotArgs)
	}
	assertLocalGitCloneEnv(t, gotEnv)
}

func TestCloneProjectRepo_PATConfiguredUsesTokenClone(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_configured"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", root)
	var gotEnv []string
	svc.runGit = func(_ context.Context, _ string, extraEnv []string, args ...string) ([]byte, error) {
		gotEnv = append([]string(nil), extraEnv...)
		if len(args) == 0 || args[0] != "clone" {
			t.Fatalf("expected git clone, got %v", args)
		}
		return nil, nil
	}

	if _, _, err := svc.CloneProjectRepo(ctx, "project-token", "https://github.com/openvibely/openvibely"); err != nil {
		t.Fatalf("CloneProjectRepo returned error: %v", err)
	}
	if !envContainsPrefix(gotEnv, "GIT_CONFIG_VALUE_0=AUTHORIZATION: Basic ") {
		t.Fatalf("expected token auth header env, got %v", gotEnv)
	}
}

func TestCloneProjectRepo_LocalGitFallbackFailureIncludesGitFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", root)
	svc.runGit = func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("git clone failed: authentication required")
	}

	_, _, err := svc.CloneProjectRepo(ctx, "project-fail", "https://github.com/openvibely/openvibely")
	if err == nil {
		t.Fatal("expected clone error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "GitHub auth was unavailable") {
		t.Fatalf("expected auth fallback context in error, got %q", msg)
	}
	if !strings.Contains(msg, "github personal access token is not configured") {
		t.Fatalf("expected missing PAT context in error, got %q", msg)
	}
	if !strings.Contains(msg, "local git clone failed") || !strings.Contains(msg, "authentication required") {
		t.Fatalf("expected underlying local git failure in error, got %q", msg)
	}
}

func TestCloneProjectRepo_LocalGitNonCredentialFailureOmitsPATContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	root := t.TempDir()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}

	svc := NewGitHubService(settingsRepo, "", "", "", root)
	svc.runGit = func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
		return nil, fmt.Errorf("git clone failed: unable to access remote: connection timed out")
	}

	_, _, err := svc.CloneProjectRepo(ctx, "project-network-fail", "https://github.com/openvibely/openvibely")
	if err == nil {
		t.Fatal("expected clone error")
	}
	msg := err.Error()
	if strings.Contains(msg, "github personal access token is not configured") {
		t.Fatalf("non-credential local git failure should not surface missing PAT context, got %q", msg)
	}
	if !strings.Contains(msg, "local git clone failed") || !strings.Contains(msg, "connection timed out") {
		t.Fatalf("expected underlying local git failure in error, got %q", msg)
	}
}

func assertLocalGitCloneEnv(t *testing.T, env []string) {
	t.Helper()
	for _, value := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true"} {
		if !envContainsValue(env, value) {
			t.Fatalf("expected non-interactive local git fallback env %q, got %v", value, env)
		}
	}
}

func envContainsPrefix(env []string, prefix string) bool {
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func envContainsValue(env []string, value string) bool {
	for _, item := range env {
		if item == value {
			return true
		}
	}
	return false
}

func TestGetPullRequestReturnsHeadRefFromResolvedRepository(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/openvibely/openvibely-hosted/pulls/4" {
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected configured PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":4,"html_url":"https://github.com/openvibely/openvibely-hosted/pull/4","state":"open","head":{"ref":"task/clean-history","repo":{"full_name":"openvibely/openvibely-hosted"}}}`))
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	pr, err := svc.GetPullRequest(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely-hosted"}, 4)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if pr.Number != 4 || pr.HeadRef != "task/clean-history" || pr.HeadRepoFullName != "openvibely/openvibely-hosted" {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
}

func TestDefaultGitHubSDLCLabelsDoNotUseProductPrefix(t *testing.T) {
	for _, label := range DefaultGitHubSDLCLabels {
		if strings.HasPrefix(label, "openvibely:") {
			t.Fatalf("default GitHub SDLC label must not use openvibely prefix: %q", label)
		}
	}
}

func TestListAssignedIssuesWithPullRequestsSkipsIssuesWithoutAssociatedPR(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	var issueListPath string
	var timelinePaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/openvibely/openvibely/issues":
			issueListPath = r.URL.RawQuery
			if r.URL.Query().Get("assignee") != "dev-bot" {
				t.Fatalf("expected assignee query dev-bot, got %q", r.URL.Query().Get("assignee"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"number":1,"html_url":"https://github.com/openvibely/openvibely/issues/1","title":"No PR","state":"open","user":{"login":"alice"},"assignees":[{"login":"dev-bot"}],"labels":[{"name":"bug"}]},
				{"number":2,"html_url":"https://github.com/openvibely/openvibely/issues/2","title":"Has PR","state":"open","user":{"login":"alice"},"assignees":[{"login":"dev-bot"}],"labels":[{"name":"approved"}]},
				{"number":3,"html_url":"https://github.com/openvibely/openvibely/pull/3","title":"PR issue object","state":"open","pull_request":{}}
			]`))
		case "/repos/openvibely/openvibely/issues/1/timeline":
			timelinePaths = append(timelinePaths, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		case "/repos/openvibely/openvibely/issues/2/timeline":
			timelinePaths = append(timelinePaths, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"source":{"issue":{"number":42,"html_url":"https://github.com/openvibely/openvibely/pull/42","state":"open","pull_request":{}}}}
			]`))
		default:
			t.Fatalf("unexpected GitHub API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	repo := &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}

	items, err := svc.ListAssignedIssuesWithPullRequests(ctx, repo, "dev-bot")
	if err != nil {
		t.Fatalf("ListAssignedIssuesWithPullRequests returned error: %v", err)
	}
	if issueListPath == "" {
		t.Fatal("expected assigned issues endpoint to be called")
	}
	if len(timelinePaths) != 2 {
		t.Fatalf("expected timeline lookup only for two non-PR issues, got %d paths %v", len(timelinePaths), timelinePaths)
	}
	if len(items) != 1 {
		t.Fatalf("expected only issues with associated PRs, got %d: %#v", len(items), items)
	}
	if items[0].Issue.Number != 2 {
		t.Fatalf("expected issue 2 to be returned, got issue %d", items[0].Issue.Number)
	}
	if items[0].PullRequest.Number != 42 {
		t.Fatalf("expected associated PR #42, got #%d", items[0].PullRequest.Number)
	}
}

func TestListAuthenticatedAssignedIssuesUsesConfiguredTokenUser(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	var sawUser bool
	var issueListQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			sawUser = true
			if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
				t.Fatalf("expected PAT bearer auth for /user, got %q", got)
			}
			_, _ = w.Write([]byte(`{"login":"channel-user"}`))
		case "/repos/openvibely/openvibely/issues":
			issueListQuery = r.URL.RawQuery
			if got := r.URL.Query().Get("assignee"); got != "channel-user" {
				t.Fatalf("expected authenticated channel user assignee, got %q", got)
			}
			if got := r.URL.Query().Get("state"); got != "open" {
				t.Fatalf("expected open issue query, got state=%q", got)
			}
			_, _ = w.Write([]byte(`[
				{"number":5,"html_url":"https://github.com/openvibely/openvibely/issues/5","title":"Testing","state":"open","user":{"login":"alice"},"assignees":[{"login":"channel-user"}],"labels":[{"name":"bug"}]},
				{"number":6,"html_url":"https://github.com/openvibely/openvibely/pull/6","title":"PR object","state":"open","pull_request":{}}
			]`))
		default:
			t.Fatalf("unexpected GitHub API path: %s", r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	repo := &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}

	user, issues, err := svc.ListAuthenticatedAssignedIssues(ctx, repo)
	if err != nil {
		t.Fatalf("ListAuthenticatedAssignedIssues returned error: %v", err)
	}
	if !sawUser || issueListQuery == "" {
		t.Fatalf("expected /user and assigned issues endpoints, sawUser=%v query=%q", sawUser, issueListQuery)
	}
	if user == nil || user.Login != "channel-user" || user.Source != GitHubAuthModePAT {
		t.Fatalf("unexpected authenticated user: %#v", user)
	}
	if len(issues) != 1 || issues[0].Number != 5 || issues[0].Title != "Testing" {
		t.Fatalf("expected only real open issue assigned to token user, got %#v", issues)
	}
	cachedLogin, err := settingsRepo.Get(ctx, GitHubSettingPATUserLogin)
	if err != nil {
		t.Fatalf("get cached PAT login: %v", err)
	}
	if cachedLogin != "channel-user" {
		t.Fatalf("expected PAT login cache to be updated, got %q", cachedLogin)
	}
}

func TestListAuthenticatedAssignedIssuesRejectsGitHubAppInstallationAccount(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModeApp); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, githubSettingAccountLogin, "openvibely-org"); err != nil {
		t.Fatalf("set app account login: %v", err)
	}
	if err := settingsRepo.Set(ctx, githubSettingAccountType, "Organization"); err != nil {
		t.Fatalf("set app account type: %v", err)
	}
	if err := settingsRepo.Set(ctx, githubSettingInstallationID, "123"); err != nil {
		t.Fatalf("set installation id: %v", err)
	}

	var sawIssueList bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIssueList = true
		t.Fatalf("github_list_my_assigned_issues must not query issues with GitHub App installation account %s", r.URL.String())
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	repo := &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}

	_, _, err := svc.ListAuthenticatedAssignedIssues(ctx, repo)
	if err == nil || !strings.Contains(err.Error(), "requires a PAT user token") || !strings.Contains(err.Error(), "github_list_assigned_issues") {
		t.Fatalf("expected GitHub App guidance error, got %v", err)
	}
	if sawIssueList {
		t.Fatalf("expected no GitHub issue-list request for GitHub App installation account")
	}
}

func TestGitHubIssueLabelsRejectOpenVibelyPrefixBeforeTransport(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("prefixed labels must be rejected before GitHub transport, got %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	repo := &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}

	if _, err := svc.CreateIssue(ctx, repo, GitHubCreateIssueRequest{Title: "Bug", Labels: []string{"bug", " openvibely:bug "}}); err == nil || !strings.Contains(err.Error(), "openvibely:") {
		t.Fatalf("expected prefixed create label rejection, got %v", err)
	}
	if err := svc.AddLabelsToIssue(ctx, repo, 7, []string{"OpenVibely:approved"}); err == nil || !strings.Contains(err.Error(), "openvibely:") {
		t.Fatalf("expected prefixed add-label rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no GitHub API calls for rejected labels, got %d", calls)
	}
}

func TestPublishBranchUsesGitHubAPIWithoutGitPush(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	baseCmd := exec.Command("git", "rev-parse", "main")
	baseCmd.Dir = repoDir
	baseOut, err := baseCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse main: %v", err)
	}
	localBaseSHA := strings.TrimSpace(string(baseOut))
	remoteBaseSHA := "1111111111111111111111111111111111111111"
	if remoteBaseSHA == localBaseSHA {
		t.Fatal("test requires distinct local and remote base shas")
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "new.txt"), []byte("new file\n"), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	var paths []string
	var treePayload string
	var commitPayload string
	var refPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"sha":"blob-%d"}`, len(paths))))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			body, _ := io.ReadAll(r.Body)
			treePayload = string(body)
			if !strings.Contains(treePayload, `"base_tree":"base-tree"`) || !strings.Contains(treePayload, `"path":"README.md"`) || !strings.Contains(treePayload, `"path":"new.txt"`) {
				t.Fatalf("unexpected tree payload: %s", treePayload)
			}
			_, _ = w.Write([]byte(`{"sha":"new-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			body, _ := io.ReadAll(r.Body)
			commitPayload = string(body)
			if !strings.Contains(commitPayload, `"message":"Publish via API"`) || !strings.Contains(commitPayload, `"tree":"new-tree"`) || !strings.Contains(commitPayload, remoteBaseSHA) || strings.Contains(commitPayload, localBaseSHA) {
				t.Fatalf("unexpected commit payload: %s", commitPayload)
			}
			_, _ = w.Write([]byte(`{"sha":"new-commit"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			body, _ := io.ReadAll(r.Body)
			refPayload = string(body)
			if !strings.Contains(refPayload, `"sha":"new-commit"`) || !strings.Contains(refPayload, `"force":false`) {
				t.Fatalf("unexpected ref payload: %s", refPayload)
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/refs":
			body, _ := io.ReadAll(r.Body)
			refPayload = string(body)
			if !strings.Contains(refPayload, `"ref":"refs/heads/task/api-publish"`) || !strings.Contains(refPayload, `"sha":"new-commit"`) {
				t.Fatalf("unexpected create-ref payload: %s", refPayload)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"new-commit"}}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	err = svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:       repoDir,
		Branch:         "task/api-publish",
		BaseBranch:     "main",
		CommitMessage:  "Publish via API",
		CommitterName:  "OpenVibely Bot",
		CommitterEmail: "bot@openvibely.ai",
	})
	if err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if treePayload == "" || commitPayload == "" || refPayload == "" {
		t.Fatalf("expected tree, commit, and ref API calls paths=%v", paths)
	}
}

func TestPublishBranchPublishesCleanCommittedLocalBranchChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	cmd := exec.Command("git", "switch", "-c", "task/api-publish")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git switch task branch: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("committed branch update\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add README.md: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cmd = exec.Command("git", "commit", "-m", "Update README on task branch")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit README.md: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status --porcelain: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected clean worktree after local commit, got %q", strings.TrimSpace(string(out)))
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	remoteBranchSHA := "2222222222222222222222222222222222222222"
	var sawBlob bool
	var sawTree bool
	var sawCommit bool
	var sawRef bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBranchSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"old-remote-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			sawBlob = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), base64.StdEncoding.EncodeToString([]byte("committed branch update\n"))) {
				t.Fatalf("expected committed README content in blob payload, got %s", string(body))
			}
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			sawTree = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"base_tree":"base-tree"`) || !strings.Contains(text, `"path":"README.md"`) {
				t.Fatalf("unexpected tree payload: %s", text)
			}
			_, _ = w.Write([]byte(`{"sha":"new-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			sawCommit = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, remoteBranchSHA) || strings.Contains(text, remoteBaseSHA) {
				t.Fatalf("expected existing remote branch parent for committed local changes, got %s", text)
			}
			_, _ = w.Write([]byte(`{"sha":"new-commit"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			sawRef = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"sha":"new-commit"`) || !strings.Contains(text, `"force":false`) {
				t.Fatalf("unexpected ref payload: %s", text)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"new-commit"}}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish committed local branch via API",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if !sawBlob || !sawTree || !sawCommit || !sawRef {
		t.Fatalf("expected blob/tree/commit/ref API calls, got blob=%v tree=%v commit=%v ref=%v", sawBlob, sawTree, sawCommit, sawRef)
	}
}

func TestPublishBranchParentsExistingRemoteTaskBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("updated again\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	remoteBranchSHA := "2222222222222222222222222222222222222222"
	var commitPayload string
	var refPayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBranchSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"old-remote-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"base_tree":"base-tree"`) || !strings.Contains(string(body), `"path":"README.md"`) {
				t.Fatalf("unexpected tree payload: %s", string(body))
			}
			_, _ = w.Write([]byte(`{"sha":"new-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			body, _ := io.ReadAll(r.Body)
			commitPayload = string(body)
			if !strings.Contains(commitPayload, remoteBranchSHA) || strings.Contains(commitPayload, remoteBaseSHA) {
				t.Fatalf("expected existing remote branch parent, got commit payload: %s", commitPayload)
			}
			_, _ = w.Write([]byte(`{"sha":"new-commit"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			body, _ := io.ReadAll(r.Body)
			refPayload = string(body)
			if !strings.Contains(refPayload, `"sha":"new-commit"`) || !strings.Contains(refPayload, `"force":false`) {
				t.Fatalf("unexpected ref payload: %s", refPayload)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"new-commit"}}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish existing branch via API",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if commitPayload == "" || refPayload == "" {
		t.Fatalf("expected commit and ref API calls")
	}
}

func TestPublishBranchNoOpsWhenDesiredTreeMatchesRemoteTaskBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("already published\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	remoteBranchSHA := "2222222222222222222222222222222222222222"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBranchSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"desired-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			_, _ = w.Write([]byte(`{"sha":"desired-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			t.Fatal("unchanged desired tree must not synthesize another commit")
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			t.Fatal("unchanged desired tree must not update the remote ref")
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Do not duplicate published tree",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
}

func TestPublishBranchNoOpsWhenNoChangesAndRemoteTaskBranchExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	remoteBaseSHA := "1111111111111111111111111111111111111111"
	remoteBranchSHA := "2222222222222222222222222222222222222222"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBranchSHA + `"}}`))
		default:
			t.Fatalf("unexpected GitHub API request for no-op publish: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "No-op publish via API",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
}

func TestPublishBranchCreatesRemoteBranchAtBaseWhenNoChangesAndBranchAbsent(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	remoteBaseSHA := "1111111111111111111111111111111111111111"
	var sawCreateRef bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"sha":"`+remoteBaseSHA+`"`) || !strings.Contains(text, `"force":false`) {
				t.Fatalf("unexpected ref update payload: %s", text)
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/refs":
			sawCreateRef = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"ref":"refs/heads/task/api-publish"`) || !strings.Contains(text, `"sha":"`+remoteBaseSHA+`"`) {
				t.Fatalf("unexpected create-ref payload: %s", text)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"` + remoteBaseSHA + `"}}`))
		default:
			t.Fatalf("unexpected GitHub API request for empty branch publish: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Create empty branch via API",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if !sawCreateRef {
		t.Fatal("expected branch ref creation")
	}
}

func TestPublishBranchNoChangesConcurrentBranchCreationSucceeds(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	remoteBaseSHA := "1111111111111111111111111111111111111111"
	concurrentBranchSHA := "2222222222222222222222222222222222222222"
	branchGets := 0
	patches := 0
	createRefs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			branchGets++
			if branchGets == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":{"sha":"` + concurrentBranchSHA + `"}}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			patches++
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"sha":"`+remoteBaseSHA+`"`) || !strings.Contains(text, `"force":false`) {
				t.Fatalf("unexpected ref update payload: %s", text)
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/refs":
			createRefs++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
		default:
			t.Fatalf("unexpected GitHub API request for no-change create race: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Reuse concurrently created empty branch",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if branchGets != 2 || patches != 1 || createRefs != 1 {
		t.Fatalf("expected no-change create-ref reconciliation, got branchGets=%d patches=%d createRefs=%d", branchGets, patches, createRefs)
	}
}

func TestPublishBranchRetriesWithLatestRemoteBranchParentOnNonFastForward(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("updated after race\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	staleBranchSHA := "2222222222222222222222222222222222222222"
	latestBranchSHA := "3333333333333333333333333333333333333333"
	refGets := 0
	commitPosts := 0
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghp_test" {
			t.Fatalf("expected PAT bearer auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			refGets++
			sha := staleBranchSHA
			if refGets > 1 {
				sha = latestBranchSHA
			}
			_, _ = w.Write([]byte(`{"object":{"sha":"` + sha + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && (r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+staleBranchSHA || r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+latestBranchSHA):
			_, _ = w.Write([]byte(`{"tree":{"sha":"old-remote-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"sha":"new-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			commitPosts++
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if commitPosts == 1 && (!strings.Contains(text, staleBranchSHA) || strings.Contains(text, latestBranchSHA)) {
				t.Fatalf("expected first commit to parent stale branch, got: %s", text)
			}
			if commitPosts == 2 && (!strings.Contains(text, latestBranchSHA) || strings.Contains(text, staleBranchSHA)) {
				t.Fatalf("expected retry commit to parent latest branch, got: %s", text)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"sha":"new-commit-%d"}`, commitPosts)))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			patches++
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"force":false`) {
				t.Fatalf("unexpected forced ref update: %s", text)
			}
			if patches == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Update is not a fast forward"}`))
				return
			}
			if !strings.Contains(text, `"sha":"new-commit-2"`) {
				t.Fatalf("expected retry commit ref update, got: %s", text)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"new-commit-2"}}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "add" || args[0] == "commit" || args[0] == "push") {
			t.Fatalf("PublishBranch must not invoke git %s", args[0])
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish retry via API",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if refGets != 2 || commitPosts != 2 || patches != 2 {
		t.Fatalf("expected ref retry flow, got refGets=%d commitPosts=%d patches=%d", refGets, commitPosts, patches)
	}
}

func TestPublishBranchRetriesAfterConcurrentBranchCreation(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("updated after create race\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	concurrentBranchSHA := "2222222222222222222222222222222222222222"
	branchGets := 0
	commitPosts := 0
	patches := 0
	createRefs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			branchGets++
			if branchGets == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":{"sha":"` + concurrentBranchSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+concurrentBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"concurrent-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			_, _ = w.Write([]byte(`{"sha":"desired-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			commitPosts++
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if commitPosts == 1 && (!strings.Contains(text, remoteBaseSHA) || strings.Contains(text, concurrentBranchSHA)) {
				t.Fatalf("expected first commit to parent remote base, got: %s", text)
			}
			if commitPosts == 2 && (!strings.Contains(text, concurrentBranchSHA) || strings.Contains(text, remoteBaseSHA)) {
				t.Fatalf("expected retry commit to parent concurrently created branch, got: %s", text)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"sha":"new-commit-%d"}`, commitPosts)))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			patches++
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if !strings.Contains(text, `"force":false`) {
				t.Fatalf("unexpected forced ref update: %s", text)
			}
			if patches == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
				return
			}
			if !strings.Contains(text, `"sha":"new-commit-2"`) {
				t.Fatalf("expected retry commit ref update, got: %s", text)
			}
			_, _ = w.Write([]byte(`{"ref":"refs/heads/task/api-publish","object":{"sha":"new-commit-2"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/refs":
			createRefs++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish after create race",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if branchGets != 2 || commitPosts != 2 || patches != 2 || createRefs != 1 {
		t.Fatalf("expected create-ref race retry, got branchGets=%d commitPosts=%d patches=%d createRefs=%d", branchGets, commitPosts, patches, createRefs)
	}
}

func TestPublishBranchConcurrentBranchCreationNoOpsWhenDesiredTreeMatches(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("published during create race\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	concurrentBranchSHA := "2222222222222222222222222222222222222222"
	branchGets := 0
	commitPosts := 0
	patches := 0
	createRefs := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			branchGets++
			if branchGets == 1 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
				return
			}
			_, _ = w.Write([]byte(`{"object":{"sha":"` + concurrentBranchSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+concurrentBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"desired-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			_, _ = w.Write([]byte(`{"sha":"desired-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			commitPosts++
			if commitPosts > 1 {
				t.Fatal("create-ref race must not synthesize a duplicate commit when the remote tree matches")
			}
			_, _ = w.Write([]byte(`{"sha":"stale-attempt-commit"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			patches++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Reference does not exist"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/refs":
			createRefs++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish during create race",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if branchGets != 2 || commitPosts != 1 || patches != 1 || createRefs != 1 {
		t.Fatalf("expected create-ref race to reuse desired tree, got branchGets=%d commitPosts=%d patches=%d createRefs=%d", branchGets, commitPosts, patches, createRefs)
	}
}

func TestPublishBranchRaceNoOpsWhenLatestRemoteTreeMatchesDesiredTree(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("published concurrently\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	remoteBaseSHA := "1111111111111111111111111111111111111111"
	staleBranchSHA := "2222222222222222222222222222222222222222"
	latestBranchSHA := "3333333333333333333333333333333333333333"
	refGets := 0
	commitPosts := 0
	patches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"` + remoteBaseSHA + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/ref/heads/task/api-publish":
			refGets++
			sha := staleBranchSHA
			if refGets > 1 {
				sha = latestBranchSHA
			}
			_, _ = w.Write([]byte(`{"object":{"sha":"` + sha + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+remoteBaseSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"base-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+staleBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"old-remote-tree"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/git/commits/"+latestBranchSHA:
			_, _ = w.Write([]byte(`{"tree":{"sha":"desired-tree"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/blobs":
			_, _ = w.Write([]byte(`{"sha":"blob-readme"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/trees":
			_, _ = w.Write([]byte(`{"sha":"desired-tree"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/git/commits":
			commitPosts++
			if commitPosts > 1 {
				t.Fatal("race retry must not synthesize a duplicate commit")
			}
			_, _ = w.Write([]byte(`{"sha":"stale-attempt-commit"}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/openvibely/openvibely/git/refs/heads/task/api-publish":
			patches++
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"Update is not a fast forward"}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	if err := svc.PublishBranch(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubPublishBranchRequest{
		RepoPath:      repoDir,
		Branch:        "task/api-publish",
		BaseBranch:    "main",
		CommitMessage: "Publish with concurrent retry",
	}); err != nil {
		t.Fatalf("PublishBranch returned error: %v", err)
	}
	if refGets != 2 || commitPosts != 1 || patches != 1 {
		t.Fatalf("expected race to reuse latest desired tree, got refGets=%d commitPosts=%d patches=%d", refGets, commitPosts, patches)
	}
}

func TestGitHubIssueAPIMethods(t *testing.T) {
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT); err != nil {
		t.Fatalf("set auth mode: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set pat: %v", err)
	}

	var sawCreate, sawGet, sawComment, sawLabels bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/issues":
			sawCreate = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if strings.Contains(text, "openvibely:") {
				t.Fatalf("issue creation must not send prefixed labels: %s", text)
			}
			if !strings.Contains(text, `"labels":["bug","approved"]`) || !strings.Contains(text, `"assignees":["dev-bot"]`) {
				t.Fatalf("unexpected create issue payload: %s", text)
			}
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/openvibely/openvibely/issues/7","title":"Bug","body":"Fix it","state":"open","user":{"login":"alice"},"assignees":[{"login":"dev-bot"}],"labels":[{"name":"bug"},{"name":"approved"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/openvibely/openvibely/issues/7":
			sawGet = true
			_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/openvibely/openvibely/issues/7","title":"Bug","body":"Fix it","state":"open","user":{"login":"alice"},"assignees":[{"login":"dev-bot"}],"labels":[{"name":"bug"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/issues/7/comments":
			sawComment = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"body":"working on it"`) {
				t.Fatalf("unexpected comment payload: %s", string(body))
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/openvibely/openvibely/issues/7/labels":
			sawLabels = true
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			if text != `{"labels":["in-progress","pr-opened"]}` {
				t.Fatalf("unexpected labels payload: %s", text)
			}
			_, _ = w.Write([]byte(`[{"name":"in-progress"},{"name":"pr-opened"}]`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	svc.apiBaseURL = server.URL
	repo := &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}
	created, err := svc.CreateIssue(ctx, repo, GitHubCreateIssueRequest{Title: "Bug", Body: "Fix it", Labels: []string{"bug", "approved", "bug", ""}, Assignees: []string{"dev-bot"}})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if created.Number != 7 || created.UserLogin != "alice" || len(created.Labels) != 2 {
		t.Fatalf("unexpected created issue: %#v", created)
	}
	issue, err := svc.GetIssue(ctx, repo, 7)
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.Number != 7 || issue.Title != "Bug" {
		t.Fatalf("unexpected issue: %#v", issue)
	}
	if err := svc.CommentOnIssue(ctx, repo, 7, "working on it"); err != nil {
		t.Fatalf("CommentOnIssue returned error: %v", err)
	}
	if err := svc.AddLabelsToIssue(ctx, repo, 7, []string{"in-progress", "pr-opened", "in-progress"}); err != nil {
		t.Fatalf("AddLabelsToIssue returned error: %v", err)
	}
	if !sawCreate || !sawGet || !sawComment || !sawLabels {
		t.Fatalf("expected all issue API endpoints to be called create=%v get=%v comment=%v labels=%v", sawCreate, sawGet, sawComment, sawLabels)
	}
}

func TestReplaceBranchHeadUsesAtomicForceWithLease(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set PAT: %v", err)
	}
	repoDir := createTestGitRepo(t)
	checkoutTestBranch(t, repoDir, "task/clean-history")

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	var pushArgs []string
	var pushEnv []string
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			pushArgs = append([]string(nil), args...)
			pushEnv = append([]string(nil), extraEnv...)
			return []byte("ok"), nil
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}

	expected := strings.Repeat("a", 40)
	if err := svc.ReplaceBranchHead(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubReplaceBranchHeadRequest{
		WorktreePath: repoDir,
		Branch:       "task/clean-history",
		ExpectedHead: expected,
	}); err != nil {
		t.Fatalf("ReplaceBranchHead returned error: %v", err)
	}

	wantArgs := []string{
		"push",
		"--force-with-lease=refs/heads/task/clean-history:" + expected,
		"https://github.com/openvibely/openvibely.git",
		"HEAD:refs/heads/task/clean-history",
	}
	if strings.Join(pushArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected push args: %#v", pushArgs)
	}
	if joined := strings.Join(pushArgs, " "); strings.Contains(joined, "ghp_test") {
		t.Fatalf("token leaked into command arguments: %q", joined)
	}
	if joined := strings.Join(pushEnv, "\n"); !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") || !strings.Contains(joined, "AUTHORIZATION: Basic") {
		t.Fatalf("expected non-interactive token environment, got %#v", pushEnv)
	}
}

func TestReplaceBranchHeadRefusesDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	checkoutTestBranch(t, repoDir, "task/clean-history")
	if err := os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	svc := NewGitHubService(nil, "", "", "", "")
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			t.Fatal("dirty worktree must not be pushed")
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}

	err := svc.ReplaceBranchHead(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubReplaceBranchHeadRequest{
		WorktreePath: repoDir,
		Branch:       "task/clean-history",
		ExpectedHead: strings.Repeat("a", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "worktree must be clean") {
		t.Fatalf("expected clean-worktree error, got %v", err)
	}
}

func TestReplaceBranchHeadRefusesMismatchedWorktreeBranch(t *testing.T) {
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	svc := NewGitHubService(nil, "", "", "", "")
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			t.Fatal("mismatched worktree branch must not be pushed")
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}

	err := svc.ReplaceBranchHead(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubReplaceBranchHeadRequest{
		WorktreePath: repoDir,
		Branch:       "task/clean-history",
		ExpectedHead: strings.Repeat("a", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "must be checked out on task branch") {
		t.Fatalf("expected worktree branch mismatch error, got %v", err)
	}
}

func checkoutTestBranch(t *testing.T, repoDir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout test branch: %v\n%s", err, out)
	}
}

func TestReplaceBranchHeadDoesNotBypassFailedLease(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "ghp_test"); err != nil {
		t.Fatalf("set PAT: %v", err)
	}
	repoDir := createTestGitRepo(t)
	checkoutTestBranch(t, repoDir, "task/clean-history")

	svc := NewGitHubService(settingsRepo, "", "", "", "")
	pushes := 0
	svc.runGit = func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "push" {
			pushes++
			return nil, fmt.Errorf("stale info: rejected")
		}
		return defaultRunGit(ctx, dir, extraEnv, args...)
	}

	err := svc.ReplaceBranchHead(ctx, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, GitHubReplaceBranchHeadRequest{
		WorktreePath: repoDir,
		Branch:       "task/clean-history",
		ExpectedHead: strings.Repeat("a", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "lease-guarded branch replacement") {
		t.Fatalf("expected guarded replacement error, got %v", err)
	}
	if pushes != 1 {
		t.Fatalf("expected one guarded push attempt, got %d", pushes)
	}
}
