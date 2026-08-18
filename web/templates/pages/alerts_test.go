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
	for _, required := range []string{"system-update-digest", "view.imageRef", "setInterval(refreshSystemUpdateCard, 1000)", "window.openVibelyNormalizeSystemUpdateSnapshot(data)", "view.hidden", "view.showCancel", "window.openVibelyHandleSystemUpdateSnapshot(data)", "window.openVibelyHandleSystemUpdateSnapshot(null)"} {
		if !strings.Contains(html, required) {
			t.Fatalf("system update UI missing %q", required)
		}
	}
	for _, duplicatedRule := range []string{"data.current_version === available", "localPackagedReady", "data.release.apply_supported", "data.distribution === 'hosted' ||"} {
		if strings.Contains(html, duplicatedRule) {
			t.Fatalf("system update UI still duplicates normalized rule %q", duplicatedRule)
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
		`view.actionable`,
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
	alerts := []models.AlertSummary{{ID: "alert-1", Title: "Disk full", ProjectID: "project-1"}}

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

func TestAlertsContent_ListOmitsBodyAndMetadataAndLazyLoadsDetail(t *testing.T) {
	createdAt := time.Date(2026, time.August, 4, 9, 8, 7, 0, time.UTC)
	largeBody := strings.Repeat("Compiler diagnostics line with secret payload ", 200)
	alerts := []models.AlertSummary{
		{
			ID: "operational-1", ProjectID: "proj-1", Type: models.AlertTaskFailed,
			Severity: models.SeverityError, Title: "Build failed", Message: "Compiler exited",
			Source: "task-runner", DecisionState: models.AlertDecisionNotRequired,
			ProcessingState: models.AlertProcessingNotApplicable, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		{
			ID: "notification-1", ProjectID: "proj-1", Type: models.AlertCustom,
			Severity: models.SeverityWarning, Title: "Review change", Message: "Approval requested",
			Source: "review-agent", DecisionState: models.AlertDecisionPending,
			ProcessingState: models.AlertProcessingUnclaimed, CreatedAt: createdAt, UpdatedAt: createdAt,
		},
	}

	var buf bytes.Buffer
	if err := AlertsContent(alerts, "proj-1", 1).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render alerts content: %v", err)
	}
	html := buf.String()

	// The list fragment must lazily reference the per-alert detail endpoint and
	// must never embed the full body or metadata in the DOM.
	for _, required := range []string{
		`<summary class="cursor-pointer text-sm font-medium">Inspect alert</summary>`,
		`<summary class="cursor-pointer text-sm font-medium">Inspect notification</summary>`,
		`data-alert-detail-url="/alerts/operational-1/details?project_id=proj-1"`,
		`data-alert-detail-url="/alerts/notification-1/details?project_id=proj-1"`,
		`ontoggle="loadAlertDetail(this)"`,
		`data-alert-detail-container`,
		`function loadAlertDetail(details)`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("lazy list markup missing %q", required)
		}
	}
	for _, forbidden := range []string{
		largeBody,
		"secret payload",
		`<pre class="hidden" data-alert-copy-text aria-hidden="true">`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("list fragment unexpectedly embedded detail content %q", forbidden)
		}
	}
}

func TestAlertDetail_IncludesBodyAndMetadataForSelectedAlert(t *testing.T) {
	createdAt := time.Date(2026, time.August, 4, 9, 8, 7, 0, time.UTC)
	alert := models.Alert{
		ID: "operational-1", ProjectID: "proj-1", IdempotencyKey: "hidden-idempotency-key",
		Type: models.AlertTaskFailed, Severity: models.SeverityError, Title: "Build failed",
		Message: "Compiler exited", Body: "Compiler diagnostics\nline 2", Source: "task-runner",
		Metadata:      map[string]any{"attempt": float64(2)},
		DecisionState: models.AlertDecisionNotRequired, ProcessingState: models.AlertProcessingNotApplicable,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}

	var buf bytes.Buffer
	if err := AlertDetail(alert).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render alert detail: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`data-alert-detail-loaded`,
		`Compiler diagnostics`,
		`attempt`,
		`data-alert-copy`,
		`aria-label="Copy inspected alert body"`,
		`onclick="copyAlertDetails(this)"`,
		`data-alert-copy-text`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("alert detail fragment missing %q", required)
		}
	}

	payloadStart := strings.Index(html, `<pre class="hidden" data-alert-copy-text aria-hidden="true">`)
	payloadEnd := strings.Index(html[payloadStart:], `</pre>`)
	if payloadStart < 0 || payloadEnd < 0 {
		t.Fatal("copy payload missing")
	}
	payload := html[payloadStart : payloadStart+payloadEnd]
	for _, forbidden := range []string{"OpenVibely", "ID:", "Title:", "hidden-idempotency-key"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("body-only copy payload unexpectedly contains %q", forbidden)
		}
	}
}

func TestAlertDetail_EmptyAlertOmitsCopyControl(t *testing.T) {
	alert := models.Alert{ID: "empty-body-1", Title: "No body", Message: "Summary only"}
	var buf bytes.Buffer
	if err := AlertDetail(alert).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render alert detail: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, `data-alert-copy`) {
		t.Fatal("alert without a body must not render a copy control")
	}
	if !strings.Contains(html, "No additional detail.") {
		t.Fatal("empty detail should render a placeholder")
	}
}

func TestAlertsContent_CardsConformToNarrowViewport(t *testing.T) {
	longText := strings.Repeat("SuperLongUnbrokenAlertToken", 8)
	alerts := []models.AlertSummary{{ID: "alert-1", Title: longText, Message: longText, ProjectID: "project-1"}}

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
