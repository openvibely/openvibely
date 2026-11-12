package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/config"
)

// Exercise the production backend, including CSRF checks, persisted sessions,
// invitation acceptance and logout, without a browser-side cookie jar.
func TestDesktopAuthentication(t *testing.T) {
	backend := desktopAuthBackend(t)
	target, _ := url.Parse(backend.BaseURL)
	for _, origin := range []string{"wails://localhost", "http://wails.localhost", "https://wails.localhost"} {
		t.Run(origin, func(t *testing.T) {
			proxy := desktopAssetProxy(target)
			request := func(method, path, requestOrigin, form, cookie string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, origin+path, strings.NewReader(form))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if path == "/events/native-ticket" {
					req.Header.Set("X-OpenVibely-Live", "1")
				}
				if requestOrigin != "" {
					req.Header.Set("Origin", requestOrigin)
				}
				if cookie != "" {
					req.Header.Set("Cookie", cookie)
				}
				rec := httptest.NewRecorder()
				proxy.ServeHTTP(rec, req)
				return rec
			}
			check := func(rec *httptest.ResponseRecorder, status int) {
				t.Helper()
				if rec.Code != status {
					t.Fatalf("got %d want %d: %s", rec.Code, status, rec.Body.String())
				}
			}
			login := request("POST", "/login", origin, "username=alice&password=alice-password-long", "")
			check(login, 302)
			stale := ""
			for _, cookie := range login.Result().Cookies() {
				if cookie.Name == auth.DefaultCookieName {
					stale = cookie.Name + "=" + cookie.Value
				}
			}
			if stale == "" {
				t.Fatal("missing session cookie")
			}
			check(request("GET", "/auth/me", origin, "", ""), 200)
			check(request("POST", "/events/native-ticket", origin, "", ""), 200)
			for _, badOrigin := range []string{"https://evil.example", "null", "wails://localhost.evil", "http://localhost"} {
				check(request("GET", "/auth/me", badOrigin, "", ""), 401)
				check(request("POST", "/organization", badOrigin, "name=bad", ""), 403)
				check(request("POST", "/login", badOrigin, "username=alice&password=alice-password-long", ""), 403)
			}
			// A separate native window cannot acquire this window's session.
			isolated := httptest.NewRecorder()
			desktopAssetProxy(target).ServeHTTP(isolated, httptest.NewRequest("GET", origin+"/auth/me", nil))
			check(isolated, 401)
			// WebKit HTMX requests may omit both Origin and Referer.
			invitation := request("POST", "/organization/invitations", "", "role=member", "")
			check(invitation, 200)
			match := regexp.MustCompile(`/invite\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(invitation.Body.String())
			if len(match) != 2 {
				t.Fatal("missing invitation token")
			}
			check(request("POST", "/logout", origin, "", ""), 302)
			check(request("GET", "/auth/me", origin, "", stale), 401)
			username := "member-" + strings.ReplaceAll(strings.ReplaceAll(origin, "://", "-"), ".", "-")
			check(request("POST", "/invite", origin, url.Values{"token": {match[1]}, "username": {username}, "password": {"member-password-long"}}.Encode(), ""), 303)
			me := request("GET", "/auth/me", origin, "", "")
			check(me, 200)
			if !strings.Contains(me.Body.String(), `"role":"member"`) {
				t.Fatalf("wrong member session: %s", me.Body.String())
			}
			check(request("POST", "/logout", origin, "", ""), 302)
			check(request("GET", "/auth/me", origin, "", ""), 401)
		})
	}
}

func desktopAuthBackend(t *testing.T) *desktopBackend {
	t.Helper()
	updates := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(updates.Close)
	root := t.TempDir()
	backend, err := startDesktopBackend(context.Background(), &config.Config{
		Mode: config.ModeDesktop, Port: "0", Environment: "test",
		DatabasePath: filepath.Join(root, "test.db"), ProjectRepoRoot: filepath.Join(root, "repos"), AppDataDir: filepath.Join(root, "appdata"), UpdateServiceURL: updates.URL,
		AuthEnabled: true, AuthMode: auth.AuthModeLocal, AuthUsername: "alice", AuthPassword: "alice-password-long", AuthSessionSecret: "desktop-test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)

	return backend
}
