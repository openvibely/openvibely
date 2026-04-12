package service

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

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
