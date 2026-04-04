package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestChannelsPage_RendersSlackCardWhenConfigured(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true, TeamName: "OpenVibely"}, nil
		},
	})

	project := createProject(t, h, "Slack Card Project")
	if h.slackAuthRepo != nil {
		if err := h.slackAuthRepo.Create(context.Background(), &models.SlackAuthorizedUser{
			ProjectID:   project.ID,
			SlackUserID: "U12345",
			DisplayName: "Alice",
			AddedBy:     "test",
		}); err != nil {
			t.Fatalf("failed to seed slack authorized user: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-channel-type="slack"`) {
		t.Fatal("expected slack active card")
	}
	if !strings.Contains(body, "OpenVibely") {
		t.Fatal("expected slack workspace metadata")
	}
	if !strings.Contains(body, "Authorized users:") || !strings.Contains(body, "1 user(s)") {
		t.Fatal("expected slack card authorized users count")
	}
}

func TestChannelsSlackConfigure(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	var reloaded bool
	h.SetSlackService(&fakeSlackService{
		reloadFn: func(ctx context.Context) error {
			reloaded = true
			return nil
		},
	})

	form := url.Values{}
	form.Set("slack_client_id", "cid")
	form.Set("slack_client_secret", "csecret")
	form.Set("slack_app_token", "xapp-123")
	form.Set("slack_bot_token", "xoxb-123")
	form.Set("slack_send_responses", "true")

	req := httptest.NewRequest(http.MethodPost, "/channels/slack/configure", strings.NewReader(form.Encode()))
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
	if !reloaded {
		t.Fatal("expected slack reload to be triggered")
	}

	clientID, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingClientID)
	appToken, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingAppToken)
	if clientID != "cid" || appToken != "xapp-123" {
		t.Fatalf("expected slack settings saved, got client_id=%q app_token=%q", clientID, appToken)
	}

	viewReq := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	viewRec := httptest.NewRecorder()
	e.ServeHTTP(viewRec, viewReq)
	if viewRec.Code != http.StatusOK {
		t.Fatalf("expected channels page 200, got %d", viewRec.Code)
	}
	body := viewRec.Body.String()
	if !strings.Contains(body, "Restrict Slack access to specific users for this project") {
		t.Fatalf("expected slack modal to include Slack authorized users section")
	}
	if !strings.Contains(body, "If no users are configured, access is denied until authorized users are added") {
		t.Fatalf("expected deny-by-default helper copy in slack modal")
	}
}

func TestChannelsSlackConfigure_ManualOverrideMode(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetSlackService(&fakeSlackService{
		reloadFn: func(ctx context.Context) error { return nil },
	})

	form := url.Values{}
	form.Set("slack_client_id", "cid")
	form.Set("slack_client_secret", "csecret")
	form.Set("slack_app_token", "xapp-123")
	form.Set("slack_bot_token_mode", service.SlackBotTokenSourceManual)
	form.Set("slack_bot_token", "xoxb-manual-123")

	req := httptest.NewRequest(http.MethodPost, "/channels/slack/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	mode, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingBotTokenSource)
	overrideToken, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingBotTokenOverride)
	if mode != service.SlackBotTokenSourceManual {
		t.Fatalf("expected manual bot token mode, got %q", mode)
	}
	if overrideToken != "xoxb-manual-123" {
		t.Fatalf("expected manual override token to be saved, got %q", overrideToken)
	}
}

func TestChannelsSlackConfigure_ManualModeRequiresToken(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetSlackService(&fakeSlackService{
		reloadFn: func(ctx context.Context) error { return nil },
	})

	form := url.Values{}
	form.Set("slack_client_id", "cid")
	form.Set("slack_client_secret", "csecret")
	form.Set("slack_app_token", "xapp-123")
	form.Set("slack_bot_token_mode", service.SlackBotTokenSourceManual)

	req := httptest.NewRequest(http.MethodPost, "/channels/slack/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Slack bot token is required") {
		t.Fatalf("expected bot token required error, got %q", rec.Body.String())
	}
}

func TestChannelsSlackConnectRedirect(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetSlackService(&fakeSlackService{
		connectURLFn: func(ctx context.Context, redirectURI string) (string, error) {
			if !strings.Contains(redirectURI, "/channels/slack/callback") {
				t.Fatalf("expected redirect URI to include slack callback path, got %q", redirectURI)
			}
			return "https://slack.com/oauth/v2/authorize?client_id=abc", nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/channels/slack/connect", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected status 307, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got == "" || !strings.Contains(got, "slack.com/oauth") {
		t.Fatalf("unexpected redirect location: %q", got)
	}
}

func TestChannelsSlackCallbackDisconnectAndRemove(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	var callbackCalled bool
	var disconnectCalled bool
	h.SetSlackService(&fakeSlackService{
		callbackFn: func(ctx context.Context, code, state, redirectURI string) error {
			callbackCalled = code == "abc" && state == "st"
			return nil
		},
		disconnectFn: func(ctx context.Context) error {
			disconnectCalled = true
			return nil
		},
	})

	_ = h.settingsRepo.Set(context.Background(), service.SlackSettingClientID, "cid")
	_ = h.settingsRepo.Set(context.Background(), service.SlackSettingBotToken, "xoxb-123")

	cbReq := httptest.NewRequest(http.MethodGet, "/channels/slack/callback?code=abc&state=st", nil)
	cbRec := httptest.NewRecorder()
	e.ServeHTTP(cbRec, cbReq)
	if cbRec.Code != http.StatusSeeOther {
		t.Fatalf("expected callback status 303, got %d", cbRec.Code)
	}
	if !callbackCalled {
		t.Fatal("expected callback handler to be called")
	}

	disReq := httptest.NewRequest(http.MethodPost, "/channels/slack/disconnect", nil)
	disReq.Header.Set("HX-Request", "true")
	disRec := httptest.NewRecorder()
	e.ServeHTTP(disRec, disReq)
	if disRec.Code != http.StatusOK {
		t.Fatalf("expected disconnect status 200, got %d", disRec.Code)
	}
	if !disconnectCalled {
		t.Fatal("expected disconnect to be called")
	}

	removeReq := httptest.NewRequest(http.MethodPost, "/channels/slack/remove", nil)
	removeReq.Header.Set("HX-Request", "true")
	removeRec := httptest.NewRecorder()
	e.ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("expected remove status 200, got %d", removeRec.Code)
	}

	clientID, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingClientID)
	botToken, _ := h.settingsRepo.Get(context.Background(), service.SlackSettingBotToken)
	if clientID != "" || botToken != "" {
		t.Fatalf("expected slack settings cleared on remove, got client_id=%q bot_token=%q", clientID, botToken)
	}
}

func TestChannelsSlackTestConnection(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetSlackService(&fakeSlackService{
		testFn: func(ctx context.Context) error {
			return nil
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/channels/slack/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Connection successful") {
		t.Fatalf("expected success body, got %q", rec.Body.String())
	}

	h.SetSlackService(&fakeSlackService{
		testFn: func(ctx context.Context) error {
			return errors.New("boom")
		},
	})
	req2 := httptest.NewRequest(http.MethodPost, "/channels/slack/test", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "Connection failed") {
		t.Fatalf("expected failure body, got %q", rec2.Body.String())
	}
}
