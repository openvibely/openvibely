package pages

import (
	"bytes"
	"context"
	"encoding/base64"
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
	for _, required := range []string{"system-update-digest", "view.imageRef", "window.openVibelyRenderSystemUpdateCard = renderSystemUpdateCard", "window.refreshGlobalSystemUpdateIndicators()", "window.openVibelyNormalizeSystemUpdateSnapshot(data)", "view.hidden", "view.showCancel", "window.openVibelyHandleSystemUpdateSnapshot(data)", "window.openVibelyHandleSystemUpdateSnapshot(null)"} {
		if !strings.Contains(html, required) {
			t.Fatalf("system update UI missing %q", required)
		}
	}
	if strings.Contains(html, "setInterval(refreshSystemUpdateCard, 1000)") {
		t.Fatal("Alerts page should use the shared system update poll instead of polling every second")
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
		base64.StdEncoding.EncodeToString([]byte(largeBody)),
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("list fragment unexpectedly embedded detail content %q", forbidden)
		}
	}
}

func decodedAlertCopyBody(t *testing.T, detailHTML string) string {
	t.Helper()
	const prefix = `data-alert-copy-base64="`
	start := strings.Index(detailHTML, prefix)
	if start < 0 {
		t.Fatal("encoded copy payload missing")
	}
	start += len(prefix)
	end := strings.IndexByte(detailHTML[start:], '"')
	if end < 0 {
		t.Fatal("encoded copy payload is unterminated")
	}
	decoded, err := base64.StdEncoding.DecodeString(detailHTML[start : start+end])
	if err != nil {
		t.Fatalf("decode copy payload: %v", err)
	}
	return string(decoded)
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

	payload := decodedAlertCopyBody(t, html)
	if payload != alert.Body {
		t.Fatalf("body-only copy payload changed: got %q, want %q", payload, alert.Body)
	}
	for _, forbidden := range []string{"OpenVibely", "ID:", "Title:", "hidden-idempotency-key"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("body-only copy payload unexpectedly contains %q", forbidden)
		}
	}
}

func TestAlertDetail_UsesSharedMarkdownSurfaceWithoutChangingCopySource(t *testing.T) {
	body := "# Heading\r\n\r\n**emphasis**\r\n\r\n- first\r\n- second\r\n\r\n[link](https://example.test)\r\n\r\n```go\r\nline 1\r\nline 2\r\n```\r\n\r\n<img src=x onerror=alert(1)>"
	alert := models.Alert{
		ID: "markdown-detail-1", ProjectID: "project-1", Title: "Markdown detail", Body: body,
		Metadata:      map[string]any{"attempt": float64(2)},
		DecisionState: models.AlertDecisionNotRequired, ProcessingState: models.AlertProcessingNotApplicable,
	}

	var detail bytes.Buffer
	if err := AlertDetail(alert).Render(context.Background(), &detail); err != nil {
		t.Fatalf("render alert detail: %v", err)
	}
	detailHTML := detail.String()
	for _, required := range []string{
		`data-alert-detail-loaded`,
		`class="chat-markdown"`,
		`data-alert-markdown`,
		`data-raw-content=`,
		`data-alert-copy-text`,
		`attempt`,
	} {
		if !strings.Contains(detailHTML, required) {
			t.Fatalf("Markdown detail fragment missing %q", required)
		}
	}
	if strings.Contains(detailHTML, `class="whitespace-pre-wrap break-words text-sm"`) {
		t.Fatal("detail body must not be emitted as a second plain-text rendering")
	}

	if got := decodedAlertCopyBody(t, detailHTML); got != body {
		t.Fatalf("copy source changed the raw body: got %q, want %q", got, body)
	}

	var content bytes.Buffer
	if err := AlertsContent(nil, "project-1", 0).Render(context.Background(), &content); err != nil {
		t.Fatalf("render Alerts content: %v", err)
	}
	contentHTML := content.String()
	for _, required := range []string{
		`function hydrateAlertMarkdown(container)`,
		`window.renderChatMarkdown(raw)`,
		`window.addCodeCopyButtons(markdown)`,
		`hydrateAlertMarkdown(container)`,
		`function decodeAlertCopyText(copyText)`,
		`new TextDecoder('utf-8', {fatal: true}).decode(bytes)`,
	} {
		if !strings.Contains(contentHTML, required) {
			t.Fatalf("Alerts detail hydration missing shared renderer contract %q", required)
		}
	}
	if strings.Contains(contentHTML, `marked.parse(`) || strings.Contains(contentHTML, `sanitizeChatHTML`) {
		t.Fatal("Alerts detail must delegate Markdown parsing and sanitization to the shared pipeline")
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

func TestAlertsContent_DecisionFilterRendersSelectedStateAndSearchState(t *testing.T) {
	var filtered bytes.Buffer
	if err := AlertsContentPageWithFiltersAndSearch(nil, "project-1", 4, false, models.AlertDecisionPending, models.AlertProcessingFailed, "needle").Render(context.Background(), &filtered); err != nil {
		t.Fatalf("render filtered alerts content: %v", err)
	}
	filteredHTML := filtered.String()
	for _, required := range []string{
		`id="alerts-filter-form"`,
		`method="get"`,
		`data-alert-search-slot`,
		`class="w-full max-w-xs flex-none"`,
		`name="search"`,
		`value="needle"`,
		`data-card-search-initial="needle"`,
		`name="decision_state"`,
		`aria-label="Filter by decision state"`,
		`All decision states`,
		`data-card-pagination-preserve-params="decision_state,processing_state,search"`,
		`data-card-pagination-url="/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`hx-get="/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`value="pending" selected`,
		`No alerts match the selected filters.`,
	} {
		if !strings.Contains(filteredHTML, required) {
			t.Fatalf("filtered Alerts markup missing %q", required)
		}
	}
	for _, removed := range []string{
		`name="processing_state"`,
		`aria-label="Filter by processing state"`,
		`All processing states`,
	} {
		if strings.Contains(filteredHTML, removed) {
			t.Fatalf("Alerts markup should not contain the removed processing selector %q", removed)
		}
	}
	if strings.Contains(filteredHTML, "No alerts. You're all clear!") {
		t.Fatal("filtered empty Alerts result should not claim the project has no alerts")
	}

	var searchOnly bytes.Buffer
	if err := AlertsContentPageWithFiltersAndSearch(nil, "project-1", 0, false, "", "", "missing").Render(context.Background(), &searchOnly); err != nil {
		t.Fatalf("render search-only alerts content: %v", err)
	}
	searchOnlyHTML := searchOnly.String()
	for _, required := range []string{
		`value="missing"`,
		`data-card-search-initial="missing"`,
		`data-card-pagination-url="/alerts?project_id=project-1&amp;search=missing"`,
		`No alerts match the selected filters.`,
	} {
		if !strings.Contains(searchOnlyHTML, required) {
			t.Fatalf("search-only Alerts markup missing %q", required)
		}
	}
	if strings.Contains(searchOnlyHTML, "No alerts. You're all clear!") {
		t.Fatal("search-only empty Alerts result should not claim the project has no alerts")
	}

	var unfiltered bytes.Buffer
	if err := AlertsContentPageWithFilters(nil, "project-1", 0, false, "", "").Render(context.Background(), &unfiltered); err != nil {
		t.Fatalf("render unfiltered alerts content: %v", err)
	}
	if !strings.Contains(unfiltered.String(), "No alerts. You're all clear!") {
		t.Fatal("unfiltered empty Alerts result should retain the existing empty message")
	}
}

func TestAlertsContent_ActiveFilterURLsReachRowMutations(t *testing.T) {
	alert := models.AlertSummary{
		ID:              "alert-1",
		ProjectID:       "project-1",
		Title:           "Pending alert",
		DecisionState:   models.AlertDecisionPending,
		ProcessingState: models.AlertProcessingFailed,
	}
	var buf bytes.Buffer
	if err := AlertsContentPageWithFiltersAndSearch([]models.AlertSummary{alert}, "project-1", 1, false, models.AlertDecisionPending, models.AlertProcessingFailed, "needle").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render active Alerts content: %v", err)
	}
	html := buf.String()
	for _, required := range []string{
		`hx-post="/alerts/read-all?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`data-delete-url="/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`hx-post="/alerts/alert-1/approve?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`hx-post="/alerts/alert-1/reject?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`hx-post="/alerts/alert-1/read?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
		`data-delete-url="/alerts/alert-1?decision_state=pending&amp;processing_state=failed&amp;project_id=project-1&amp;search=needle"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("active Alerts markup missing mutation URL %q", required)
		}
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
