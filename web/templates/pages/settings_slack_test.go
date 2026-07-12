package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
)

func TestSettingsContent_RendersSlackMenuCardAndModal(t *testing.T) {
	var buf bytes.Buffer
	err := SettingsContent(
		"",
		false,
		nil,
		nil,
		nil,
		"default",
		true,
		true,
		service.GitHubConnectionStatus{},
		service.GitHubAuthModePAT,
		"",
		"",
		"",
		"",
		false,
		false,
		service.SlackConnectionStatus{Configured: true, Connected: true, TeamName: "OpenVibely"},
		"cid",
		"secret",
		"xapp-123",
		"xoxb-123",
		service.SlackBotTokenSourceManual,
		true,
		true,
		true,
		true,
		true,
		service.DiscordConnectionStatus{},
		"",
		true,
		service.EmailConnectionStatus{},
		nil,
		"",
		true,
		true,
		false,
		"60",
		false,
		false,
		true,
		false,
		false,
		nil,
		nil,
		nil,
		nil,
		false,
	).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Slack") {
		t.Fatal("expected Slack label to render")
	}
	if !strings.Contains(out, `data-channel-type="slack"`) {
		t.Fatal("expected active Slack card")
	}
	if !strings.Contains(out, `id="slack_config_modal"`) {
		t.Fatal("expected Slack config modal")
	}
	if !strings.Contains(out, `openSlackChannelFromMenu`) {
		t.Fatal("expected Slack menu open handler")
	}
	if !strings.Contains(out, `id="slack_bot_token_mode"`) {
		t.Fatal("expected Slack bot token source combobox")
	}
	if !strings.Contains(out, "beginning with https") {
		t.Fatal("expected Slack HTTPS OAuth guidance in modal")
	}
	if !strings.Contains(out, "Manual Override Token") {
		t.Fatal("expected manual token fallback guidance in modal")
	}
}
func TestSettingsContent_RendersDiscordCardIconAcrossGatewayStates(t *testing.T) {
	tests := []struct {
		name               string
		status             service.DiscordConnectionStatus
		expectGatewayBadge bool
	}{
		{
			name:               "gateway running",
			status:             service.DiscordConnectionStatus{Configured: true, Connected: true, Running: true},
			expectGatewayBadge: true,
		},
		{
			name:   "disconnected",
			status: service.DiscordConnectionStatus{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderDiscordSettingsContent(t, tt.status)
			cardStart := strings.Index(out, `data-channel-type="discord"`)
			if cardStart < 0 {
				t.Fatal("expected Discord channel card")
			}
			cardEnd := strings.Index(out[cardStart:], `data-channel-type="email"`)
			if cardEnd < 0 {
				cardEnd = len(out) - cardStart
			}
			discordCard := out[cardStart : cardStart+cardEnd]

			for _, expected := range []string{
				`class="font-bold flex items-center gap-2"`,
				`class="h-5 w-5"`,
				`fill="currentColor"`,
				`aria-hidden="true"`,
				`data-icon="discord-brand"`,
			} {
				if !strings.Contains(discordCard, expected) {
					t.Fatalf("expected Discord card to contain %q", expected)
				}
			}

			hasGatewayBadge := strings.Contains(discordCard, "Gateway running")
			if hasGatewayBadge != tt.expectGatewayBadge {
				t.Fatalf("Gateway running badge presence = %t, want %t", hasGatewayBadge, tt.expectGatewayBadge)
			}
		})
	}
}

func renderDiscordSettingsContent(t *testing.T, status service.DiscordConnectionStatus) string {
	t.Helper()

	var buf bytes.Buffer
	err := SettingsContent(
		"",
		false,
		nil,
		nil,
		nil,
		"default",
		true,
		true,
		service.GitHubConnectionStatus{},
		service.GitHubAuthModePAT,
		"",
		"",
		"",
		"",
		false,
		false,
		service.SlackConnectionStatus{},
		"",
		"",
		"",
		"",
		service.SlackBotTokenSourceOAuth,
		false,
		false,
		false,
		false,
		false,
		status,
		"",
		true,
		service.EmailConnectionStatus{},
		nil,
		"",
		true,
		true,
		false,
		"60",
		false,
		false,
		false,
		true,
		false,
		nil,
		nil,
		nil,
		nil,
		false,
	).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

func TestSettingsContent_RendersSystemLevelInboundAuthorizationCopy(t *testing.T) {
	var buf bytes.Buffer
	err := SettingsContent(
		"telegram-token",
		false,
		nil,
		nil,
		nil,
		"project-1",
		true,
		true,
		service.GitHubConnectionStatus{},
		service.GitHubAuthModePAT,
		"",
		"",
		"",
		"",
		false,
		false,
		service.SlackConnectionStatus{},
		"",
		"",
		"",
		"",
		service.SlackBotTokenSourceOAuth,
		false,
		false,
		false,
		false,
		true,
		service.DiscordConnectionStatus{},
		"",
		true,
		service.EmailConnectionStatus{},
		nil,
		"",
		true,
		true,
		false,
		"60",
		true,
		false,
		true,
		true,
		true,
		nil,
		nil,
		nil,
		nil,
		false,
	).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}

	out := buf.String()
	for _, expected := range []string{
		"Authorized Telegram users are system-level for this channel and can use Telegram across projects.",
		"Authorized Slack users are system-level for this channel and can use Slack across projects.",
		"Authorized Discord users are system-level for this channel and can use Discord across projects.",
		"Authorized email senders are system-level for this channel and can use Email across projects.",
		"Authorized Users control who can talk to OpenVibely. Outbound targets control where agents may send messages.",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected rendered settings to contain %q", expected)
		}
	}

	for _, stale := range []string{
		"Telegram users for this project",
		"Slack access to specific users for this project",
		"Discord access to specific users for this project",
		"Email access to specific senders for this project",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("did not expect project-scoped inbound authorization copy %q", stale)
		}
	}
}
