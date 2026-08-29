package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestSettingsContentRendersXPollingFailureAsOffline(t *testing.T) {
	var buf bytes.Buffer
	view := defaultChannelsSettingsView("project-1")
	view.HasXChannel = true
	view.XStatus = service.XConnectionStatus{Configured: true, Connected: false, Running: true, Username: "openvibely", LastError: "mention access revoked"}
	if err := SettingsContent(view).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Configured, polling offline") || !strings.Contains(output, "mention access revoked") {
		t.Fatalf("expected degraded X readiness, got %s", output)
	}
	if strings.Contains(output, `badge badge-success">Connected`) {
		t.Fatal("polling failure must not render a connected badge")
	}
}

func TestSettingsContentRendersXConfigurationWithoutSecrets(t *testing.T) {
	var buf bytes.Buffer
	view := defaultChannelsSettingsView("project-1")
	view.HasXChannel = true
	view.XStatus = service.XConnectionStatus{Configured: true, Connected: true, Running: true, Username: "openvibely"}
	view.XHasConsumerKey = true
	view.XHasConsumerSecret = true
	view.XHasAccessToken = true
	view.XHasAccessTokenSecret = true
	view.XPollIntervalSeconds = "30"
	view.XSendResponses = true
	view.XAuthorizedUsers = []models.XAuthorizedUser{{ID: "authorization-1", ProjectID: "project-1", XUserID: "12345", Username: "alice"}}

	if err := SettingsContent(view).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	output := buf.String()
	for _, expected := range []string{
		`data-channel-type="x"`,
		`id="x_config_modal"`,
		`action="/channels/x/configure"`,
		`name="x_consumer_secret"`,
		`type="password"`,
		`value="project-1"`,
		`ID 12345`,
		`@alice`,
		`value="x"`,
		`targetInput.value = 'me'`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected X settings output to contain %q", expected)
		}
	}
	for _, secret := range []string{"consumer-secret-value", "access-token-value", "access-token-secret-value"} {
		if strings.Contains(output, secret) {
			t.Fatalf("X settings must not render saved secret %q", secret)
		}
	}
}
