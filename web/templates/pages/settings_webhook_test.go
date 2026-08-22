package pages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestSettingsContent_WebhookCardsOmitEditOnlyPayloads(t *testing.T) {
	largeSystem := "system-secret-payload-" + strings.Repeat("s", 32*1024)
	largeTitle := "title-template-payload-" + strings.Repeat("t", 32*1024)
	largePrompt := "prompt-template-payload-" + strings.Repeat("p", 32*1024)
	secret := "webhook-secret-value-that-must-not-render"
	webhooks := []models.WebhookEndpoint{{
		ID:                 "wh-1",
		ProjectID:          "project-1",
		Name:               "PagerDuty Alerts",
		Enabled:            true,
		PathToken:          "token-1",
		Secret:             secret,
		SystemInstructions: largeSystem,
		TitleTemplate:      largeTitle,
		PromptTemplate:     largePrompt,
		DefaultPriority:    2,
	}}

	out := renderSettingsContentForWebhookTest(t, webhooks)
	for _, forbidden := range []string{secret, largeSystem, largeTitle, largePrompt, `data-webhook-secret=`, `data-webhook-system=`, `data-webhook-agent-ids=`} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("initial Settings HTML contained edit-only webhook payload/attribute %q", forbidden[:min(len(forbidden), 64)])
		}
	}
	for _, expected := range []string{`data-webhook-id="wh-1"`, `data-webhook-token="token-1"`, "PagerDuty Alerts", "openWebhookEditModal('wh-1')"} {
		if !strings.Contains(out, expected) {
			t.Fatalf("initial Settings HTML missing expected compact webhook content %q", expected)
		}
	}
}

func TestSettingsContent_WebhookCopyRoutesThroughSharedClipboardHelper(t *testing.T) {
	out := renderSettingsContentForWebhookTest(t, []models.WebhookEndpoint{{
		ID:              "wh-copy",
		ProjectID:       "project-1",
		Name:            "Copy Webhook",
		Enabled:         true,
		PathToken:       "token-copy",
		DefaultPriority: 2,
	}})

	for _, expected := range []string{
		`copyWebhookEndpointUrl('wh-copy', this)`,
		`copyWebhookAbsoluteUrl('webhook_url_display', this)`,
		`function writeToClipboardWithFeedback(text, button, successMessage, failureMessage)`,
		`window.openVibelyCopyText(text, {`,
		`successFeedback: { text: 'Copied' }`,
		`Webhook URL copied`,
		`Failed to copy webhook URL`,
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("settings webhook copy script missing shared-helper contract %q", expected)
		}
	}
	for _, forbidden := range []string{`navigator.clipboard.writeText`, `document.execCommand('copy')`} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("settings webhook copy must not render local clipboard implementation %q", forbidden)
		}
	}
}

func TestSettingsContent_WebhookEditBlocksSaveUntilDetailsHydrate(t *testing.T) {
	out := renderSettingsContentForWebhookTest(t, []models.WebhookEndpoint{{
		ID:              "wh-1",
		ProjectID:       "project-1",
		Name:            "PagerDuty Alerts",
		Enabled:         true,
		PathToken:       "token-1",
		DefaultPriority: 2,
	}})

	for _, expected := range []string{
		`data-webhook-mode="create"`,
		`data-webhook-detail-ready="true"`,
		`onsubmit="return submitWebhookForm(event)"`,
		`id="webhook_submit_button"`,
		`function submitWebhookForm(event)`,
		`form.dataset.webhookMode = 'edit'`,
		`form.dataset.webhookDetailReady = 'false'`,
		`setWebhookSubmitState(false, 'Loading Webhook…')`,
		`if (form && form.dataset.webhookMode === 'edit' && form.dataset.webhookDetailReady !== 'true')`,
		`form.dataset.webhookDetailReady = 'true'`,
		`setWebhookSubmitState(true, 'Save Webhook')`,
		`setWebhookSubmitState(false, 'Load failed')`,
		`params.agent_ids = selectedWebhookAgentIDs.join(',')`,
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("rendered Settings HTML missing webhook edit hydration guard %q", expected)
		}
	}
	for _, forbidden := range []string{
		`selectedWebhookAgentIDsCSV`,
		`webhook_agent_ids')`,
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("rendered Settings HTML contains stale webhook agent helper reference %q", forbidden)
		}
	}
}

func TestSettingsContent_WebhookCards200InitialHTMLUnder1MB(t *testing.T) {
	large := strings.Repeat("x", 32*1024)
	secret := strings.Repeat("s", 128)
	webhooks := make([]models.WebhookEndpoint, 200)
	for i := range webhooks {
		webhooks[i] = models.WebhookEndpoint{
			ID:                 fmt.Sprintf("wh-%03d", i),
			ProjectID:          "project-1",
			Name:               fmt.Sprintf("Webhook %03d", i),
			Enabled:            i%2 == 0,
			PathToken:          fmt.Sprintf("token-%03d", i),
			Secret:             secret,
			SystemInstructions: large,
			TitleTemplate:      large,
			PromptTemplate:     large,
			DefaultPriority:    (i % 4) + 1,
		}
	}

	out := renderSettingsContentForWebhookTest(t, webhooks)
	if len(out) >= 1_000_000 {
		t.Fatalf("rendered Settings HTML = %d bytes, want under 1 MB", len(out))
	}
	if strings.Contains(out, large) || strings.Contains(out, secret) {
		t.Fatalf("rendered Settings HTML contains large edit-only webhook payload")
	}
}

func BenchmarkSettingsContentWebhookCards200(b *testing.B) {
	large := strings.Repeat("x", 32*1024)
	webhooks := make([]models.WebhookEndpoint, 200)
	for i := range webhooks {
		webhooks[i] = models.WebhookEndpoint{
			ID:                 fmt.Sprintf("wh-%03d", i),
			ProjectID:          "project-1",
			Name:               fmt.Sprintf("Webhook %03d", i),
			Enabled:            true,
			PathToken:          fmt.Sprintf("token-%03d", i),
			Secret:             strings.Repeat("s", 128),
			SystemInstructions: large,
			TitleTemplate:      large,
			PromptTemplate:     large,
			DefaultPriority:    2,
		}
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		if err := settingsContentForWebhookTest(webhooks).Render(context.Background(), &buf); err != nil {
			b.Fatalf("render failed: %v", err)
		}
		if i == 0 {
			b.ReportMetric(float64(buf.Len()), "html_bytes")
		}
	}
}

func renderSettingsContentForWebhookTest(t *testing.T, webhooks []models.WebhookEndpoint) string {
	t.Helper()
	var buf bytes.Buffer
	if err := settingsContentForWebhookTest(webhooks).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

func settingsContentForWebhookTest(webhooks []models.WebhookEndpoint) interface {
	Render(context.Context, io.Writer) error
} {
	view := defaultChannelsSettingsView("project-1")
	view.Webhooks = webhooks
	return SettingsContent(view)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
