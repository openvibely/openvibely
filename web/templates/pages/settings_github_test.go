package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
)

func TestSettingsContent_RendersGitHubStatusVariants(t *testing.T) {
	tests := []struct {
		name        string
		status      service.GitHubConnectionStatus
		expectLabel string
		expectClass string
	}{
		{name: "not configured", status: service.GitHubConnectionStatus{Configured: false}, expectLabel: "Not Configured", expectClass: "badge-ghost"},
		{name: "not connected", status: service.GitHubConnectionStatus{Configured: true, Connected: false}, expectLabel: "Not Connected", expectClass: "badge-warning"},
		{name: "connected", status: service.GitHubConnectionStatus{Configured: true, Connected: true, InstallationID: "123", AccountLogin: "openvibely"}, expectLabel: "Connected", expectClass: "badge-success"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			view := defaultChannelsSettingsView("default")
			view.GitHubStatus = tt.status
			view.GitHubPrivateKeyValue = "private-key-value"
			view.GitHubPATValue = "pat-value"
			view.HasGitHubChannel = tt.status.Configured || tt.status.Connected
			err := SettingsContent(view).Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("render failed: %v", err)
			}
			out := buf.String()
			if !strings.Contains(out, `data-openvibely-page-title="Channels - OpenVibely"`) {
				t.Fatal("channels fragment missing authoritative title marker")
			}
			if tt.name == "not configured" {
				if strings.Contains(out, "Not Configured") {
					t.Fatal("did not expect GitHub status badge when channel is not added")
				}
				if !strings.Contains(out, "GitHub") {
					t.Fatal("expected GitHub option in Add Channel menu")
				}
				return
			}
			assertRenderedChannelBadge(t, out, tt.expectLabel, tt.expectClass)
			if !strings.Contains(out, "Delete") {
				t.Fatal("expected GitHub kebab delete action for existing channel")
			}
			if !strings.Contains(out, `onclick="togglePasswordVisibility('github_pat', this)"`) {
				t.Fatal("expected GitHub PAT visibility toggle")
			}
			if !strings.Contains(out, `onclick="toggleSecretTextareaVisibility('github_app_private_key', this)"`) {
				t.Fatal("expected GitHub private key visibility toggle")
			}
			if !strings.Contains(out, `value="pat-value"`) {
				t.Fatal("expected stored PAT value to be prefilled in edit dialog")
			}
			if !strings.Contains(out, "private-key-value") {
				t.Fatal("expected stored private key value to be prefilled in edit dialog")
			}
		})
	}
}

func TestChannelConnectionStatusBadgeMapping(t *testing.T) {
	tests := []struct {
		name        string
		configured  bool
		connected   bool
		options     channelStatusBadgeOptions
		expectLabel string
		expectClass string
	}{
		{name: "not configured", expectLabel: "Not Configured", expectClass: "badge-ghost"},
		{name: "connected", configured: true, connected: true, expectLabel: "Connected", expectClass: "badge-success"},
		{name: "configured offline", configured: true, expectLabel: "Not Connected", expectClass: "badge-warning"},
		{name: "custom configured label", configured: true, options: channelStatusBadgeOptions{ConfiguredLabel: "Gateway Offline"}, expectLabel: "Gateway Offline", expectClass: "badge-warning"},
		{name: "custom not configured label and class", options: channelStatusBadgeOptions{NotConfiguredLabel: "Not Connected", NotConfiguredClass: "badge-warning"}, expectLabel: "Not Connected", expectClass: "badge-warning"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			badge := channelConnectionStatusBadge(tt.configured, tt.connected, tt.options)
			if badge.Label != tt.expectLabel || badge.Class != tt.expectClass {
				t.Fatalf("badge = {%q %q}, want {%q %q}", badge.Label, badge.Class, tt.expectLabel, tt.expectClass)
			}
		})
	}
}

func TestSettingsContent_RendersSharedChannelStatusBadgesAcrossCards(t *testing.T) {
	var buf bytes.Buffer
	view := defaultChannelsSettingsView("default")
	view.HasGitHubChannel = true
	view.HasSlackChannel = true
	view.HasTelegramChannel = true
	view.HasDiscordChannel = true
	view.HasEmailChannel = true
	view.GitHubStatus = service.GitHubConnectionStatus{Configured: true}
	view.SlackStatus = service.SlackConnectionStatus{Configured: true, Connected: true, TeamName: "OpenVibely"}
	view.IsBotRunning = false
	view.DiscordStatus = service.DiscordConnectionStatus{Configured: true}
	view.EmailStatus = service.EmailConnectionStatus{Configured: true, Address: "bot@example.com"}

	if err := SettingsContent(view).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	out := buf.String()

	cases := []struct {
		channelType string
		label       string
		class       string
		searchText  string
		extra       string
	}{
		{channelType: "github", label: "Not Connected", class: "badge-warning", searchText: `data-search-text="GitHub Not Connected"`},
		{channelType: "slack", label: "Connected", class: "badge-success", searchText: `data-search-text="Slack Connected"`},
		{channelType: "telegram", label: "Not Connected", class: "badge-warning", searchText: `data-search-text="Telegram Bot Not Running"`},
		{channelType: "discord", label: "Gateway Offline", class: "badge-warning", searchText: `data-search-text="Discord Gateway Offline"`, extra: "Gateway offline"},
		{channelType: "email", label: "Configured", class: "badge-warning", searchText: `data-search-text="Email bot@example.com"`},
	}

	for _, tt := range cases {
		t.Run(tt.channelType, func(t *testing.T) {
			card := channelCardMarkup(t, out, tt.channelType)
			assertRenderedChannelBadge(t, card, tt.label, tt.class)
			if !strings.Contains(out, tt.searchText) {
				t.Fatalf("expected search text %s", tt.searchText)
			}
			if tt.extra != "" && !strings.Contains(card, tt.extra) {
				t.Fatalf("expected card to contain provider-specific text %q", tt.extra)
			}
		})
	}
}

func channelCardMarkup(t *testing.T, out, channelType string) string {
	t.Helper()
	marker := `data-channel-type="` + channelType + `"`
	start := strings.Index(out, marker)
	if start < 0 {
		t.Fatalf("expected %s channel card", channelType)
	}
	remaining := out[start+len(marker):]
	end := len(out)
	if next := strings.Index(remaining, `data-channel-type="`); next >= 0 {
		end = start + len(marker) + next
	}
	return out[start:end]
}

func assertRenderedChannelBadge(t *testing.T, html, label, class string) {
	t.Helper()
	expected := `class="badge badge-sm ` + class + `">` + label + `</span>`
	if !strings.Contains(html, expected) {
		t.Fatalf("expected paired status badge %s", expected)
	}
}
