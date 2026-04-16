package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCookieName = "ov_session"
	DefaultSessionTTL = 24 * time.Hour
)

var (
	ErrInvalidToken = errors.New("invalid session token")
	ErrExpiredToken = errors.New("expired session token")
)

type Config struct {
	Enabled       bool
	Username      string
	Password      string
	SessionSecret string
	SessionTTL    time.Duration
	CookieName    string
}

type User struct {
	Username string `json:"username"`
	Display  string `json:"display"`
}

func (c Config) Normalized() Config {
	cfg := c
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.SessionSecret = strings.TrimSpace(cfg.SessionSecret)
	if cfg.CookieName == "" {
		cfg.CookieName = DefaultCookieName
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	return cfg
}

func (c Config) Validate() error {
	cfg := c.Normalized()
	if !cfg.Enabled {
		return nil
	}
	if cfg.Username == "" {
		return errors.New("AUTH_USERNAME is required when auth is enabled")
	}
	if cfg.Password == "" {
		return errors.New("AUTH_PASSWORD is required when auth is enabled")
	}
	if cfg.SessionSecret == "" {
		return errors.New("AUTH_SESSION_SECRET is required when auth is enabled")
	}
	if cfg.SessionTTL <= 0 {
		return errors.New("AUTH_SESSION_TTL must be greater than zero")
	}
	return nil
}

func (c Config) ValidateCredentials(username, password string) bool {
	cfg := c.Normalized()
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(cfg.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.Password)) == 1
	return userOK && passOK
}

func (c Config) SignToken(now time.Time) (string, error) {
	cfg := c.Normalized()
	if cfg.Username == "" || cfg.SessionSecret == "" {
		return "", errors.New("username and session secret are required")
	}
	expiresAt := now.Add(cfg.SessionTTL).Unix()
	payload := cfg.Username + ":" + strconv.FormatInt(expiresAt, 10)
	sig := sign(payload, cfg.SessionSecret)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c Config) VerifyToken(token string, now time.Time) (*User, error) {
	cfg := c.Normalized()
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidToken
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}

	payload := string(payloadBytes)
	expectedSig := sign(payload, cfg.SessionSecret)
	if subtle.ConstantTimeCompare(sigBytes, expectedSig) != 1 {
		return nil, ErrInvalidToken
	}

	payloadParts := strings.Split(payload, ":")
	if len(payloadParts) != 2 {
		return nil, ErrInvalidToken
	}

	expUnix, err := strconv.ParseInt(payloadParts[1], 10, 64)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if now.Unix() > expUnix {
		return nil, ErrExpiredToken
	}

	username := strings.TrimSpace(payloadParts[0])
	if username == "" {
		return nil, ErrInvalidToken
	}

	return &User{Username: username, Display: username}, nil
}

func (c Config) SessionCookie(token string, secure bool) *http.Cookie {
	cfg := c.Normalized()
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   int(cfg.SessionTTL.Seconds()),
	}
}

func (c Config) ClearSessionCookie(secure bool) *http.Cookie {
	cfg := c.Normalized()
	return &http.Cookie{
		Name:     cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
}

func UserFromRequest(r *http.Request, cfg Config, now time.Time) (*User, error) {
	ncfg := cfg.Normalized()
	cookie, err := r.Cookie(ncfg.CookieName)
	if err != nil {
		return nil, err
	}
	if cookie.Value == "" {
		return nil, ErrInvalidToken
	}
	return ncfg.VerifyToken(cookie.Value, now)
}

func sign(payload, secret string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func RedirectURL(next string) string {
	next = sanitizeNext(next)
	return "/login?next=" + base64.RawURLEncoding.EncodeToString([]byte(next))
}

func DecodeNext(encoded string) (string, error) {
	if strings.TrimSpace(encoded) == "" {
		return "/", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid next value: %w", err)
	}
	return sanitizeNext(string(b)), nil
}

func sanitizeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}
