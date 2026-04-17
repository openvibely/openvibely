package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RuntimeMode distinguishes web/server deployments from desktop app mode.
type RuntimeMode string

const (
	// ModeServer is the default web/VPS/Docker deployment mode.
	ModeServer RuntimeMode = "server"
	// ModeDesktop is the Wails desktop app mode.
	ModeDesktop RuntimeMode = "desktop"
)

type Config struct {
	// Mode is the runtime mode (server or desktop).
	Mode                          RuntimeMode
	Port                          string
	DatabasePath                  string
	DatabaseURL                   string
	AnthropicKey                  string
	TelegramToken                 string
	Environment                   string
	GitHubAppID                   string
	GitHubAppSlug                 string
	GitHubAppPrivateKey           string
	SlackClientID                 string
	SlackClientSecret             string
	SlackAppToken                 string
	SlackBotToken                 string
	AppBaseURL                    string
	ProjectRepoRoot               string
	EnableLocalRepoPath           bool
	EnableTaskChangesMergeOptions bool
	AuthEnabled                   bool
	AuthUsername                  string
	AuthPassword                  string
	AuthSessionSecret             string
	AuthSessionTTL                time.Duration
}

// Load builds a Config from environment variables in server mode.
func Load() *Config {
	return LoadWithMode(ModeServer)
}

// LoadWithMode builds a Config from environment variables, applying
// mode-specific defaults where appropriate.
func LoadWithMode(mode RuntimeMode) *Config {
	// Resolve defaults that differ by mode.
	defaultPort := "3001"
	defaultDBPath := "./openvibely.db"
	defaultRepoRoot := "./repos"
	defaultEnableLocalRepo := false

	if mode == ModeDesktop {
		// Desktop mode: use OS app-data directory for writable storage.
		dataDir := desktopDataDir()
		defaultDBPath = filepath.Join(dataDir, "openvibely.db")
		defaultRepoRoot = filepath.Join(dataDir, "repos")
		// Ephemeral port — let OS pick; 0 means the server will bind to a random free port.
		defaultPort = "0"
		// Desktop users always have access to local paths.
		defaultEnableLocalRepo = true
	}

	enableLocalRepo := ResolveEnableLocalRepoPath(os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH"))
	if mode == ModeDesktop && os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH") == "" {
		enableLocalRepo = defaultEnableLocalRepo
	}

	return &Config{
		Mode:                          mode,
		Port:                          getEnv("PORT", defaultPort),
		DatabasePath:                  getEnv("DATABASE_PATH", defaultDBPath),
		DatabaseURL:                   getEnv("DATABASE_URL", ""),
		AnthropicKey:                  getEnv("ANTHROPIC_API_KEY", ""),
		TelegramToken:                 getEnv("TELEGRAM_BOT_TOKEN", ""),
		Environment:                   getEnv("ENVIRONMENT", "development"),
		GitHubAppID:                   getEnv("GITHUB_APP_ID", ""),
		GitHubAppSlug:                 getEnv("GITHUB_APP_SLUG", ""),
		GitHubAppPrivateKey:           getEnv("GITHUB_APP_PRIVATE_KEY", ""),
		SlackClientID:                 getEnv("SLACK_CLIENT_ID", ""),
		SlackClientSecret:             getEnv("SLACK_CLIENT_SECRET", ""),
		SlackAppToken:                 getEnv("SLACK_APP_TOKEN", ""),
		SlackBotToken:                 getEnv("SLACK_BOT_TOKEN", ""),
		AppBaseURL:                    ResolveAppBaseURL(getEnv("APP_BASE_URL", "")),
		ProjectRepoRoot:               getEnv("PROJECT_REPO_ROOT", defaultRepoRoot),
		EnableLocalRepoPath:           enableLocalRepo,
		EnableTaskChangesMergeOptions: ResolveEnableTaskChangesMergeOptions(os.Getenv("OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS")),
		AuthEnabled:                   ResolveAuthEnabled(os.Getenv("AUTH_ENABLED"), os.Getenv("AUTH_USERNAME"), os.Getenv("AUTH_PASSWORD")),
		AuthUsername:                  getEnv("AUTH_USERNAME", ""),
		AuthPassword:                  getEnv("AUTH_PASSWORD", ""),
		AuthSessionSecret:             getEnv("AUTH_SESSION_SECRET", ""),
		AuthSessionTTL:                ResolveAuthSessionTTL(getEnv("AUTH_SESSION_TTL", "")),
	}
}

// desktopDataDir returns the OS-conventional app-data directory for OpenVibely
// desktop mode.  The directory is created if it does not exist.
func desktopDataDir() string {
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "OpenVibely")
	case "windows":
		appData := os.Getenv("LOCALAPPDATA")
		if appData == "" {
			appData = os.Getenv("APPDATA")
		}
		if appData == "" {
			appData = "."
		}
		base = filepath.Join(appData, "OpenVibely")
	default: // linux, *bsd, etc.
		xdg := os.Getenv("XDG_DATA_HOME")
		if xdg == "" {
			xdg = filepath.Join(os.Getenv("HOME"), ".local", "share")
		}
		base = filepath.Join(xdg, "openvibely")
	}
	_ = os.MkdirAll(base, 0o755)
	return base
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ResolveEnableLocalRepoPath resolves local repository-path enablement from
// OPENVIBELY_ENABLE_LOCAL_REPO_PATH only.
// Unset or invalid values default to false.
func ResolveEnableLocalRepoPath(explicitValue string) bool {
	if v, ok := parseEnvBool(explicitValue); ok {
		return v
	}
	return false
}

// ResolveEnableTaskChangesMergeOptions resolves merge-options visibility in the
// Task Changes tab from OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS only.
// Unset or invalid values default to false.
func ResolveEnableTaskChangesMergeOptions(explicitValue string) bool {
	if v, ok := parseEnvBool(explicitValue); ok {
		return v
	}
	return false
}

func parseEnvBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// ResolveAuthEnabled resolves auth enablement from AUTH_ENABLED, or infers it
// from AUTH_USERNAME/AUTH_PASSWORD presence when AUTH_ENABLED is unset.
func ResolveAuthEnabled(explicitValue, username, password string) bool {
	if v, ok := parseEnvBool(explicitValue); ok {
		return v
	}
	return strings.TrimSpace(username) != "" && strings.TrimSpace(password) != ""
}

// ResolveAuthSessionTTL parses AUTH_SESSION_TTL and falls back to 24h on empty/invalid.
func ResolveAuthSessionTTL(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

// ResolveAppBaseURL normalizes APP_BASE_URL for absolute URL use.
// Invalid values return empty string so callers can fall back to request-derived URLs.
func ResolveAppBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}

	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

// ValidateAppBaseURL returns a detailed error for invalid APP_BASE_URL values.
func ValidateAppBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	normalized := ResolveAppBaseURL(trimmed)
	if normalized == "" {
		return fmt.Errorf("APP_BASE_URL must be an absolute http(s) URL without query/fragment/userinfo, got %q", trimmed)
	}
	return nil
}
