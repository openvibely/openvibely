package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
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

func Load() *Config {
	return &Config{
		Port:                          getEnv("PORT", "3001"),
		DatabasePath:                  getEnv("DATABASE_PATH", "./openvibely.db"),
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
		ProjectRepoRoot:               getEnv("PROJECT_REPO_ROOT", "./repos"),
		EnableLocalRepoPath:           ResolveEnableLocalRepoPath(os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH")),
		EnableTaskChangesMergeOptions: ResolveEnableTaskChangesMergeOptions(os.Getenv("OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS")),
		AuthEnabled:                   ResolveAuthEnabled(os.Getenv("AUTH_ENABLED"), os.Getenv("AUTH_USERNAME"), os.Getenv("AUTH_PASSWORD")),
		AuthUsername:                  getEnv("AUTH_USERNAME", ""),
		AuthPassword:                  getEnv("AUTH_PASSWORD", ""),
		AuthSessionSecret:             getEnv("AUTH_SESSION_SECRET", ""),
		AuthSessionTTL:                ResolveAuthSessionTTL(getEnv("AUTH_SESSION_TTL", "")),
	}
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
