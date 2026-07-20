package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/auth"
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
	Mode                     RuntimeMode
	Port                     string
	DatabasePath             string
	DatabaseURL              string
	AnthropicKey             string
	TelegramToken            string
	DiscordToken             string
	Environment              string
	EnvironmentExplicitlySet bool
	GitHubAppID              string
	GitHubAppSlug            string
	GitHubAppPrivateKey      string
	SlackClientID            string
	SlackClientSecret        string
	SlackAppToken            string
	SlackBotToken            string
	AppBaseURL               string
	ProjectRepoRoot          string
	AppDataDir               string
	EnableLocalRepoPath      bool
	AuthEnabled              bool
	AuthMode                 auth.AuthMode
	AuthUsername             string
	AuthPassword             string
	AuthSessionSecret        string
	AuthSessionTTL           time.Duration
	HostedSSOEnabled         bool
	HostedSSORequested       bool
	HostedSSOControlURL      string
	HostedSSOInstanceID      string
	HostedSSOKey             []byte
	hostedSSOSwitchSet       bool
	hostedSSOSwitchOK        bool
	environmentInput         string
	environmentInputRead     bool
}

// Load builds a Config from environment variables in server mode.
func Load() *Config {
	return LoadWithMode(ModeServer)
}

// LoadWithMode builds a Config from environment variables, applying
// mode-specific defaults where appropriate.
func LoadWithMode(mode RuntimeMode) *Config {
	defaults := defaultsForMode(mode)

	appDataDir := getEnv("OPENVIBELY_APP_DATA_DIR", defaults.AppDataDir)
	defaultDBPath := filepath.Join(appDataDir, "openvibely.db")
	defaultRepoRoot := filepath.Join(appDataDir, "repos")

	enableLocalRepo := ResolveEnableLocalRepoPath(os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH"))
	if mode == ModeDesktop && os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH") == "" {
		enableLocalRepo = defaults.EnableLocalRepoPath
	}

	environmentRaw, environmentSet := os.LookupEnv("ENVIRONMENT")
	hostedRaw, hostedSet := os.LookupEnv("OPENVIBELY_HOSTED_SSO_ENABLED")
	hostedEnabled, hostedOK := parseStrictBool(hostedRaw)
	if !hostedSet {
		hostedOK = true
	}
	localAuthEnabled := ResolveAuthEnabled(os.Getenv("AUTH_ENABLED"), os.Getenv("AUTH_USERNAME"), os.Getenv("AUTH_PASSWORD"))
	authMode := auth.AuthModeDisabled
	if mode == ModeServer && hostedSet && hostedOK && hostedEnabled {
		authMode = auth.AuthModeHostedSSO
	} else if localAuthEnabled {
		authMode = auth.AuthModeLocal
	}
	rawAppBaseURL := getEnv("APP_BASE_URL", "")
	appBaseURL := ResolveAppBaseURL(rawAppBaseURL)
	if authMode == auth.AuthModeHostedSSO {
		appBaseURL = rawAppBaseURL
	}

	return (&Config{
		Mode:                     mode,
		Port:                     getEnv("PORT", defaults.Port),
		DatabasePath:             getEnv("DATABASE_PATH", defaultDBPath),
		DatabaseURL:              getEnv("DATABASE_URL", ""),
		AnthropicKey:             getEnv("ANTHROPIC_API_KEY", ""),
		TelegramToken:            getEnv("TELEGRAM_BOT_TOKEN", ""),
		DiscordToken:             getEnv("DISCORD_BOT_TOKEN", ""),
		Environment:              getEnv("ENVIRONMENT", "development"),
		EnvironmentExplicitlySet: environmentSet,
		GitHubAppID:              getEnv("GITHUB_APP_ID", ""),
		GitHubAppSlug:            getEnv("GITHUB_APP_SLUG", ""),
		GitHubAppPrivateKey:      getEnv("GITHUB_APP_PRIVATE_KEY", ""),
		SlackClientID:            getEnv("SLACK_CLIENT_ID", ""),
		SlackClientSecret:        getEnv("SLACK_CLIENT_SECRET", ""),
		SlackAppToken:            getEnv("SLACK_APP_TOKEN", ""),
		SlackBotToken:            getEnv("SLACK_BOT_TOKEN", ""),
		AppBaseURL:               appBaseURL,
		ProjectRepoRoot:          getEnv("PROJECT_REPO_ROOT", defaultRepoRoot),
		AppDataDir:               appDataDir,
		EnableLocalRepoPath:      enableLocalRepo,
		AuthEnabled:              localAuthEnabled,
		AuthMode:                 authMode,
		AuthUsername:             getEnv("AUTH_USERNAME", ""),
		AuthPassword:             getEnv("AUTH_PASSWORD", ""),
		AuthSessionSecret:        getEnv("AUTH_SESSION_SECRET", ""),
		AuthSessionTTL:           ResolveAuthSessionTTL(getEnv("AUTH_SESSION_TTL", "")),
		HostedSSOEnabled:         authMode == auth.AuthModeHostedSSO,
		HostedSSORequested:       hostedSet && hostedOK && hostedEnabled,
		HostedSSOControlURL:      getEnv("OPENVIBELY_HOSTED_CONTROL_URL", ""),
		HostedSSOInstanceID:      getEnv("OPENVIBELY_HOSTED_INSTANCE_ID", ""),
		hostedSSOSwitchSet:       hostedSet,
		hostedSSOSwitchOK:        hostedOK,
		environmentInput:         environmentRaw,
		environmentInputRead:     true,
	}).NormalizeForMode()
}

// NormalizeForMode fills mode-specific storage defaults on partially constructed
// configs. Entry points should normally use LoadWithMode, but server.Start also
// calls this so tests and embedded callers cannot accidentally run desktop mode
// with project-relative database, repo, or global agent storage paths.
func (c *Config) NormalizeForMode() *Config {
	if c == nil {
		return nil
	}
	if c.Mode == "" {
		c.Mode = ModeServer
	}
	defaults := defaultsForMode(c.Mode)
	if c.Port == "" {
		c.Port = defaults.Port
	}
	if c.AppDataDir == "" {
		c.AppDataDir = defaults.AppDataDir
	}
	if c.AppDataDir != "" {
		_ = os.MkdirAll(c.AppDataDir, 0o755)
	}
	if c.DatabasePath == "" {
		c.DatabasePath = filepath.Join(c.AppDataDir, "openvibely.db")
	}
	if c.ProjectRepoRoot == "" {
		c.ProjectRepoRoot = filepath.Join(c.AppDataDir, "repos")
	}
	if c.Mode == ModeDesktop && os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH") == "" {
		c.EnableLocalRepoPath = true
	}
	return c
}

type modeDefaults struct {
	Port                string
	AppDataDir          string
	DatabasePath        string
	ProjectRepoRoot     string
	EnableLocalRepoPath bool
}

func defaultsForMode(mode RuntimeMode) modeDefaults {
	appDataDir := serverDataDir()
	defaults := modeDefaults{
		Port:            "3001",
		AppDataDir:      appDataDir,
		DatabasePath:    filepath.Join(appDataDir, "openvibely.db"),
		ProjectRepoRoot: filepath.Join(appDataDir, "repos"),
	}
	if mode == ModeDesktop {
		defaults.Port = "0"
		defaults.EnableLocalRepoPath = true
	}
	return defaults
}

// serverDataDir returns the default app-owned storage directory for web/server
// mode. This is where the default database, managed repos, global agents,
// global skills, and other app-owned config live by default.
func serverDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	base := filepath.Join(home, ".openvibely")
	_ = os.MkdirAll(base, 0o755)
	return base
}

// desktopDataDir returns the OS-conventional desktop config directory for
// OpenVibely. Runtime storage defaults intentionally use serverDataDir for both
// web/server and desktop modes so both apps share the same DB unless explicitly
// configured otherwise.
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

func parseStrictBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// Validate checks startup configuration, including hosted SSO's fail-closed
// security contract. It also decodes the hosted HMAC key exactly once.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("configuration is nil")
	}
	if c.hostedSSOSwitchSet && !c.hostedSSOSwitchOK {
		return errors.New("OPENVIBELY_HOSTED_SSO_ENABLED must be exactly true or false")
	}
	if c.Mode == ModeDesktop && (c.HostedSSORequested || c.HostedSSOEnabled) {
		return errors.New("hosted SSO is not supported in desktop mode")
	}
	if c.AuthMode == "" {
		switch {
		case c.HostedSSOEnabled:
			c.AuthMode = auth.AuthModeHostedSSO
		case c.AuthEnabled:
			c.AuthMode = auth.AuthModeLocal
		default:
			c.AuthMode = auth.AuthModeDisabled
		}
	}
	if c.AuthMode != auth.AuthModeHostedSSO {
		return nil
	}
	c.HostedSSOEnabled = true
	if err := validateIdentifier(c.HostedSSOInstanceID, 128, "OPENVIBELY_HOSTED_INSTANCE_ID"); err != nil {
		return err
	}
	environment := c.Environment
	if c.environmentInputRead {
		environment = c.environmentInput
	}
	control, err := validateHostedOrigin(c.HostedSSOControlURL, environment, c.EnvironmentExplicitlySet)
	if err != nil {
		return fmt.Errorf("OPENVIBELY_HOSTED_CONTROL_URL: %w", err)
	}
	application, err := validateHostedOrigin(c.AppBaseURL, environment, c.EnvironmentExplicitlySet)
	if err != nil {
		return fmt.Errorf("APP_BASE_URL: %w", err)
	}
	if control != c.HostedSSOControlURL || application != c.AppBaseURL {
		return errors.New("hosted SSO origins must already use canonical serialization")
	}
	key, err := decodeHostedSecret(c.AuthSessionSecret)
	if err != nil {
		return fmt.Errorf("AUTH_SESSION_SECRET: %w", err)
	}
	c.HostedSSOControlURL = control
	c.AppBaseURL = application
	c.HostedSSOKey = key
	return nil
}

func validateIdentifier(value string, max int, name string) error {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be canonical valid UTF-8 between 1 and %d bytes", name, max)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return fmt.Errorf("%s contains an ASCII control character", name)
		}
	}
	return nil
}

func decodeHostedSecret(value string) ([]byte, error) {
	if len(value) != 43 || !isASCII(value) {
		return nil, errors.New("must be canonical unpadded base64url for exactly 32 bytes")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("must be canonical unpadded base64url for exactly 32 bytes")
	}
	return decoded, nil
}

func validateHostedOrigin(raw, environment string, environmentExplicit bool) (string, error) {
	if len(raw) == 0 || len(raw) > 2048 || !isASCII(raw) {
		return "", errors.New("must contain 1 through 2048 ASCII bytes")
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.Host == "" {
		return "", errors.New("must be an absolute HTTP(S) origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Path != "" || u.RawPath != "" {
		return "", errors.New("must be an origin without userinfo, path, query, or fragment")
	}
	hostname := u.Hostname()
	if hostname == "" || strings.Contains(hostname, "%") {
		return "", errors.New("invalid hostname")
	}
	portText, explicitPort, err := splitCanonicalPort(u.Host)
	if err != nil {
		return "", err
	}
	if explicitPort {
		if portText == "" || (len(portText) > 1 && portText[0] == '0') {
			return "", errors.New("port must be canonical decimal between 1 and 65535")
		}
		for i := range portText {
			if portText[i] < '0' || portText[i] > '9' {
				return "", errors.New("port must be canonical decimal between 1 and 65535")
			}
		}
		port, parseErr := strconv.Atoi(portText)
		if parseErr != nil || port < 1 || port > 65535 {
			return "", errors.New("port must be canonical decimal between 1 and 65535")
		}
		if (scheme == "https" && port == 443) || (scheme == "http" && port == 80) {
			return "", errors.New("explicit default port is not canonical")
		}
	}
	canonicalHost, isLoopback, err := canonicalHostedHostname(hostname)
	if err != nil {
		return "", err
	}
	if scheme == "http" && (!environmentExplicit || !strings.EqualFold(strings.TrimSpace(environment), "development") || !isLoopback) {
		return "", errors.New("http is allowed only for explicit development loopback origins")
	}
	host := canonicalHost
	if explicitPort {
		host = net.JoinHostPort(canonicalHost, portText)
	} else if strings.Contains(canonicalHost, ":") {
		host = "[" + canonicalHost + "]"
	}
	canonical := scheme + "://" + host
	if canonical != raw {
		return "", errors.New("origin is not canonically serialized")
	}
	return canonical, nil
}

func splitCanonicalPort(host string) (string, bool, error) {
	if strings.HasPrefix(host, "[") {
		end := strings.LastIndexByte(host, ']')
		if end < 0 {
			return "", false, errors.New("malformed IP literal")
		}
		rest := host[end+1:]
		if rest == "" {
			return "", false, nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", false, errors.New("malformed host")
		}
		return rest[1:], true, nil
	}
	if strings.Count(host, ":") == 0 {
		return "", false, nil
	}
	if strings.Count(host, ":") != 1 {
		return "", false, errors.New("IP literals must use canonical brackets")
	}
	idx := strings.LastIndexByte(host, ':')
	return host[idx+1:], true, nil
}

func canonicalHostedHostname(hostname string) (string, bool, error) {
	if ip := net.ParseIP(hostname); ip != nil {
		canonical := ip.String()
		return canonical, ip.IsLoopback(), nil
	}
	if looksLikeMalformedIPv4(hostname) {
		return "", false, errors.New("malformed IP literal")
	}
	if len(hostname) == 0 || len(hostname) > 253 || !isASCII(hostname) || strings.HasSuffix(hostname, ".") {
		return "", false, errors.New("invalid DNS hostname")
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false, errors.New("invalid DNS hostname label")
		}
		for i := range label {
			b := label[i]
			if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-') {
				return "", false, errors.New("invalid DNS hostname character")
			}
		}
	}
	canonical := strings.ToLower(hostname)
	return canonical, strings.EqualFold(canonical, "localhost"), nil
}

func looksLikeMalformedIPv4(hostname string) bool {
	if !strings.Contains(hostname, ".") {
		return false
	}
	for i := range hostname {
		if (hostname[i] < '0' || hostname[i] > '9') && hostname[i] != '.' {
			return false
		}
	}
	return true
}

func isASCII(value string) bool {
	for i := range value {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
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
