package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
)

func hostedSecretFixture() string {
	return base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

func clearHostedEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OPENVIBELY_HOSTED_SSO_ENABLED", "OPENVIBELY_HOSTED_CONTROL_URL",
		"OPENVIBELY_HOSTED_INSTANCE_ID", "APP_BASE_URL", "AUTH_SESSION_SECRET",
		"AUTH_ENABLED", "AUTH_USERNAME", "AUTH_PASSWORD", "ENVIRONMENT",
	} {
		value, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, value)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func TestHostedSSOModePrecedenceAndStrictSwitch(t *testing.T) {
	clearHostedEnv(t)
	t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", " TrUe ")
	t.Setenv("OPENVIBELY_HOSTED_CONTROL_URL", "https://openvibely.ai")
	t.Setenv("OPENVIBELY_HOSTED_INSTANCE_ID", "instance-1")
	t.Setenv("APP_BASE_URL", "https://alice.openvibely.ai")
	t.Setenv("AUTH_SESSION_SECRET", hostedSecretFixture())
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("AUTH_USERNAME", "legacy")
	t.Setenv("AUTH_PASSWORD", "legacy-password")

	cfg := LoadWithMode(ModeServer)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.AuthMode != auth.AuthModeHostedSSO || !cfg.HostedSSOEnabled {
		t.Fatalf("mode=%q enabled=%v", cfg.AuthMode, cfg.HostedSSOEnabled)
	}
	if len(cfg.HostedSSOKey) != 32 {
		t.Fatalf("decoded hosted key length=%d", len(cfg.HostedSSOKey))
	}

	for _, value := range []string{"", "tru", "1", "yes", "on"} {
		t.Run("invalid_"+value, func(t *testing.T) {
			clearHostedEnv(t)
			t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", value)
			t.Setenv("AUTH_USERNAME", "legacy")
			t.Setenv("AUTH_PASSWORD", "legacy-password")
			cfg := LoadWithMode(ModeServer)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected present value %q to fail closed", value)
			}
		})
	}
}

func TestHostedSSODesktopRejectedAndLocalCompatibilityPreserved(t *testing.T) {
	clearHostedEnv(t)
	t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", "true")
	if err := LoadWithMode(ModeDesktop).Validate(); err == nil {
		t.Fatal("expected hosted SSO desktop configuration to fail")
	}

	clearHostedEnv(t)
	t.Setenv("AUTH_USERNAME", "admin")
	t.Setenv("AUTH_PASSWORD", "password")
	cfg := LoadWithMode(ModeServer)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("local validation: %v", err)
	}
	if cfg.AuthMode != auth.AuthModeLocal || !cfg.AuthEnabled {
		t.Fatalf("expected inferred local mode, got %q", cfg.AuthMode)
	}
}

func TestHostedSSODesktopRejectsDirectAuthMode(t *testing.T) {
	cfg := Config{
		Mode:                     ModeDesktop,
		AuthMode:                 auth.AuthModeHostedSSO,
		HostedSSOControlURL:      "https://openvibely.ai",
		HostedSSOInstanceID:      "instance-1",
		AppBaseURL:               "https://alice.openvibely.ai",
		AuthSessionSecret:        hostedSecretFixture(),
		Environment:              "production",
		EnvironmentExplicitlySet: true,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "desktop mode") {
		t.Fatalf("Validate error=%v, want desktop hosted SSO rejection", err)
	}
}

func TestHostedSSOCanonicalConfiguration(t *testing.T) {
	valid := Config{
		Mode:                     ModeServer,
		AuthMode:                 auth.AuthModeHostedSSO,
		HostedSSOEnabled:         true,
		HostedSSOControlURL:      "https://openvibely.ai:8443",
		HostedSSOInstanceID:      "instance-1",
		AppBaseURL:               "https://alice.openvibely.ai:9443",
		AuthSessionSecret:        hostedSecretFixture(),
		Environment:              "production",
		EnvironmentExplicitlySet: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid hosted config rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"control path", func(c *Config) { c.HostedSSOControlURL += "/control" }},
		{"app slash", func(c *Config) { c.AppBaseURL += "/" }},
		{"uppercase host", func(c *Config) { c.AppBaseURL = "https://Alice.openvibely.ai:9443" }},
		{"default port", func(c *Config) { c.AppBaseURL = "https://alice.openvibely.ai:443" }},
		{"userinfo", func(c *Config) { c.HostedSSOControlURL = "https://u@openvibely.ai" }},
		{"query", func(c *Config) { c.AppBaseURL += "?x=1" }},
		{"bad host label", func(c *Config) { c.AppBaseURL = "https://-alice.openvibely.ai" }},
		{"zone", func(c *Config) { c.AppBaseURL = "https://[fe80::1%25lo0]:9443" }},
		{"instance whitespace", func(c *Config) { c.HostedSSOInstanceID = " instance-1" }},
		{"instance control", func(c *Config) { c.HostedSSOInstanceID = "instance\n1" }},
		{"instance too long", func(c *Config) { c.HostedSSOInstanceID = strings.Repeat("x", 129) }},
		{"short secret", func(c *Config) { c.AuthSessionSecret = base64.RawURLEncoding.EncodeToString([]byte("short")) }},
		{"padded secret", func(c *Config) { c.AuthSessionSecret = hostedSecretFixture() + "=" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestHostedSSOSwitchModeMatrix(t *testing.T) {
	for _, tt := range []struct {
		name        string
		switchSet   bool
		switchValue string
		authEnabled string
		username    string
		password    string
		wantMode    auth.AuthMode
		wantError   bool
	}{
		{name: "unset disabled", wantMode: auth.AuthModeDisabled},
		{name: "unset inferred local", username: "user", password: "pass", wantMode: auth.AuthModeLocal},
		{name: "explicit false local", switchSet: true, switchValue: " FaLsE ", username: "user", password: "pass", wantMode: auth.AuthModeLocal},
		{name: "explicit auth false", authEnabled: "false", username: "user", password: "pass", wantMode: auth.AuthModeDisabled},
		{name: "legacy invalid auth infers", authEnabled: "invalid", username: "user", password: "pass", wantMode: auth.AuthModeLocal},
		{name: "empty fails closed", switchSet: true, switchValue: "", username: "user", password: "pass", wantMode: auth.AuthModeLocal, wantError: true},
		{name: "numeric fails closed", switchSet: true, switchValue: "1", username: "user", password: "pass", wantMode: auth.AuthModeLocal, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearHostedEnv(t)
			if tt.switchSet {
				t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", tt.switchValue)
			}
			if tt.authEnabled != "" {
				t.Setenv("AUTH_ENABLED", tt.authEnabled)
			}
			if tt.username != "" {
				t.Setenv("AUTH_USERNAME", tt.username)
				t.Setenv("AUTH_PASSWORD", tt.password)
			}
			cfg := LoadWithMode(ModeServer)
			if cfg.AuthMode != tt.wantMode {
				t.Fatalf("mode=%q want=%q", cfg.AuthMode, tt.wantMode)
			}
			err := cfg.Validate()
			if (err != nil) != tt.wantError {
				t.Fatalf("Validate error=%v wantError=%v", err, tt.wantError)
			}
		})
	}
}

func TestHostedSSORejectsInvalidOriginComponents(t *testing.T) {
	base := Config{
		Mode: ModeServer, AuthMode: auth.AuthModeHostedSSO, HostedSSOEnabled: true,
		HostedSSOControlURL: "https://openvibely.ai", HostedSSOInstanceID: "instance-1",
		AppBaseURL: "https://alice.openvibely.ai", AuthSessionSecret: hostedSecretFixture(),
		Environment: "production", EnvironmentExplicitlySet: true,
	}
	longLabel := strings.Repeat("a", 64) + ".example.com"
	longHost := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62)
	for _, origin := range []string{
		"", "https://éxample.com", "https://a..example.com", "https://" + longLabel,
		"https://" + longHost, "https://example.com.", "https://-example.com", "https://example-.com",
		"https://999.999.999.999", "https://[fe80::1%25lo0]", "https://example.com:",
		"https://example.com:+1", "https://example.com:0", "https://example.com:65536",
		"https://example.com:0443", "https://example.com:abc", "https://example.com:443",
		"https://user@example.com", "https://example.com/path", "https://example.com?x=1", "https://example.com#fragment",
		"https://" + strings.Repeat("a", 2040),
	} {
		cfg := base
		cfg.HostedSSOControlURL = origin
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted invalid origin %q", origin)
		}
	}
	for _, origin := range []string{"https://openvibely.ai:8443", "https://127.0.0.1:8443", "https://[::1]:8443"} {
		cfg := base
		cfg.HostedSSOControlURL = origin
		if err := cfg.Validate(); err != nil {
			t.Fatalf("rejected canonical origin %q: %v", origin, err)
		}
	}
}

func TestHostedSSOExplicitEmptyEnvironmentDoesNotPermitHTTP(t *testing.T) {
	clearHostedEnv(t)
	t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", "true")
	t.Setenv("OPENVIBELY_HOSTED_CONTROL_URL", "http://localhost:3002")
	t.Setenv("OPENVIBELY_HOSTED_INSTANCE_ID", "instance-1")
	t.Setenv("APP_BASE_URL", "http://127.0.0.1:3001")
	t.Setenv("AUTH_SESSION_SECRET", hostedSecretFixture())
	t.Setenv("ENVIRONMENT", "")
	if err := LoadWithMode(ModeServer).Validate(); err == nil {
		t.Fatal("explicit empty ENVIRONMENT permitted hosted HTTP")
	}
}

func TestHostedSSODesktopSwitchMatrix(t *testing.T) {
	for _, tt := range []struct {
		name      string
		set       bool
		value     string
		wantError bool
	}{
		{name: "unset"},
		{name: "explicit false", set: true, value: " FaLsE "},
		{name: "explicit true", set: true, value: "true", wantError: true},
		{name: "invalid", set: true, value: "yes", wantError: true},
		{name: "present empty", set: true, value: "", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearHostedEnv(t)
			if tt.set {
				t.Setenv("OPENVIBELY_HOSTED_SSO_ENABLED", tt.value)
			}
			cfg := LoadWithMode(ModeDesktop)
			err := cfg.Validate()
			if (err != nil) != tt.wantError {
				t.Fatalf("Validate error=%v wantError=%v", err, tt.wantError)
			}
			if !tt.wantError && cfg.AuthMode == auth.AuthModeHostedSSO {
				t.Fatal("desktop selected hosted SSO")
			}
		})
	}
}

func TestHostedSSORequiredValuesAndByteBoundaries(t *testing.T) {
	valid := Config{
		Mode: ModeServer, AuthMode: auth.AuthModeHostedSSO, HostedSSOEnabled: true,
		HostedSSOControlURL: "https://openvibely.ai", HostedSSOInstanceID: "instance-1",
		AppBaseURL: "https://alice.openvibely.ai", AuthSessionSecret: hostedSecretFixture(),
		Environment: "production", EnvironmentExplicitlySet: true,
	}
	for name, mutate := range map[string]func(*Config){
		"control URL":     func(c *Config) { c.HostedSSOControlURL = "" },
		"instance ID":     func(c *Config) { c.HostedSSOInstanceID = "" },
		"application URL": func(c *Config) { c.AppBaseURL = "" },
		"session secret":  func(c *Config) { c.AuthSessionSecret = "" },
	} {
		t.Run("missing "+name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("missing required hosted value accepted")
			}
		})
	}

	for _, instanceID := range []string{strings.Repeat("i", 128), strings.Repeat("é", 64)} {
		cfg := valid
		cfg.HostedSSOInstanceID = instanceID
		if err := cfg.Validate(); err != nil {
			t.Fatalf("128-byte instance ID rejected: %v", err)
		}
	}
	for name, instanceID := range map[string]string{
		"invalid UTF-8":       string([]byte{0xff}),
		"ASCII control":       "instance\n1",
		"leading space":       " instance-1",
		"trailing space":      "instance-1 ",
		"129 ASCII bytes":     strings.Repeat("i", 129),
		"130 multibyte bytes": strings.Repeat("é", 65),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			cfg.HostedSSOInstanceID = instanceID
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid instance ID accepted")
			}
		})
	}
}

func TestHostedSSOSecretCanonicalMatrixInEveryEnvironment(t *testing.T) {
	canonical := hostedSecretFixture()
	noncanonical := canonical[:len(canonical)-1] + "B"
	for _, environment := range []string{"development", "production"} {
		t.Run(environment, func(t *testing.T) {
			base := Config{
				Mode: ModeServer, AuthMode: auth.AuthModeHostedSSO, HostedSSOEnabled: true,
				HostedSSOControlURL: "https://openvibely.ai", HostedSSOInstanceID: "instance-1",
				AppBaseURL: "https://alice.openvibely.ai", AuthSessionSecret: canonical,
				Environment: environment, EnvironmentExplicitlySet: true,
			}
			if err := base.Validate(); err != nil {
				t.Fatalf("canonical secret rejected: %v", err)
			}
			for name, secret := range map[string]string{
				"empty":                      "",
				"padded":                     canonical + "=",
				"malformed":                  strings.Repeat("!", 43),
				"noncanonical trailing bits": noncanonical,
				"shorter decoded key":        base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
				"longer decoded key":         base64.RawURLEncoding.EncodeToString(make([]byte, 33)),
			} {
				t.Run(name, func(t *testing.T) {
					cfg := base
					cfg.AuthSessionSecret = secret
					if err := cfg.Validate(); err == nil {
						t.Fatalf("invalid secret accepted: %q", secret)
					}
				})
			}
		})
	}
}

func TestSourceRunnerPreservesEnvironmentPresence(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "start.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	if !strings.Contains(script, `ENVIRONMENT_WAS_SET="${ENVIRONMENT+x}"`) {
		t.Fatal("start.sh must capture ENVIRONMENT presence before applying its local default")
	}
	if !strings.Contains(script, `if [ "$ENVIRONMENT_WAS_SET" = "x" ]; then export ENVIRONMENT; fi`) {
		t.Fatal("start.sh must export ENVIRONMENT only when it was explicitly present")
	}
	if strings.Contains(script, `if [ "${ENVIRONMENT+x}" = "x" ] && [ -n "${ENVIRONMENT:-}" ]; then export ENVIRONMENT; fi`) {
		t.Fatal("start.sh re-checks presence after assigning its default")
	}
}

func TestHostedSSOHTTPRequiresExplicitDevelopmentLoopback(t *testing.T) {
	base := Config{
		Mode: ModeServer, AuthMode: auth.AuthModeHostedSSO, HostedSSOEnabled: true,
		HostedSSOControlURL: "http://localhost:3002", HostedSSOInstanceID: "instance-1",
		AppBaseURL: "http://127.0.0.1:3001", AuthSessionSecret: hostedSecretFixture(),
		Environment: "development", EnvironmentExplicitlySet: true,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("explicit development loopback rejected: %v", err)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.EnvironmentExplicitlySet = false },
		func(c *Config) { c.Environment = "production" },
		func(c *Config) { c.AppBaseURL = "http://0.0.0.0:3001" },
		func(c *Config) { c.AppBaseURL = "http://app.localhost:3001" },
		func(c *Config) { c.HostedSSOControlURL = "http://192.168.1.2:3002" },
	} {
		cfg := base
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected HTTP configuration rejection: %#v", cfg)
		}
	}
}
