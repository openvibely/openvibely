package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/update"
)

func TestSystemHealthLoopbackSchema(t *testing.T) {
	e := echo.New()
	h := &Handler{}
	h.SetSystemHealth(buildinfo.Build{Version: "0.6.0", Commit: "abc", BuildTime: time.Unix(1, 0).UTC().Format(time.RFC3339), Artifact: buildinfo.ArtifactContainer}, buildinfo.ModeHosted, buildinfo.DistributionHosted, "token", "", 139, update.NewDrainManager(nil, nil, 0, time.Now))
	req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	if err := h.SystemHealth(e.NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body SystemHealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ready || body.Version != "0.6.0" || body.DatabaseSchema != 139 || body.Distribution != buildinfo.DistributionHosted {
		t.Fatalf("body = %#v", body)
	}
}

func TestSystemHealthAcceptsValidSessions(t *testing.T) {
	t.Run("local session", func(t *testing.T) {
		now := time.Now()
		h := &Handler{authMode: auth.AuthModeLocal}
		h.SetAuthConfig(auth.Config{
			Enabled: true, Username: "admin", Password: "secret", SessionSecret: "local-secret", SessionTTL: time.Hour,
		})
		token, err := h.authCfg.SignToken(now)
		if err != nil {
			t.Fatal(err)
		}
		h.SetSystemHealth(buildinfo.Build{}, "", "", "", "", 1, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
		rec := httptest.NewRecorder()
		if err := h.SystemHealth(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("hosted session", func(t *testing.T) {
		now := time.Now()
		key := []byte("0123456789abcdef0123456789abcdef")
		h := &Handler{
			authMode:            auth.AuthModeHostedSSO,
			hostedSSOKey:        key,
			hostedSSOInstanceID: "instance-1",
		}
		claims := auth.SessionClaims{
			Version: 1, Subject: "subject", Email: "user@example.com", Display: "user@example.com",
			InstanceID: "instance-1", AuthSource: auth.HostedAuthSource,
			IssuedAt: now.Unix(), ExpiresAt: now.Unix() + int64(time.Hour/time.Second),
		}
		token, err := auth.SignHostedSession(claims, key)
		if err != nil {
			t.Fatal(err)
		}
		h.SetSystemHealth(buildinfo.Build{}, "", "", "", "", 1, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
		rec := httptest.NewRecorder()
		if err := h.SystemHealth(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestSystemHealthRejectsNonLoopbackAndAcceptsMatchingAgentToken(t *testing.T) {
	e := echo.New()
	h := &Handler{}
	h.SetSystemHealth(buildinfo.Build{Artifact: buildinfo.ArtifactContainer}, buildinfo.ModeHosted, buildinfo.DistributionHosted, "secret", "", 1, update.NewDrainManager(nil, nil, 0, time.Now))
	for _, tc := range []struct {
		auth string
		want int
	}{{"", http.StatusUnauthorized}, {"Bearer wrong", http.StatusUnauthorized}, {"Bearer secret", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/api/system/health", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("Authorization", tc.auth)
		rec := httptest.NewRecorder()
		if err := h.SystemHealth(e.NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		if rec.Code != tc.want {
			t.Fatalf("auth %q status=%d want=%d", tc.auth, rec.Code, tc.want)
		}
	}
}
