package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAlertsContent_SystemUpdateShowsExactDockerDigestAndLiveProgress(t *testing.T) {
	var buf bytes.Buffer
	if err := AlertsContent(nil, "project-1", 0).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, required := range []string{"system-update-digest", "image_ref", "setInterval(refreshSystemUpdateCard, 1000)", "data.current_version === available", "data.distribution === 'hosted' || (data.state !== 'waiting_for_idle'"} {
		if !strings.Contains(html, required) {
			t.Fatalf("system update UI missing %q", required)
		}
	}
}

func TestAlertsContent_SystemUpdateUsesSingleAcceptanceActionAndExplainsDrain(t *testing.T) {
	var buf bytes.Buffer
	if err := AlertsContent(nil, "project-1", 0).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, required := range []string{
		`id="system-update-accept"`,
		`Update OpenVibely`,
		`The replacement is downloaded and verified before approval. After you accept, OpenVibely waits for active work to finish, restarts, validates the new version, and rolls back automatically if needed.`,
		`data.staged`,
		`systemUpdateAction('apply')`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("single-action update UI missing %q", required)
		}
	}
	for _, removed := range []string{`id="system-update-stage"`, `id="system-update-apply"`, `Stage update`, `Apply update`} {
		if strings.Contains(html, removed) {
			t.Fatalf("two-step update UI still contains %q", removed)
		}
	}
}

func TestAlertsContent_DeleteActionsDoNotDependOnHxConfirm(t *testing.T) {
	alerts := []models.Alert{{ID: "alert-1", Title: "Disk full", ProjectID: "project-1"}}

	var buf bytes.Buffer
	err := AlertsContent(alerts, "project-1", 1).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render alerts content: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-delete-url="/alerts?project_id=project-1"`) {
		t.Fatal("expected delete-all alerts action to provide delete URL via dataset")
	}
	if !strings.Contains(html, `data-delete-url="/alerts/alert-1?project_id=project-1"`) {
		t.Fatal("expected per-alert delete action to provide delete URL via dataset")
	}
	if !strings.Contains(html, `onclick="return deleteAlertsFromDataset(this)"`) {
		t.Fatal("expected delete-all action to call dataset-based delete helper")
	}
	if !strings.Contains(html, `deleteAlertsFromDataset(this)`) {
		t.Fatal("expected per-alert action to call dataset-based delete helper")
	}
	if strings.Contains(html, `hx-confirm="Delete all alerts? This action cannot be undone."`) {
		t.Fatal("delete-all should not depend on hx-confirm in desktop webview")
	}
	if strings.Contains(html, `hx-confirm="Delete this alert?"`) {
		t.Fatal("per-alert delete should not depend on hx-confirm in desktop webview")
	}
	if strings.Contains(html, `Delete all alerts? This action cannot be undone.`) {
		t.Fatal("alerts delete-all should not include confirmation copy")
	}
	if strings.Contains(html, `Delete this alert?`) {
		t.Fatal("per-alert delete should not include confirmation copy")
	}
	if strings.Contains(html, `function confirmAndDeleteAlerts(`) {
		t.Fatal("alerts template should not define confirmation-based delete helper")
	}
	if !strings.Contains(html, `function deleteAlerts(url, target)`) {
		t.Fatal("expected direct delete helper in alerts template")
	}
	for _, want := range []string{
		`data-alert-scroll-anchor`,
		`data-alert-delete`,
		`aria-label="Delete alert Disk full"`,
		`htmx:beforeSwap`,
		`htmx:afterSettle`,
		`focus({preventScroll: true})`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected single-alert delete flow to preserve viewport and focus with %q", want)
		}
	}
}

func TestAlertsContent_InspectCopyCopiesOnlyBodyForAllAlertKinds(t *testing.T) {
	createdAt := time.Date(2026, time.August, 4, 9, 8, 7, 0, time.UTC)
	alerts := []models.Alert{
		{
			ID: "operational-1", ProjectID: "hidden-project", IdempotencyKey: "hidden-idempotency-key",
			Type: models.AlertTaskFailed, Severity: models.SeverityError, Title: "Build failed",
			Message: "Compiler exited", Body: "Compiler diagnostics\nline 2", Source: "task-runner",
			DecisionState: models.AlertDecisionNotRequired, ProcessingState: models.AlertProcessingNotApplicable,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		{
			ID: "notification-1", ProjectID: "hidden-project", IdempotencyKey: "hidden-idempotency-key",
			Type: models.AlertCustom, Severity: models.SeverityWarning, Title: "Review change",
			Message: "Approval requested", Body: "Check the proposed patch.", Source: "review-agent",
			Metadata: map[string]any{"attempt": float64(2)}, DecisionState: models.AlertDecisionPending,
			ProcessingState: models.AlertProcessingUnclaimed, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		{
			ID: "empty-body-1", Title: "No body", Message: "Summary only", Type: models.AlertCustom,
			Severity: models.SeverityInfo, Source: "system", DecisionState: models.AlertDecisionNotRequired,
			ProcessingState: models.AlertProcessingNotApplicable, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
	}

	var buf bytes.Buffer
	if err := AlertsContent(alerts, "hidden-project", 1).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render alerts content: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`<summary class="cursor-pointer text-sm font-medium">Inspect alert</summary>`,
		`<summary class="cursor-pointer text-sm font-medium">Inspect notification</summary>`,
		`class="relative mt-3 min-w-0 max-w-full min-h-6 pr-8"`,
		`class="btn btn-xs btn-ghost btn-square absolute right-0 top-0"`,
		`data-alert-copy`, `aria-label="Copy inspected alert body"`, `aria-label="Copy inspected notification body"`,
		`title="Copy body"`, `data-alert-copy-icon`, `data-alert-copy-success-icon`, `data-alert-copy-error-icon`,
		`data-alert-copy-feedback class="sr-only" aria-live="polite"`,
		`onclick="copyAlertDetails(this)"`, `data-alert-copy-text`,
		`navigator.clipboard.writeText(text)`, `Copied`, `Copy failed`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("inspect copy markup missing %q", required)
		}
	}

	if strings.Contains(html, `showToast('Alert body copied'`) || strings.Contains(html, `showToast('Could not copy alert body'`) {
		t.Fatal("copy feedback must remain local to the copy button without redundant toasts")
	}

	operationalStart := strings.Index(html, `id="alert-operational-1"`)
	notificationStart := strings.Index(html, `id="alert-notification-1"`)
	emptyStart := strings.Index(html, `id="alert-empty-body-1"`)
	if operationalStart < 0 || notificationStart <= operationalStart || emptyStart <= notificationStart {
		t.Fatal("rendered alert card boundaries missing")
	}
	operationalCard := html[operationalStart:notificationStart]
	notificationCard := html[notificationStart:emptyStart]
	emptyEnd := strings.Index(html[emptyStart:], `<div class="mt-2 text-xs opacity-60 break-words">`)
	if emptyEnd < 0 {
		t.Fatal("empty-body alert source row missing")
	}
	emptyCard := html[emptyStart : emptyStart+emptyEnd]
	for card, body := range map[string]string{
		operationalCard:  "Compiler diagnostics\nline 2",
		notificationCard: "Check the proposed patch.",
	} {
		payloadStart := strings.Index(card, `<pre class="hidden" data-alert-copy-text aria-hidden="true">`)
		payloadEnd := strings.Index(card[payloadStart:], `</pre>`)
		if payloadStart < 0 || payloadEnd < 0 {
			t.Fatal("copy payload missing")
		}
		payload := card[payloadStart : payloadStart+payloadEnd]
		if !strings.Contains(payload, body) {
			t.Fatalf("copy payload missing exact body %q", body)
		}
		for _, forbidden := range []string{"OpenVibely", "ID:", "Title:", "Message:", "Metadata:", "hidden-project", "hidden-idempotency-key"} {
			if strings.Contains(payload, forbidden) {
				t.Fatalf("body-only copy payload unexpectedly contains %q", forbidden)
			}
		}
	}
	if strings.Contains(emptyCard, `data-alert-copy`) {
		t.Fatal("alert without a body must not render a copy control")
	}
}

func TestAlertsContent_CardsConformToNarrowViewport(t *testing.T) {
	longText := strings.Repeat("SuperLongUnbrokenAlertToken", 8)
	alerts := []models.Alert{{ID: "alert-1", Title: longText, Message: longText, ProjectID: "project-1"}}

	var buf bytes.Buffer
	err := AlertsContent(alerts, "project-1", 1).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render alerts content: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		`id="alerts-container" class="h-full overflow-y-auto overflow-x-hidden max-w-full min-w-0"`,
		`class="grid grid-cols-1 gap-4 max-w-full min-w-0"`,
		`transition-all w-full min-w-0 max-w-full`,
		`card-body max-w-full min-w-0 p-4 sm:p-6`,
		`class="flex items-start gap-3 max-w-full min-w-0"`,
		`class="mt-0.5 flex-shrink-0"`,
		`class="flex-1 min-w-0 max-w-full"`,
		`font-semibold break-words [overflow-wrap:anywhere]`,
		`class="text-sm opacity-60 mt-1 break-words [overflow-wrap:anywhere]"`,
		`class="flex flex-shrink-0 items-center gap-1 self-start"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected alerts markup to include responsive class %q", want)
		}
	}
	if strings.Contains(html, `absolute top-4 right-4`) {
		t.Fatal("alert card controls should stay in normal top-row flow so long titles cannot render underneath them")
	}
	if strings.Contains(html, `pr-14`) || strings.Contains(html, `pr-20`) {
		t.Fatal("alert titles should not rely on fixed right padding to avoid top-right action overlap")
	}
	if strings.Contains(html, "overflow-wrap-anywhere") {
		t.Fatal("alerts should use Tailwind arbitrary overflow-wrap utility, not a non-existent class")
	}
}
