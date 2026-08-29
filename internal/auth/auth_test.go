package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Enabled:       true,
		Username:      "admin",
		Password:      "secret",
		SessionSecret: "signing-secret",
		SessionTTL:    time.Hour,
		CookieName:    "ov_session",
	}
}

func TestConfigValidate(t *testing.T) {
	cfg := testConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}

	cfg.Enabled = false
	cfg.Username = ""
	cfg.Password = ""
	cfg.SessionSecret = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled auth should not require credentials: %v", err)
	}
}

func TestConfigValidateRequiredFields(t *testing.T) {
	cfg := testConfig()

	cfg.Username = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing username to fail")
	}

	cfg = testConfig()
	cfg.Password = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing password to fail")
	}

	cfg = testConfig()
	cfg.SessionSecret = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing session secret to fail")
	}
}

func TestValidateCredentials(t *testing.T) {
	cfg := testConfig()
	if !cfg.ValidateCredentials("admin", "secret") {
		t.Fatal("expected credentials to validate")
	}
	if cfg.ValidateCredentials("admin", "wrong") {
		t.Fatal("expected wrong password to fail")
	}
	if cfg.ValidateCredentials("wrong", "secret") {
		t.Fatal("expected wrong username to fail")
	}
}

func TestSignVerifyToken(t *testing.T) {
	cfg := testConfig()
	now := time.Unix(1_700_000_000, 0)
	token, err := cfg.SignToken(now)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	user, err := cfg.VerifyToken(token, now.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("VerifyToken error: %v", err)
	}
	if user.Username != "admin" {
		t.Fatalf("expected username admin, got %q", user.Username)
	}
}

func TestVerifyTokenExpiresAtExactBoundary(t *testing.T) {
	cfg := testConfig()
	signTime := time.Unix(1_700_000_000, 123_456_789)
	token, err := cfg.SignToken(signTime)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	_, err = cfg.VerifyToken(token, signTime.Add(cfg.SessionTTL))
	if err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken at exact expiration boundary, got %v", err)
	}
}

func TestVerifyTokenValidImmediatelyBeforeBoundary(t *testing.T) {
	cfg := testConfig()
	signTime := time.Unix(1_700_000_000, 123_456_789)
	token, err := cfg.SignToken(signTime)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	user, err := cfg.VerifyToken(token, signTime.Add(cfg.SessionTTL).Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("expected token to remain valid immediately before expiration, got %v", err)
	}
	if user.Username != cfg.Username {
		t.Fatalf("expected username %q, got %q", cfg.Username, user.Username)
	}
}

func TestVerifyTokenAcceptsLegacySecondPrecisionToken(t *testing.T) {
	cfg := testConfig()
	expiresAt := int64(1_700_000_100)
	payload := cfg.Username + ":" + strconv.FormatInt(expiresAt, 10)
	token := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sign(payload, cfg.SessionSecret))

	if _, err := cfg.VerifyToken(token, time.Unix(expiresAt-1, 999_999_999)); err != nil {
		t.Fatalf("expected legacy token to remain valid before expiration, got %v", err)
	}
	if _, err := cfg.VerifyToken(token, time.Unix(expiresAt, 0)); err != ErrExpiredToken {
		t.Fatalf("expected legacy token to expire at its second boundary, got %v", err)
	}
}

func TestSubsecondSessionTTLExpiresAtConfiguredBoundary(t *testing.T) {
	cfg := testConfig()
	cfg.SessionTTL = 100 * time.Millisecond
	signTime := time.Unix(1_700_000_000, 900_000_000)
	token, err := cfg.SignToken(signTime)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: token})
	boundary := signTime.Add(cfg.SessionTTL)
	if _, err := UserFromRequest(req, cfg, boundary.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("expected subsecond token to remain valid immediately before expiration, got %v", err)
	}
	if _, err := UserFromRequest(req, cfg, boundary); err != ErrExpiredToken {
		t.Fatalf("expected subsecond token to expire at configured boundary, got %v", err)
	}
}

func TestVerifyTokenExpired(t *testing.T) {
	cfg := testConfig()
	now := time.Unix(1_700_000_000, 0)
	token, err := cfg.SignToken(now)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	_, err = cfg.VerifyToken(token, now.Add(2*time.Hour))
	if err == nil || err != ErrExpiredToken {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerifyTokenTampered(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	token, err := cfg.SignToken(now)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	tampered := token + "x"
	_, err = cfg.VerifyToken(tampered, now)
	if err == nil {
		t.Fatal("expected tampered token to fail")
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	cfg := testConfig()
	token, err := cfg.SignToken(time.Now())
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	cfg2 := cfg
	cfg2.SessionSecret = "different-secret"
	_, err = cfg2.VerifyToken(token, time.Now())
	if err == nil {
		t.Fatal("expected wrong secret verification to fail")
	}
}

func TestVerifyTokenMalformed(t *testing.T) {
	cfg := testConfig()
	_, err := cfg.VerifyToken("not-a-token", time.Now())
	if err == nil {
		t.Fatal("expected malformed token to fail")
	}
}

func TestCookieAttributes(t *testing.T) {
	cfg := testConfig()
	cookie := cfg.SessionCookie("token", true)
	if !cookie.HttpOnly {
		t.Fatal("expected HttpOnly cookie")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("expected SameSite=Lax, got %v", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Fatalf("expected path '/', got %q", cookie.Path)
	}
	if !cookie.Secure {
		t.Fatal("expected secure cookie")
	}
}

func TestClearSessionCookie(t *testing.T) {
	cfg := testConfig()
	cookie := cfg.ClearSessionCookie(false)
	if cookie.MaxAge != -1 {
		t.Fatalf("expected MaxAge=-1, got %d", cookie.MaxAge)
	}
	if cookie.Value != "" {
		t.Fatalf("expected empty value, got %q", cookie.Value)
	}
}

func TestUserFromRequest(t *testing.T) {
	cfg := testConfig()
	now := time.Now()
	token, err := cfg.SignToken(now)
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: token})

	user, err := UserFromRequest(req, cfg, now)
	if err != nil {
		t.Fatalf("UserFromRequest error: %v", err)
	}
	if user.Display != "admin" {
		t.Fatalf("expected display admin, got %q", user.Display)
	}
}

func TestRedirectURLAndDecodeNext(t *testing.T) {
	r := RedirectURL("/tasks?project_id=p1")
	if !strings.HasPrefix(r, "/login?next=") {
		t.Fatalf("unexpected redirect url: %s", r)
	}

	encoded := strings.TrimPrefix(r, "/login?next=")
	next, err := DecodeNext(encoded)
	if err != nil {
		t.Fatalf("DecodeNext error: %v", err)
	}
	if next != "/tasks?project_id=p1" {
		t.Fatalf("expected decoded next path, got %q", next)
	}
}

func TestDecodeNextRejectsInvalid(t *testing.T) {
	next, err := DecodeNext("!!!")
	if err == nil {
		t.Fatal("expected invalid next decode to fail")
	}
	if next != "" {
		t.Fatalf("expected empty next on error, got %q", next)
	}
}

func TestSanitizeNext(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "absolute URL", input: "https://evil.com", want: "/"},
		{name: "protocol-relative URL", input: "//evil", want: "/"},
		{name: "leading backslash", input: "\\attacker.example", want: "/"},
		{name: "double leading backslashes", input: "\\\\attacker.example", want: "/"},
		{name: "backslash authority form", input: "/\\attacker.example", want: "/"},
		{name: "double backslash authority form", input: "/\\\\attacker.example", want: "/"},
		{name: "mixed slash authority form", input: "/\\/attacker.example", want: "/"},
		{name: "encoded backslash authority form", input: "/%5Cattacker.example", want: "/"},
		{name: "encoded double backslash authority form", input: "/%5c%5cattacker.example", want: "/"},
		{name: "encoded mixed slash authority form", input: "/%5c/attacker.example", want: "/"},
		{name: "backslash in path", input: "/safe\\path", want: "/"},
		{name: "safe internal path", input: "/safe", want: "/safe"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeNext(tt.input); got != tt.want {
				t.Fatalf("sanitizeNext(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
