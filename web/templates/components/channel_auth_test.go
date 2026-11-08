package components

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
)

func TestChannelAuthorizationListWrappersPreserveHTMXContracts(t *testing.T) {
	const projectID = "project-27"

	tests := []struct {
		name          string
		containerID   string
		addEndpoint   string
		deleteRoute   string
		inputName     string
		inputType     string
		emptyState    string
		confirmation  string
		emptyList     templ.Component
		populatedList templ.Component
	}{
		{
			name:          "Slack",
			containerID:   "slack-authorized-users",
			addEndpoint:   "/channels/slack/authorized-users",
			deleteRoute:   "/channels/slack/authorized-users/slack-row",
			inputName:     "slack_user_id",
			inputType:     "text",
			emptyState:    "No authorized users configured.",
			confirmation:  "Remove this authorized user?",
			emptyList:     SlackAuthorizedUsersList(nil, projectID),
			populatedList: SlackAuthorizedUsersList([]models.SlackAuthorizedUser{{ID: "slack-row", ProjectID: projectID, SlackUserID: "U12345678", DisplayName: "Slack User"}}, projectID),
		},
		{
			name:          "Discord",
			containerID:   "discord-authorized-users",
			addEndpoint:   "/channels/discord/authorized-users",
			deleteRoute:   "/channels/discord/authorized-users/discord-row",
			inputName:     "discord_user_id",
			inputType:     "text",
			emptyState:    "No authorized users configured. Access is denied until authorized users are added.",
			confirmation:  "Remove this authorized user?",
			emptyList:     DiscordAuthorizedUsersList(nil, projectID),
			populatedList: DiscordAuthorizedUsersList([]models.DiscordAuthorizedUser{{ID: "discord-row", ProjectID: projectID, DiscordUserID: "123456789012345678", DisplayName: "Discord User"}}, projectID),
		},
		{
			name:          "Telegram",
			containerID:   "telegram-authorized-users",
			addEndpoint:   "/channels/telegram/authorized-users",
			deleteRoute:   "/channels/telegram/authorized-users/telegram-row",
			inputName:     "user_id_or_username",
			inputType:     "text",
			emptyState:    "No authorized users configured. Access is denied until authorized users are added.",
			confirmation:  "Remove this authorized user?",
			emptyList:     TelegramAuthorizedUsersList(nil, projectID),
			populatedList: TelegramAuthorizedUsersList([]models.TelegramAuthorizedUser{{ID: "telegram-row", ProjectID: projectID, TelegramUserID: 987654321, TelegramUsername: "telegram_user", DisplayName: "Telegram User"}}, projectID),
		},
		{
			name:          "Email",
			containerID:   "email-authorized-senders",
			addEndpoint:   "/channels/email/authorized-senders",
			deleteRoute:   "/channels/email/authorized-senders/email-row",
			inputName:     "authorized_email_address",
			inputType:     "email",
			emptyState:    "No authorized senders configured. Access is denied until senders are added.",
			confirmation:  "Remove this authorized sender?",
			emptyList:     EmailAuthorizedSendersList(nil, projectID),
			populatedList: EmailAuthorizedSendersList([]models.EmailAuthorizedSender{{ID: "email-row", ProjectID: projectID, EmailAddress: "person@example.com", DisplayName: "Email Sender"}}, projectID),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emptyHTML := renderChannelAuthorizationComponent(t, tc.emptyList)
			assertChannelAuthorizationContains(t, emptyHTML,
				fmt.Sprintf(`id="%s"`, tc.containerID),
				tc.emptyState,
				`name="application_user_id"`,
				fmt.Sprintf(`<input type="%s" name="%s"`, tc.inputType, tc.inputName),
				fmt.Sprintf(`hx-post="%s"`, tc.addEndpoint),
				fmt.Sprintf(`hx-target="#%s"`, tc.containerID),
				`hx-swap="outerHTML"`,
				fmt.Sprintf(`hx-include="#%s-add-controls"`, tc.containerID),
			)
			assertChannelAuthorizationMutationScope(t, emptyHTML, tc.containerID, 1)
			if got := strings.Count(emptyHTML, fmt.Sprintf(`hx-post="%s"`, tc.addEndpoint)); got != 1 {
				t.Fatalf("expected exactly one Add endpoint %s, got %d", tc.addEndpoint, got)
			}

			populatedHTML := renderChannelAuthorizationComponent(t, tc.populatedList)
			assertChannelAuthorizationContains(t, populatedHTML,
				fmt.Sprintf(`hx-delete="%s"`, tc.deleteRoute),
				fmt.Sprintf(`hx-confirm="%s"`, tc.confirmation),
			)
			mutationCount := 3
			assertChannelAuthorizationMutationScope(t, populatedHTML, tc.containerID, mutationCount)
			if got := strings.Count(populatedHTML, fmt.Sprintf(`hx-delete="%s"`, tc.deleteRoute)); got != 1 {
				t.Fatalf("expected exactly one Remove endpoint %s, got %d", tc.deleteRoute, got)
			}
		})
	}
}

func TestTelegramAuthorizedUsersListPreservesUsernameAndUserIDRendering(t *testing.T) {
	html := renderChannelAuthorizationComponent(t, TelegramAuthorizedUsersList([]models.TelegramAuthorizedUser{
		{ID: "username-and-id", TelegramUsername: "both_identity", TelegramUserID: 123456789, DisplayName: "Both"},
		{ID: "username-only", TelegramUsername: "username_only", DisplayName: "Username"},
		{ID: "id-only", TelegramUserID: 987654321, DisplayName: "ID"},
	}, "project-27"))

	assertChannelAuthorizationContains(t, html, "@both_identity", "ID: 123456789", "@username_only", "ID: 987654321")
	if strings.Contains(html, "ID: 0") {
		t.Fatal("Telegram rows without a numeric user ID must not render ID: 0")
	}
}

func assertChannelAuthorizationMutationScope(t *testing.T, html, containerID string, mutationCount int) {
	t.Helper()
	rootPrefix := fmt.Sprintf(`<div id="%s">`, containerID)
	if !strings.HasPrefix(html, rootPrefix) || !strings.HasSuffix(html, `</div>`) {
		t.Fatalf("authorization mutations must render inside the single root %s", rootPrefix)
	}
	if got := strings.Count(html, `hx-target=`); got != mutationCount {
		t.Fatalf("expected %d modal-fragment targets, got %d", mutationCount, got)
	}
	if got := strings.Count(html, fmt.Sprintf(`hx-target="#%s"`, containerID)); got != mutationCount {
		t.Fatalf("expected all %d mutations to target only #%s, got %d", mutationCount, containerID, got)
	}
	if got := strings.Count(html, `hx-swap="outerHTML"`); got != mutationCount {
		t.Fatalf("expected all %d mutations to use modal-scoped outerHTML swaps, got %d", mutationCount, got)
	}
}

func renderChannelAuthorizationComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render authorization component: %v", err)
	}
	return buf.String()
}

func assertChannelAuthorizationContains(t *testing.T, html string, snippets ...string) {
	t.Helper()
	for _, snippet := range snippets {
		if !strings.Contains(html, snippet) {
			t.Errorf("rendered authorization component missing %q", snippet)
		}
	}
}

func TestSlackIdentityBindingControls(t *testing.T) {
	var output bytes.Buffer
	err := SlackAuthorizedUsersList([]models.SlackAuthorizedUser{{ID: "binding-row", ProjectID: "project-27", SlackUserID: "U123"}}, "project-27").Render(context.Background(), &output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`hx-post="/channels/slack/senders/binding-row"`, `name="team_id"`, `name="application_user_id"`, `hx-include="#slack-authorized-users-binding-row"`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("missing binding control %q", want)
		}
	}
}

func TestDiscordIdentityBindingControls(t *testing.T) {
	ctx := models.WithChannelSenderOptions(context.Background(), models.ChannelSenderOptions{Authenticated: true, Actor: models.Principal{UserID: "admin", Role: "admin"}, Users: []models.OrgUser{{Principal: models.Principal{UserID: "user-1", Username: "alice", DisplayName: "Alice"}}}})
	var output bytes.Buffer
	err := DiscordAuthorizedUsersList([]models.DiscordAuthorizedUser{{ID: "row", DiscordUserID: "123", ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: "user-1"}}}, "project").Render(ctx, &output)
	if err != nil {
		t.Fatal(err)
	}
	html := output.String()
	assertChannelAuthorizationContains(t, html, `hx-post="/channels/discord/senders/row"`, `value="user-1" selected`, `Alice`)
	if strings.Contains(html, `name="project_id"`) || strings.Contains(html, `data-project-id=`) {
		t.Fatal("global sender settings included a project scope")
	}
}
