package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
)

func TestChannelsPage_RendersGitHubCardStatus(t *testing.T) {
	t.Run("not configured", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		h.SetGitHubService(&fakeGitHubService{
			statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
				return service.GitHubConnectionStatus{Configured: false}, nil
			},
		})

		req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Add Channel") {
			t.Fatalf("expected add channel entry point")
		}
		if strings.Contains(body, "Not Configured") {
			t.Fatalf("did not expect GitHub active card status when not added")
		}
	})

	t.Run("connected", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		h.SetGitHubService(&fakeGitHubService{
			statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
				return service.GitHubConnectionStatus{Configured: true, Connected: true, InstallationID: "12345", AccountLogin: "openvibely"}, nil
			},
		})

		req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Connected") {
			t.Fatalf("expected Connected status")
		}
		if !strings.Contains(body, "openvibely") {
			t.Fatalf("expected account metadata to render")
		}
		if strings.Contains(body, "Clear Token") {
			t.Fatalf("did not expect token-specific clear action on connected GitHub card")
		}
	})
}

func TestChannelsGitHubConnectRedirect(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetGitHubService(&fakeGitHubService{
		connectURLFn: func(_ context.Context) (string, error) {
			return "https://github.com/apps/openvibely/installations/new", nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/channels/github/connect", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected status 307, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://github.com/apps/openvibely/installations/new" {
		t.Fatalf("unexpected redirect location: %s", got)
	}
}

func TestChannelsGitHubCallbackAndDisconnect(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	var callbackInstallationID string
	var disconnectCalled bool
	h.SetGitHubService(&fakeGitHubService{
		callbackFn: func(_ context.Context, installationID string) error {
			callbackInstallationID = installationID
			return nil
		},
		disconnectFn: func(_ context.Context) error {
			disconnectCalled = true
			return nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/channels/github/callback?installation_id=4242", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected callback status 303, got %d", rec.Code)
	}
	if callbackInstallationID != "4242" {
		t.Fatalf("expected installation_id 4242, got %s", callbackInstallationID)
	}

	disconnectReq := httptest.NewRequest(http.MethodPost, "/channels/github/disconnect", nil)
	disconnectReq.Header.Set("HX-Request", "true")
	disconnectRec := httptest.NewRecorder()
	e.ServeHTTP(disconnectRec, disconnectReq)

	if disconnectRec.Code != http.StatusOK {
		t.Fatalf("expected disconnect status 200, got %d", disconnectRec.Code)
	}
	if !disconnectCalled {
		t.Fatal("expected disconnect handler to be called")
	}
	if disconnectRec.Header().Get("HX-Refresh") != "true" {
		t.Fatal("expected HX-Refresh header on HTMX disconnect")
	}
}

func TestChannelsGitHubConfigure(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("github_app_id", "123456")
	form.Set("github_app_slug", "openvibely-app")
	form.Set("github_app_private_key", "-----BEGIN RSA PRIVATE KEY-----\\nabc\\n-----END RSA PRIVATE KEY-----")

	req := httptest.NewRequest(http.MethodPost, "/channels/github/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh true")
	}

	appID, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppID)
	if err != nil {
		t.Fatalf("reading app id: %v", err)
	}
	if appID != "123456" {
		t.Fatalf("expected app id saved, got %q", appID)
	}
	appSlug, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppSlug)
	if err != nil {
		t.Fatalf("reading app slug: %v", err)
	}
	if appSlug != "openvibely-app" {
		t.Fatalf("expected app slug saved, got %q", appSlug)
	}
	privateKey, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppPrivateKey)
	if err != nil {
		t.Fatalf("reading private key: %v", err)
	}
	if privateKey == "" {
		t.Fatal("expected private key saved")
	}
	authMode, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingAuthMode)
	if err != nil {
		t.Fatalf("reading auth mode: %v", err)
	}
	if authMode != service.GitHubAuthModeApp {
		t.Fatalf("expected app auth mode, got %q", authMode)
	}
}

func TestChannelsGitHubConfigurePAT(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("github_auth_mode", service.GitHubAuthModePAT)
	form.Set("github_pat", "ghp_test_token")

	req := httptest.NewRequest(http.MethodPost, "/channels/github/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh true")
	}

	authMode, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingAuthMode)
	if err != nil {
		t.Fatalf("reading auth mode: %v", err)
	}
	if authMode != service.GitHubAuthModePAT {
		t.Fatalf("expected PAT auth mode, got %q", authMode)
	}
	pat, err := h.settingsRepo.Get(context.Background(), service.GitHubSettingPAT)
	if err != nil {
		t.Fatalf("reading PAT: %v", err)
	}
	if pat != "ghp_test_token" {
		t.Fatalf("expected PAT saved, got %q", pat)
	}
}

func TestChannelsGitHubRemove(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h.settingsRepo.Set(context.Background(), service.GitHubSettingAppID, "123")
	_ = h.settingsRepo.Set(context.Background(), service.GitHubSettingAppSlug, "my-app")
	_ = h.settingsRepo.Set(context.Background(), service.GitHubSettingAppPrivateKey, "secret")
	_ = h.settingsRepo.Set(context.Background(), service.GitHubSettingPAT, "ghp_token")
	_ = h.settingsRepo.Set(context.Background(), service.GitHubSettingAuthMode, service.GitHubAuthModePAT)

	req := httptest.NewRequest(http.MethodPost, "/channels/github/remove", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("expected HX-Refresh true")
	}
	appID, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppID)
	appSlug, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppSlug)
	privateKey, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppPrivateKey)
	pat, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingPAT)
	authMode, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAuthMode)
	if appID != "" || appSlug != "" || privateKey != "" || pat != "" || authMode != "" {
		t.Fatalf("expected github settings cleared, got id=%q slug=%q key=%q pat=%q mode=%q", appID, appSlug, privateKey, pat, authMode)
	}
}
