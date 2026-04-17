package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveEnableLocalRepoPath(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		want     bool
	}{
		{name: "explicit true", explicit: "true", want: true},
		{name: "explicit false", explicit: "false", want: false},
		{name: "unset defaults false", explicit: "", want: false},
		{name: "invalid defaults false", explicit: "maybe", want: false},
		{name: "numeric true", explicit: "1", want: true},
		{name: "numeric false", explicit: "0", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEnableLocalRepoPath(tt.explicit)
			if got != tt.want {
				t.Fatalf("ResolveEnableLocalRepoPath(%q) = %v, want %v", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestResolveEnableTaskChangesMergeOptions(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		want     bool
	}{
		{name: "explicit true", explicit: "true", want: true},
		{name: "explicit false", explicit: "false", want: false},
		{name: "unset defaults false", explicit: "", want: false},
		{name: "invalid defaults false", explicit: "maybe", want: false},
		{name: "numeric true", explicit: "1", want: true},
		{name: "numeric false", explicit: "0", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEnableTaskChangesMergeOptions(tt.explicit)
			if got != tt.want {
				t.Fatalf("ResolveEnableTaskChangesMergeOptions(%q) = %v, want %v", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestResolveAppBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "trim and strip trailing slash", raw: "  https://dubee.org/ ", want: "https://dubee.org"},
		{name: "allow path prefix", raw: "https://dubee.org/openvibely/", want: "https://dubee.org/openvibely"},
		{name: "http localhost", raw: "http://localhost:3001", want: "http://localhost:3001"},
		{name: "reject no scheme", raw: "dubee.org", want: ""},
		{name: "reject unsupported scheme", raw: "ftp://dubee.org", want: ""},
		{name: "reject query", raw: "https://dubee.org?x=1", want: ""},
		{name: "reject fragment", raw: "https://dubee.org#frag", want: ""},
		{name: "reject user info", raw: "https://user:pass@dubee.org", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAppBaseURL(tt.raw); got != tt.want {
				t.Fatalf("ResolveAppBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidateAppBaseURL(t *testing.T) {
	if err := ValidateAppBaseURL(""); err != nil {
		t.Fatalf("ValidateAppBaseURL empty returned unexpected error: %v", err)
	}
	if err := ValidateAppBaseURL("https://dubee.org"); err != nil {
		t.Fatalf("ValidateAppBaseURL valid URL returned unexpected error: %v", err)
	}
	err := ValidateAppBaseURL("dubee.org")
	if err == nil {
		t.Fatal("expected error for invalid APP_BASE_URL")
	}
	if !strings.Contains(err.Error(), "APP_BASE_URL") {
		t.Fatalf("expected APP_BASE_URL in validation error, got %q", err)
	}
}

func TestResolveAuthEnabled(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		username string
		password string
		want     bool
	}{
		{name: "explicit true", explicit: "true", want: true},
		{name: "explicit false", explicit: "false", username: "u", password: "p", want: false},
		{name: "inferred true from credentials", username: "u", password: "p", want: true},
		{name: "inferred false missing password", username: "u", password: "", want: false},
		{name: "inferred false missing username", username: "", password: "p", want: false},
		{name: "invalid explicit falls back to inference", explicit: "maybe", username: "u", password: "p", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAuthEnabled(tt.explicit, tt.username, tt.password); got != tt.want {
				t.Fatalf("ResolveAuthEnabled(%q,%q,%q)=%v want %v", tt.explicit, tt.username, tt.password, got, tt.want)
			}
		})
	}
}

func TestResolveAuthSessionTTL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty defaults", raw: "", want: "24h0m0s"},
		{name: "valid hours", raw: "48h", want: "48h0m0s"},
		{name: "valid minutes", raw: "30m", want: "30m0s"},
		{name: "invalid defaults", raw: "abc", want: "24h0m0s"},
		{name: "non-positive defaults", raw: "0s", want: "24h0m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAuthSessionTTL(tt.raw).String(); got != tt.want {
				t.Fatalf("ResolveAuthSessionTTL(%q)=%q want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// --- Runtime mode tests ---

func TestLoadWithMode_ServerDefaults(t *testing.T) {
	// Clear env vars that would override defaults.
	for _, k := range []string{"PORT", "DATABASE_PATH", "PROJECT_REPO_ROOT", "OPENVIBELY_ENABLE_LOCAL_REPO_PATH"} {
		prev := os.Getenv(k)
		os.Unsetenv(k)
		defer os.Setenv(k, prev)
	}

	cfg := LoadWithMode(ModeServer)
	if cfg.Mode != ModeServer {
		t.Fatalf("expected mode server, got %s", cfg.Mode)
	}
	if cfg.Port != "3001" {
		t.Fatalf("expected server default port 3001, got %s", cfg.Port)
	}
	if cfg.DatabasePath != "./openvibely.db" {
		t.Fatalf("expected server default DB path, got %s", cfg.DatabasePath)
	}
	if cfg.ProjectRepoRoot != "./repos" {
		t.Fatalf("expected server default repo root, got %s", cfg.ProjectRepoRoot)
	}
	if cfg.EnableLocalRepoPath {
		t.Fatal("expected server default EnableLocalRepoPath=false")
	}
}

func TestLoadWithMode_DesktopDefaults(t *testing.T) {
	// Clear env vars that would override defaults.
	for _, k := range []string{"PORT", "DATABASE_PATH", "PROJECT_REPO_ROOT", "OPENVIBELY_ENABLE_LOCAL_REPO_PATH"} {
		prev := os.Getenv(k)
		os.Unsetenv(k)
		defer os.Setenv(k, prev)
	}

	cfg := LoadWithMode(ModeDesktop)
	if cfg.Mode != ModeDesktop {
		t.Fatalf("expected mode desktop, got %s", cfg.Mode)
	}
	if cfg.Port != "0" {
		t.Fatalf("expected desktop default port 0 (ephemeral), got %s", cfg.Port)
	}

	// DB path should be inside OS app-data dir, not relative.
	if cfg.DatabasePath == "./openvibely.db" {
		t.Fatal("desktop mode should not use relative default DB path")
	}
	if !filepath.IsAbs(cfg.DatabasePath) {
		t.Fatalf("expected absolute desktop DB path, got %s", cfg.DatabasePath)
	}
	if !strings.Contains(cfg.DatabasePath, "openvibely.db") {
		t.Fatalf("expected 'openvibely.db' in path, got %s", cfg.DatabasePath)
	}

	// Repo root should also be inside app-data dir.
	if cfg.ProjectRepoRoot == "./repos" {
		t.Fatal("desktop mode should not use relative default repo root")
	}

	// Desktop mode enables local repo paths by default.
	if !cfg.EnableLocalRepoPath {
		t.Fatal("expected desktop default EnableLocalRepoPath=true")
	}
}

func TestLoadWithMode_DesktopEnvOverrides(t *testing.T) {
	// Env vars override desktop defaults.
	os.Setenv("PORT", "9999")
	os.Setenv("DATABASE_PATH", "/custom/path.db")
	os.Setenv("PROJECT_REPO_ROOT", "/custom/repos")
	os.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	defer func() {
		os.Unsetenv("PORT")
		os.Unsetenv("DATABASE_PATH")
		os.Unsetenv("PROJECT_REPO_ROOT")
		os.Unsetenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH")
	}()

	cfg := LoadWithMode(ModeDesktop)
	if cfg.Port != "9999" {
		t.Fatalf("expected env override port 9999, got %s", cfg.Port)
	}
	if cfg.DatabasePath != "/custom/path.db" {
		t.Fatalf("expected env override DB path, got %s", cfg.DatabasePath)
	}
	if cfg.ProjectRepoRoot != "/custom/repos" {
		t.Fatalf("expected env override repo root, got %s", cfg.ProjectRepoRoot)
	}
	if cfg.EnableLocalRepoPath {
		t.Fatal("expected env override EnableLocalRepoPath=false")
	}
}

func TestDesktopDataDir(t *testing.T) {
	dir := desktopDataDir()
	if dir == "" {
		t.Fatal("desktopDataDir returned empty string")
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("expected absolute path, got %s", dir)
	}

	// Basic OS-specific path checks.
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(dir, "Library/Application Support") {
			t.Fatalf("macOS data dir should be in Library/Application Support, got %s", dir)
		}
	case "linux":
		if !strings.Contains(dir, ".local/share") && !strings.Contains(dir, os.Getenv("XDG_DATA_HOME")) {
			t.Fatalf("linux data dir should be under XDG_DATA_HOME or ~/.local/share, got %s", dir)
		}
	}
}

func TestLoad_IsServerMode(t *testing.T) {
	cfg := Load()
	if cfg.Mode != ModeServer {
		t.Fatalf("Load() should produce server mode, got %s", cfg.Mode)
	}
}
