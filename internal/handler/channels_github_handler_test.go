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
	assertChannelsRefreshTrigger(t, disconnectRec)
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
	assertChannelsRefreshTrigger(t, rec)

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
	assertChannelsRefreshTrigger(t, rec)

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
	assertChannelsRefreshTrigger(t, rec)
	appID, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppID)
	appSlug, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppSlug)
	privateKey, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAppPrivateKey)
	pat, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingPAT)
	authMode, _ := h.settingsRepo.Get(context.Background(), service.GitHubSettingAuthMode)
	if appID != "" || appSlug != "" || privateKey != "" || pat != "" || authMode != "" {
		t.Fatalf("expected github settings cleared, got id=%q slug=%q key=%q pat=%q mode=%q", appID, appSlug, privateKey, pat, authMode)
	}
}

func assertGitHubRuntimeSettingsFragmentResponse(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("expected github runtime fragment response not to trigger channels refresh, got HX-Trigger=%q", got)
	}
	if got := rec.Header().Get("HX-Refresh"); got != "" {
		t.Fatalf("expected github runtime fragment response not to refresh page, got HX-Refresh=%q", got)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("expected github runtime fragment response not to redirect, got Location=%q", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="github-runtime-settings"`) {
		t.Fatalf("expected github runtime settings fragment, got: %s", body)
	}
	for _, want := range []string{
		"Authorized Users",
		"GitHub users allowed for this channel.",
		"Scheduled GitHub tasks can scan issues assigned to these users",
		"authorization checks use the same list",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected github runtime settings fragment to include %q, got: %s", want, body)
		}
	}
	for _, old := range []string{"Optional Authorized Users", "Project Inbox Assignee Override", "Issue Inbox Assignee", "Approve"} {
		if strings.Contains(body, old) {
			t.Fatalf("did not expect confusing github runtime copy %q, got: %s", old, body)
		}
	}
}

func TestGitHubRuntimeSettingsRoutesAuthorizeActors(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("project_id", "default")
	form.Set("github_login", " @Alice ")
	form.Set("display_name", "Alice")
	form.Set("permission", "")

	rec := htmxPost(e, "/channels/github/authorized-actors", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertGitHubRuntimeSettingsFragmentResponse(t, rec)
	body := rec.Body.String()
	if !strings.Contains(body, "@alice") {
		t.Fatalf("expected normalized login in fragment, got: %s", body)
	}
	if strings.Contains(body, "Approve") {
		t.Fatalf("did not expect approval permission jargon in fragment, got: %s", body)
	}
	authorized, err := h.githubAuthRepo.IsActorAuthorized(context.Background(), "alice")
	if err != nil {
		t.Fatalf("checking stored actor: %v", err)
	}
	if !authorized {
		t.Fatal("expected normalized actor to authorize plain login")
	}
	authorized, err = h.githubAuthRepo.IsActorAuthorized(context.Background(), "@ALICE")
	if err != nil {
		t.Fatalf("checking @ actor: %v", err)
	}
	if !authorized {
		t.Fatal("expected stored actor to authorize @ login")
	}

	actors, err := h.githubAuthRepo.ListAuthorizedActors(context.Background())
	if err != nil {
		t.Fatalf("list actors: %v", err)
	}
	if len(actors) != 1 {
		t.Fatalf("expected one actor, got %d", len(actors))
	}
	if actors[0].Permission != "triage" {
		t.Fatalf("expected empty permission to default to triage, got %q", actors[0].Permission)
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/channels/github/authorized-actors/"+actors[0].ID+"?project_id=default", nil)
	deleteReq.Header.Set("HX-Request", "true")
	deleteRec := httptest.NewRecorder()
	e.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d (%s)", deleteRec.Code, deleteRec.Body.String())
	}
	assertGitHubRuntimeSettingsFragmentResponse(t, deleteRec)
	if body := deleteRec.Body.String(); !strings.Contains(body, "No authorized users configured. GitHub authorization checks deny by default, and GitHub App/custom issue scanning has no assignee to use.") {
		t.Fatalf("expected empty authorized users message after delete, got: %s", body)
	}
	authorized, err = h.githubAuthRepo.IsActorAuthorized(context.Background(), "alice")
	if err != nil {
		t.Fatalf("checking deleted actor: %v", err)
	}
	if authorized {
		t.Fatal("expected actor to be unauthorized after delete")
	}
}

func TestGitHubProjectInboxRouteStoresProjectScopedNormalizedLogin(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("project_id", "default")
	form.Set("github_login", " @Dev-Bot ")
	form.Set("enabled", "true")

	rec := htmxPost(e, "/channels/github/project-inbox", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertGitHubRuntimeSettingsFragmentResponse(t, rec)
	inbox, err := h.githubAuthRepo.GetEnabledProjectInbox(context.Background(), "default")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	if inbox == nil || inbox.GitHubLogin != "dev-bot" {
		t.Fatalf("expected enabled dev-bot inbox, got %#v", inbox)
	}
	other, err := h.githubAuthRepo.GetProjectInbox(context.Background(), "other-project")
	if err != nil {
		t.Fatalf("get other inbox: %v", err)
	}
	if other != nil {
		t.Fatalf("expected project-scoped inbox, got other project row %#v", other)
	}

	disabled := url.Values{}
	disabled.Set("project_id", "default")
	disabled.Set("github_login", "")
	disabled.Set("enabled", "true")
	rec = htmxPost(e, "/channels/github/project-inbox", disabled)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected disable status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertGitHubRuntimeSettingsFragmentResponse(t, rec)
	inbox, err = h.githubAuthRepo.GetEnabledProjectInbox(context.Background(), "default")
	if err != nil {
		t.Fatalf("get disabled inbox: %v", err)
	}
	if inbox != nil {
		t.Fatalf("expected disabled inbox to be hidden from enabled lookup, got %#v", inbox)
	}
}

func TestGitHubRuntimeSettingsRoutesPreflightErrors(t *testing.T) {
	t.Run("missing project id", func(t *testing.T) {
		_, e, _ := setupTestHandler(t)
		cases := []struct {
			name    string
			request func() *httptest.ResponseRecorder
		}{
			{
				name: "add authorized actor",
				request: func() *httptest.ResponseRecorder {
					return htmxPost(e, "/channels/github/authorized-actors", url.Values{"github_login": {"alice"}})
				},
			},
			{
				name: "remove authorized actor",
				request: func() *httptest.ResponseRecorder {
					return htmxDelete(e, "/channels/github/authorized-actors/actor-id")
				},
			},
			{
				name: "save project inbox",
				request: func() *httptest.ResponseRecorder {
					return htmxPost(e, "/channels/github/project-inbox", url.Values{"github_login": {"alice"}})
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := tc.request()
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "project_id is required") {
					t.Fatalf("expected controlled missing-project error, got %d (%s)", rec.Code, rec.Body.String())
				}
			})
		}
	})

	t.Run("unavailable GitHub authorization dependency", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		h.SetGitHubAuthRepo(nil)
		cases := []struct {
			name    string
			request func() *httptest.ResponseRecorder
		}{
			{
				name: "add authorized actor",
				request: func() *httptest.ResponseRecorder {
					return htmxPost(e, "/channels/github/authorized-actors", url.Values{"project_id": {"default"}, "github_login": {"alice"}})
				},
			},
			{
				name: "remove authorized actor",
				request: func() *httptest.ResponseRecorder {
					return htmxDelete(e, "/channels/github/authorized-actors/actor-id?project_id=default")
				},
			},
			{
				name: "save project inbox",
				request: func() *httptest.ResponseRecorder {
					return htmxPost(e, "/channels/github/project-inbox", url.Values{"project_id": {"default"}, "github_login": {"alice"}})
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := tc.request()
				if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "GitHub auth not configured") {
					t.Fatalf("expected controlled unavailable-dependency error, got %d (%s)", rec.Code, rec.Body.String())
				}
			})
		}
	})
}

func TestChannelsPageRendersGitHubRuntimeSettingsLazyHook(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{Configured: true, Connected: true, AuthMode: service.GitHubAuthModePAT}, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/channels/github/runtime-settings?project_id=default") {
		t.Fatalf("expected github runtime settings lazy endpoint in page")
	}
	if !strings.Contains(body, "Loading GitHub runtime settings") {
		t.Fatalf("expected github runtime settings loading placeholder")
	}
	runtimeIdx := strings.Index(body, `id="github-runtime-settings"`)
	saveIdx := strings.Index(body, "Save GitHub Settings")
	cancelIdx := strings.Index(body, `onclick="closeGitHubConfigModal()">Cancel`)
	if runtimeIdx == -1 || saveIdx == -1 || cancelIdx == -1 {
		t.Fatalf("expected github modal runtime settings plus cancel/save actions")
	}
	if runtimeIdx > saveIdx || runtimeIdx > cancelIdx {
		t.Fatalf("expected github runtime settings to render before modal Cancel/Save actions so the footer stays last")
	}
	if !strings.Contains(body, `modal-action sticky bottom-0`) {
		t.Fatalf("expected github modal Cancel/Save actions to use the sticky modal footer placement")
	}
}
