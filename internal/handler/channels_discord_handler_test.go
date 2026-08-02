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

func TestChannelsPage_RendersDiscordCardWhenConfigured(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetDiscordService(&fakeDiscordService{
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{Configured: true, Connected: true, Running: true, BotUserID: "bot-1"}, nil
		},
	})

	project := createProject(t, h, "Discord Card Project")
	if h.discordAuthRepo != nil {
		if err := h.discordAuthRepo.Create(context.Background(), &models.DiscordAuthorizedUser{
			ProjectID:     project.ID,
			DiscordUserID: "12345",
			DisplayName:   "Alice",
			AddedBy:       "test",
		}); err != nil {
			t.Fatalf("failed to seed discord authorized user: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id="+project.ID, nil)
	req.AddCookie(&http.Cookie{Name: "current_project_id", Value: project.ID})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-channel-type="discord"`) {
		t.Fatal("expected discord active card")
	}
	if !strings.Contains(body, "Authorized users:") || !strings.Contains(body, "1 user(s)") {
		t.Fatal("expected discord card authorized users count")
	}
	if strings.Contains(body, "Discord Bot Coming Soon") {
		t.Fatal("did not expect discord coming soon placeholder")
	}
	if !strings.Contains(body, "/channels/discord/remove?project_id="+project.ID) {
		t.Fatal("expected discord delete action to preserve project context for auth cleanup")
	}
}

func TestChannelsPage_RendersDiscordGatewayOfflineWhenConfiguredButNotRunning(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetDiscordService(&fakeDiscordService{
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{
				Configured:  true,
				Connected:   false,
				Running:     false,
				HasBotToken: true,
				BotUserID:   "bot-1",
				LastError:   "open discord gateway: websocket: close 4004: Authentication failed",
			}, nil
		},
	})
	project := createProject(t, h, "Discord Offline Project")

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Gateway Offline") || !strings.Contains(body, "Gateway failed to start") || !strings.Contains(body, "Authentication failed") {
		t.Fatalf("expected Discord offline gateway status with error, got %q", body)
	}
	if strings.Contains(body, `badge-success badge-sm">Connected`) || strings.Contains(body, "Gateway running") {
		t.Fatalf("did not expect Discord card to claim connected/running when gateway is offline: %q", body)
	}
}

func TestChannelsDiscordConfigure(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	var reloaded bool
	h.SetDiscordService(&fakeDiscordService{
		reloadFn: func(ctx context.Context) error {
			reloaded = true
			return nil
		},
	})

	form := url.Values{}
	form.Set("discord_bot_token", "bot-token")
	form.Set("discord_default_channel_id", "legacy-ignored")
	form.Set("discord_free_response_channels", "legacy-ignored")
	form.Set("discord_send_responses", "true")
	form.Set("discord_require_mention", "false")

	req := httptest.NewRequest(http.MethodPost, "/channels/discord/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertChannelsRefreshTrigger(t, rec)
	if !reloaded {
		t.Fatalf("expected discord service reload")
	}
	for key, want := range map[string]string{
		service.DiscordSettingBotToken:      "bot-token",
		service.DiscordSettingSendResponses: "true",
	} {
		got, err := h.settingsRepo.Get(context.Background(), key)
		if err != nil || got != want {
			t.Fatalf("setting %s = %q, %v; want %q", key, got, err, want)
		}
	}
	for _, key := range []string{"discord_default_channel_id", "discord_free_response_channels", "discord_require_mention"} {
		got, err := h.settingsRepo.Get(context.Background(), key)
		if err != nil {
			t.Fatalf("get legacy setting %s: %v", key, err)
		}
		if got != "" {
			t.Fatalf("expected legacy setting %s not to be saved, got %q", key, got)
		}
	}
}

func TestChannelsDiscordConfigureReturnsSuccessWhenReloadFailsAfterSave(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetDiscordService(&fakeDiscordService{
		reloadFn: func(ctx context.Context) error { return errors.New("gateway unavailable") },
		statusFn: func(ctx context.Context) (service.DiscordConnectionStatus, error) {
			return service.DiscordConnectionStatus{Configured: true, HasBotToken: true, Running: false, Connected: false, LastError: "gateway unavailable"}, nil
		},
	})

	form := url.Values{}
	form.Set("discord_bot_token", "bot-token")

	req := httptest.NewRequest(http.MethodPost, "/channels/discord/configure", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 after saving settings despite reload failure, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertChannelsRefreshTrigger(t, rec)
	got, err := h.settingsRepo.Get(context.Background(), service.DiscordSettingBotToken)
	if err != nil || got != "bot-token" {
		t.Fatalf("expected saved bot token despite reload failure, got %q err=%v", got, err)
	}
}

func TestChannelsDiscordConfigureRequiresToken(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/channels/discord/configure", strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestChannelsDiscordRemoveClearsSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Discord Remove Project")
	otherProject := createProject(t, h, "Other Discord Remove Project")
	if err := h.discordAuthRepo.Create(context.Background(), &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "12345", DisplayName: "Alice", AddedBy: "test"}); err != nil {
		t.Fatalf("seed discord auth user: %v", err)
	}
	if err := h.discordAuthRepo.Create(context.Background(), &models.DiscordAuthorizedUser{ProjectID: otherProject.ID, DiscordUserID: "67890", DisplayName: "Bob", AddedBy: "test"}); err != nil {
		t.Fatalf("seed other discord auth user: %v", err)
	}
	var disconnected bool
	h.SetDiscordService(&fakeDiscordService{
		disconnectFn: func(ctx context.Context) error {
			disconnected = true
			return nil
		},
	})
	for _, key := range []string{service.DiscordSettingBotToken, service.DiscordSettingSendResponses} {
		if err := h.settingsRepo.Set(context.Background(), key, "value"); err != nil {
			t.Fatalf("seed setting: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/channels/discord/remove?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	assertChannelsRefreshTrigger(t, rec)
	if !disconnected {
		t.Fatalf("expected discord service disconnect")
	}
	users, err := h.discordAuthRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(users) != 0 {
		t.Fatalf("expected discord authorized users cleared for deleted project, got %d err=%v", len(users), err)
	}
	otherUsers, err := h.discordAuthRepo.ListByProject(context.Background(), otherProject.ID)
	if err != nil || len(otherUsers) != 0 {
		t.Fatalf("expected system-level discord authorized users cleared, got %d err=%v", len(otherUsers), err)
	}
}

func TestChannelsDiscordTestConnection(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetDiscordService(&fakeDiscordService{testFn: func(ctx context.Context) error { return nil }})

	req := httptest.NewRequest(http.MethodPost, "/channels/discord/test", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Connection successful!") {
		t.Fatalf("expected success response, got %d %q", rec.Code, rec.Body.String())
	}

	h.SetDiscordService(&fakeDiscordService{testFn: func(ctx context.Context) error { return errors.New("bad token") }})
	req2 := httptest.NewRequest(http.MethodPost, "/channels/discord/test", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "bad token") {
		t.Fatalf("expected failure response, got %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestDiscordAuthorizedUsersHandlers(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Discord Auth Handler Project")

	form := url.Values{}
	form.Set("project_id", project.ID)
	form.Set("discord_user_id", "12345")
	req := httptest.NewRequest(http.MethodPost, "/channels/discord/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected add status 200, got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ">12345<") || !strings.Contains(rec.Body.String(), "ID: 12345") {
		t.Fatalf("expected numeric ID display-name default in response: %q", rec.Body.String())
	}

	invalidForm := url.Values{}
	invalidForm.Set("project_id", project.ID)
	invalidForm.Set("discord_user_id", "jamesdubee_53308")
	invalidReq := httptest.NewRequest(http.MethodPost, "/channels/discord/authorized-users", strings.NewReader(invalidForm.Encode()))
	invalidReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	invalidRec := httptest.NewRecorder()
	e.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest || !strings.Contains(invalidRec.Body.String(), "numeric ID") {
		t.Fatalf("expected non-numeric Discord ID rejected with numeric guidance, got %d %q", invalidRec.Code, invalidRec.Body.String())
	}

	users, err := h.discordAuthRepo.ListByProject(context.Background(), project.ID)
	if err != nil || len(users) != 1 {
		t.Fatalf("expected one discord user, got %d err=%v", len(users), err)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/channels/discord/authorized-users/"+users[0].ID+"?project_id="+project.ID, nil)
	delRec := httptest.NewRecorder()
	e.ServeHTTP(delRec, delReq)

	if delRec.Code != http.StatusOK {
		t.Fatalf("expected delete status 200, got %d %q", delRec.Code, delRec.Body.String())
	}
	if !strings.Contains(delRec.Body.String(), "No authorized users configured. Access is denied until authorized users are added.") {
		t.Fatalf("expected deny-by-default empty state, got %q", delRec.Body.String())
	}
}
