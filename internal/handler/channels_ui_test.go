package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func cardSectionByType(body, channelType string) string {
	start := strings.Index(body, `data-channel-type="`+channelType+`"`)
	if start == -1 {
		return ""
	}
	next := strings.Index(body[start+1:], `data-channel-type="`)
	if next == -1 {
		return body[start:]
	}
	return body[start : start+1+next]
}

func titleSection(cardBody string) string {
	titleStart := strings.Index(cardBody, `<h3 class="font-bold flex items-center gap-2">`)
	if titleStart == -1 {
		return ""
	}
	titleEnd := strings.Index(cardBody[titleStart:], `</h3>`)
	if titleEnd == -1 {
		return ""
	}
	return cardBody[titleStart : titleStart+titleEnd+len(`</h3>`)]
}

func assertIndexOrder(t *testing.T, body, first, second, message string) {
	t.Helper()
	firstIdx := strings.Index(body, first)
	if firstIdx == -1 {
		t.Fatalf("missing marker %q", first)
	}
	secondIdx := strings.Index(body, second)
	if secondIdx == -1 {
		t.Fatalf("missing marker %q", second)
	}
	if firstIdx > secondIdx {
		t.Fatal(message)
	}
}

func TestChannelsPageRendersCardLayout(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify empty-state card-based layout is present
	if !strings.Contains(body, "No channels added yet") {
		t.Error("expected empty state when no channels are configured")
	}

	// Verify no active telegram card by default
	if strings.Contains(body, `data-channel-type="telegram"`) {
		t.Error("did not expect telegram card before channel is added")
	}

	// Verify dropdown handler still present for channel cards
	if !strings.Contains(body, "handleDropdownToggle") {
		t.Error("expected dropdown menu handler")
	}

	// Verify modal for telegram editing
	if !strings.Contains(body, "channel_modal") {
		t.Error("expected channel configuration modal")
	}
	if !strings.Contains(body, `id="channel_form"`) {
		t.Error("expected telegram channel form")
	}
	if !strings.Contains(body, `hx-post="/channels/telegram"`) {
		t.Error("expected telegram channel form to submit via HTMX")
	}
	if !strings.Contains(body, `hx-swap="none"`) {
		t.Error("expected telegram channel form to submit in-place without swapping")
	}

	// Verify add-channel dropdown menu exists
	if !strings.Contains(body, "All channels added") && !strings.Contains(body, "GitHub") {
		t.Error("expected add-channel dropdown options")
	}

	// Verify "Add Channel" button
	if !strings.Contains(body, "+ Add Channel") {
		t.Error("expected Add Channel button")
	}

	// Verify Coming Soon section
	if !strings.Contains(body, "Coming Soon") {
		t.Error("expected Coming Soon section")
	}

	// Verify add modal includes available channel options
	if !strings.Contains(body, "GitHub") || !strings.Contains(body, "Telegram Bot") || !strings.Contains(body, "Slack") {
		t.Error("expected add-channel options for GitHub, Slack, and Telegram")
	}
}

func TestChannelsPageConnectedCardsHideTokenSpecificTextAndActions(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{
				Configured: true,
				Connected:  true,
				AuthMode:   service.GitHubAuthModePAT,
			}, nil
		},
	})

	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `data-channel-type="github"`) {
		t.Fatal("expected connected GitHub card")
	}
	if !strings.Contains(body, `data-channel-type="telegram"`) {
		t.Fatal("expected connected Telegram card")
	}
	if strings.Contains(body, `data-channel-type="slack"`) {
		t.Fatal("did not expect Slack card when not configured")
	}
	if strings.Contains(body, "Clear Token") {
		t.Fatal("did not expect Clear Token action on connected GitHub card")
	}
	if strings.Contains(body, "Token configured") {
		t.Fatal("did not expect token configured text on connected Telegram card")
	}
	if !strings.Contains(body, `data-icon="telegram-brand"`) {
		t.Fatal("expected Telegram brand icon marker on connected Telegram card")
	}
	// Check icon uses currentColor (theme-adaptive) not hardcoded colors
	if !strings.Contains(body, `fill="currentColor"`) {
		t.Fatal("expected Telegram icon to use fill=\"currentColor\" for theme adaptation")
	}
	// Verify it's the official Simple Icons Telegram path (not custom/broken)
	if !strings.Contains(body, `M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12`) {
		t.Fatal("expected official Telegram icon path from Simple Icons")
	}
	if strings.Contains(body, `fill="#229ED9"`) || strings.Contains(body, `fill="#fff"`) {
		t.Fatal("Telegram icon should not use hardcoded brand colors")
	}
	if strings.Contains(body, `M8 10h.01M12 10h.01M16 10h.01`) {
		t.Fatal("did not expect legacy chat bubble icon path for Telegram card")
	}
}

func TestChannelsPageStatusBadgesRenderAtBottomOfDetailsSection(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)

	h.SetGitHubService(&fakeGitHubService{
		statusFn: func(ctx context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{
				Configured:    true,
				Connected:     true,
				AuthMode:      service.GitHubAuthModePAT,
				AccountLogin:  "ov-user",
			}, nil
		},
	})
	h.SetSlackService(&fakeSlackService{
		statusFn: func(ctx context.Context) (service.SlackConnectionStatus, error) {
			return service.SlackConnectionStatus{Configured: true, Connected: true, TeamName: "OpenVibely"}, nil
		},
	})

	if err := h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"); err != nil {
		t.Fatalf("failed to seed telegram token: %v", err)
	}

	if h.slackAuthRepo != nil {
		if err := h.slackAuthRepo.Create(context.Background(), &models.SlackAuthorizedUser{
			ProjectID:   "default",
			SlackUserID: "U123",
			DisplayName: "Slack User",
			AddedBy:     "test",
		}); err != nil {
			t.Fatalf("failed to seed slack authorized user: %v", err)
		}
	}
	if h.telegramAuthRepo == nil {
		h.SetTelegramAuthRepo(repository.NewTelegramAuthRepo(db))
	}
	if err := h.telegramAuthRepo.Create(context.Background(), &models.TelegramAuthorizedUser{
		ProjectID:        "default",
		TelegramUserID:   1001,
		TelegramUsername: "tguser",
		DisplayName:      "Telegram User",
		AddedBy:          "test",
	}); err != nil {
		t.Fatalf("failed to seed telegram authorized user: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, channelType := range []string{"github", "slack", "telegram"} {
		card := cardSectionByType(body, channelType)
		if card == "" {
			t.Fatalf("expected %s card to render", channelType)
		}
		title := titleSection(card)
		if strings.Contains(title, "badge") {
			t.Fatalf("expected %s title row to not include status badge", channelType)
		}
	}

	githubCard := cardSectionByType(body, "github")
	if !strings.Contains(githubCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected github connected badge in details section")
	}
	assertIndexOrder(
		t,
		githubCard,
		`Account: ov-user`,
		`<span class="badge badge-sm badge-success">Connected</span>`,
		"expected github status badge below account metadata",
	)

	slackCard := cardSectionByType(body, "slack")
	if !strings.Contains(slackCard, `<span class="badge badge-sm badge-success">Connected</span>`) {
		t.Fatal("expected slack connected badge in details section")
	}
	assertIndexOrder(
		t,
		slackCard,
		`Authorized users:</span>`,
		`<span class="badge badge-sm badge-success">Connected</span>`,
		"expected slack status badge below authorized users",
	)

	telegramCard := cardSectionByType(body, "telegram")
	if !strings.Contains(telegramCard, `<span class="badge badge-warning badge-sm">Not Connected</span>`) && !strings.Contains(telegramCard, `<span class="badge badge-success badge-sm">Connected</span>`) {
		t.Fatal("expected telegram status badge in details section")
	}
	if strings.Contains(telegramCard, `<span class="badge badge-warning badge-sm">Not Connected</span>`) {
		assertIndexOrder(
			t,
			telegramCard,
			`Authorized users:</span>`,
			`<span class="badge badge-warning badge-sm">Not Connected</span>`,
			"expected telegram status badge below authorized users",
		)
	}
	if strings.Contains(telegramCard, `<span class="badge badge-success badge-sm">Connected</span>`) {
		assertIndexOrder(
			t,
			telegramCard,
			`Authorized users:</span>`,
			`<span class="badge badge-success badge-sm">Connected</span>`,
			"expected telegram status badge below authorized users",
		)
	}
}

func TestChannelsPagePasswordToggleButtonStaysFixedOnActive(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	if strings.Contains(body, "translate(0.25rem, -50%)") {
		t.Fatal("password toggle active state still shifts horizontally")
	}

	if !strings.Contains(body, "translate(0, -50%) !important") {
		t.Fatal("expected password toggle active/focus transform to keep fixed position")
	}

	if !strings.Contains(body, `onclick="togglePasswordVisibility('channel_telegram_token', this)"`) {
		t.Fatal("expected password visibility toggle onclick handler")
	}

	if !strings.Contains(body, `class="eye-open`) || !strings.Contains(body, `class="eye-closed`) {
		t.Fatal("expected both eye icons for show/hide token toggle")
	}
}
