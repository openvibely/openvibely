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
			deleteRoute:   "/channels/slack/authorized-users/slack-row?project_id=" + projectID,
			inputName:     "slack_user_id",
			inputType:     "text",
			emptyState:    "No authorized users configured.",
			confirmation:  "Remove this authorized user?",
			emptyList:     SlackAuthorizedUsersList(nil, projectID),
			populatedList: SlackAuthorizedUsersList([]models.SlackAuthorizedUser{{ID: "slack-row", SlackUserID: "U12345678", DisplayName: "Slack User"}}, projectID),
		},
		{
			name:          "Discord",
			containerID:   "discord-authorized-users",
			addEndpoint:   "/channels/discord/authorized-users",
			deleteRoute:   "/channels/discord/authorized-users/discord-row?project_id=" + projectID,
			inputName:     "discord_user_id",
			inputType:     "text",
			emptyState:    "No authorized users configured. Access is denied until authorized users are added.",
			confirmation:  "Remove this authorized user?",
			emptyList:     DiscordAuthorizedUsersList(nil, projectID),
			populatedList: DiscordAuthorizedUsersList([]models.DiscordAuthorizedUser{{ID: "discord-row", DiscordUserID: "123456789012345678", DisplayName: "Discord User"}}, projectID),
		},
		{
			name:          "Telegram",
			containerID:   "telegram-authorized-users",
			addEndpoint:   "/channels/telegram/authorized-users",
			deleteRoute:   "/channels/telegram/authorized-users/telegram-row?project_id=" + projectID,
			inputName:     "user_id_or_username",
			inputType:     "text",
			emptyState:    "No authorized users configured. Access is denied until authorized users are added.",
			confirmation:  "Remove this authorized user?",
			emptyList:     TelegramAuthorizedUsersList(nil, projectID),
			populatedList: TelegramAuthorizedUsersList([]models.TelegramAuthorizedUser{{ID: "telegram-row", TelegramUserID: 987654321, TelegramUsername: "telegram_user", DisplayName: "Telegram User"}}, projectID),
		},
		{
			name:          "Email",
			containerID:   "email-authorized-senders",
			addEndpoint:   "/channels/email/authorized-senders",
			deleteRoute:   "/channels/email/authorized-senders/email-row?project_id=" + projectID,
			inputName:     "authorized_email_address",
			inputType:     "email",
			emptyState:    "No authorized senders configured. Access is denied until senders are added.",
			confirmation:  "Remove this authorized sender?",
			emptyList:     EmailAuthorizedSendersList(nil, projectID),
			populatedList: EmailAuthorizedSendersList([]models.EmailAuthorizedSender{{ID: "email-row", EmailAddress: "person@example.com", DisplayName: "Email Sender"}}, projectID),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			emptyHTML := renderChannelAuthorizationComponent(t, tc.emptyList)
			assertChannelAuthorizationContains(t, emptyHTML,
				fmt.Sprintf(`id="%s"`, tc.containerID),
				tc.emptyState,
				`name="project_id"`,
				fmt.Sprintf(`value="%s"`, projectID),
				fmt.Sprintf(`type="%s"`, tc.inputType),
				fmt.Sprintf(`name="%s"`, tc.inputName),
				fmt.Sprintf(`hx-post="%s"`, tc.addEndpoint),
				fmt.Sprintf(`hx-target="#%s"`, tc.containerID),
				`hx-swap="outerHTML"`,
				fmt.Sprintf(`hx-include="#%s-add-controls"`, tc.containerID),
			)

			populatedHTML := renderChannelAuthorizationComponent(t, tc.populatedList)
			assertChannelAuthorizationContains(t, populatedHTML,
				fmt.Sprintf(`hx-delete="%s"`, tc.deleteRoute),
				fmt.Sprintf(`hx-confirm="%s"`, tc.confirmation),
			)
			if got := strings.Count(populatedHTML, `hx-target=`); got != 2 {
				t.Fatalf("expected only Add and Remove HTMX targets, got %d target attributes", got)
			}
			if got := strings.Count(populatedHTML, fmt.Sprintf(`hx-target="#%s"`, tc.containerID)); got != 2 {
				t.Fatalf("expected Add and Remove to target only #%s, got %d modal-fragment targets", tc.containerID, got)
			}
			if got := strings.Count(populatedHTML, `hx-swap="outerHTML"`); got != 2 {
				t.Fatalf("expected Add and Remove to use modal-scoped outerHTML swaps, got %d swap attributes", got)
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
