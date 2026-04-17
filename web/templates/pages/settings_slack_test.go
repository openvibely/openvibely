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
		"default",
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
			false,
			false,
			true,
			nil,
			nil,
			nil,
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
