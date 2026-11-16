package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/config"
)

func TestDesktopAuthenticatedStartupNavigation(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("custom-scheme WebKit navigation")
	}
	backend := desktopAuthBackend(t)
	target, _ := url.Parse(backend.BaseURL)
	proxy := desktopAssetProxy(target)
	navigate := func(method, path, form string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "wails://localhost"+path, strings.NewReader(form))
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		if method == http.MethodPost {
			req.Header.Set("Origin", "wails://localhost")
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s returned %d, Location %q: %s", method, path, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		return rec
	}
	startup := navigate(http.MethodGet, "/chat", "")
	if !strings.Contains(startup.Body.String(), `window.location.replace("/login?next=L2NoYXQ")`) {
		t.Fatalf("startup did not navigate to login: %s", startup.Body.String())
	}
	loginPage := navigate(http.MethodGet, "/login?next=L2NoYXQ", "")
	if !strings.Contains(loginPage.Body.String(), `action="/login"`) {
		t.Fatal("login form missing")
	}
	login := navigate(http.MethodPost, "/login", "username=alice&password=alice-password-long&next=L2NoYXQ")
	if !strings.Contains(login.Body.String(), `window.location.replace("/chat")`) {
		t.Fatalf("login did not return to chat: %s", login.Body.String())
	}
	chat := navigate(http.MethodGet, "/chat", "")
	if !strings.Contains(chat.Body.String(), `id="main-content"`) {
		t.Fatal("authenticated workspace missing")
	}
}

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
			for _, path := range []string{"/login", "/invite?token=missing"} {
				page := request("GET", path, "", "", "")
				for _, marker := range []string{`id="desktop-project-titlebar"`, `--wails-draggable: drag`, `/wails/runtime.js`} {
					if !strings.Contains(page.Body.String(), marker) {
						t.Errorf("%s missing native auth chrome %q", path, marker)
					}
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

// Invitations created in a native window are opened and submitted in a browser.
func TestDesktopInvitationBrowserSignup(t *testing.T) {
	backend := desktopAuthBackend(t)
	target, err := url.Parse(backend.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	for i, origin := range []string{"wails://localhost", "http://wails.localhost", "https://wails.localhost"} {
		t.Run(origin, func(t *testing.T) {
			proxy := desktopAssetProxy(target)
			post := func(path, form string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, origin+path, strings.NewReader(form))
				req.Header.Set("Origin", origin)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				proxy.ServeHTTP(rec, req)
				return rec
			}
			if rec := post("/login", "username=alice&password=alice-password-long"); rec.Code != http.StatusFound {
				t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
			}
			invitation := post("/organization/invitations", "role=member")
			if invitation.Code != http.StatusOK {
				t.Fatalf("invite: %d %s", invitation.Code, invitation.Body.String())
			}
			match := regexp.MustCompile(`<code>(https?://[^<]+/invite\?token=[A-Za-z0-9_-]+)</code>`).FindStringSubmatch(invitation.Body.String())
			if len(match) != 2 {
				t.Fatal("missing invitation URL")
			}
			if !strings.HasPrefix(match[1], backend.BaseURL+"/invite?") {
				t.Fatalf("invitation URL must use bound backend address: %s", match[1])
			}
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			page, err := client.Get(match[1])
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(page.Body)
			page.Body.Close()
			if err != nil || page.StatusCode != http.StatusOK {
				t.Fatalf("invite page: %d %v %s", page.StatusCode, err, body)
			}
			token := regexp.MustCompile(`name="token" value="([A-Za-z0-9_-]+)"`).FindSubmatch(body)
			if len(token) != 2 {
				t.Fatal("missing signup form token")
			}
			form := url.Values{"token": {string(token[1])}, "username": {"browser-member-" + string(rune('a'+i))}, "password": {"member-password-long"}}
			join := func() *http.Response {
				req, err := http.NewRequest(http.MethodPost, backend.BaseURL+"/invite", strings.NewReader(form.Encode()))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Origin", backend.BaseURL)
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			joined := join()
			body, err = io.ReadAll(joined.Body)
			joined.Body.Close()
			if err != nil || joined.StatusCode != http.StatusSeeOther {
				t.Fatalf("signup: %d %v %s", joined.StatusCode, err, body)
			}
			if joined.Header.Get("Location") != "/organization" || len(joined.Cookies()) == 0 {
				t.Fatal("signup did not establish a session and redirect to organization")
			}
			meRequest, err := http.NewRequest(http.MethodGet, backend.BaseURL+"/auth/me", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, cookie := range joined.Cookies() {
				meRequest.AddCookie(cookie)
			}
			me, err := client.Do(meRequest)
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(me.Body)
			me.Body.Close()
			if err != nil || me.StatusCode != http.StatusOK || !strings.Contains(string(body), `"role":"member"`) || !strings.Contains(string(body), form.Get("username")) {
				t.Fatalf("member session: %d %v %s", me.StatusCode, err, body)
			}
			reused := join()
			reused.Body.Close()
			if reused.StatusCode != http.StatusBadRequest {
				t.Fatalf("reused invitation: %d", reused.StatusCode)
			}
		})
	}
}

func desktopAuthBackend(t *testing.T) *desktopBackend {
	t.Helper()
	updates := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(updates.Close)
	root := t.TempDir()
	cfg := &config.Config{
		Mode: config.ModeDesktop, Port: "0", Environment: "test",
		DatabasePath: filepath.Join(root, "test.db"), ProjectRepoRoot: filepath.Join(root, "repos"), AppDataDir: filepath.Join(root, "appdata"), UpdateServiceURL: updates.URL,
		AuthEnabled: true, AuthMode: auth.AuthModeLocal, AuthUsername: "alice", AuthPassword: "alice-password-long", AuthSessionSecret: "desktop-test-secret",
	}
	// Keep state out of the working directory (the source tree): concurrent test
	// processes would otherwise share its plugin root and race to migrate ./uploads.
	t.Setenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION", "1")
	t.Setenv("OPENVIBELY_PLUGIN_ROOT", "")
	if err := ensureDesktopPluginRoot(cfg); err != nil {
		t.Fatal(err)
	}
	backend, err := startDesktopBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Shutdown)

	return backend
}
