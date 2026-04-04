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
	}{
		{name: "not configured", status: service.GitHubConnectionStatus{Configured: false}, expectLabel: "Not Configured"},
		{name: "not connected", status: service.GitHubConnectionStatus{Configured: true, Connected: false}, expectLabel: "Not Connected"},
		{name: "connected", status: service.GitHubConnectionStatus{Configured: true, Connected: true, InstallationID: "123", AccountLogin: "openvibely"}, expectLabel: "Connected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			hasGitHubChannel := tt.status.Configured || tt.status.Connected
			err := SettingsContent("", false, nil, nil, "default", true, tt.status, service.GitHubAuthModePAT, "", "", "private-key-value", "pat-value", false, false, service.SlackConnectionStatus{}, "", "", "", "", service.SlackBotTokenSourceOAuth, false, false, false, false, true, false, hasGitHubChannel, false).Render(context.Background(), &buf)
			if err != nil {
				t.Fatalf("render failed: %v", err)
			}
			out := buf.String()
			if tt.name == "not configured" {
				if strings.Contains(out, "Not Configured") {
					t.Fatal("did not expect GitHub status badge when channel is not added")
				}
				if !strings.Contains(out, "GitHub") {
					t.Fatal("expected GitHub option in Add Channel menu")
				}
				return
			}
			if !strings.Contains(out, tt.expectLabel) {
				t.Fatalf("expected status label %q", tt.expectLabel)
			}
			if !strings.Contains(out, "Remove") {
				t.Fatal("expected GitHub kebab remove action for existing channel")
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
