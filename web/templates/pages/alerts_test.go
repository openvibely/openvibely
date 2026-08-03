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
	for _, required := range []string{"system-update-digest", "image_ref", "setInterval(refreshSystemUpdateCard, 1000)", "data.distribution === 'hosted' || (data.state !== 'waiting_for_idle'"} {
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

func TestAlertsContent_InspectCopyIncludesSafeStructuredDetailsForAllAlertKinds(t *testing.T) {
	decidedAt := time.Date(2026, time.August, 4, 10, 11, 12, 0, time.UTC)
	claimedAt := decidedAt.Add(time.Minute)
	claimExpiresAt := claimedAt.Add(15 * time.Minute)
	implementationTaskID := "implementation-task-1"
	sourceTaskID := "source-task-1"
	executionID := "execution-1"
	operationalTaskID := "operational-task-1"
	createdAt := time.Date(2026, time.August, 4, 9, 8, 7, 0, time.UTC)
	updatedAt := createdAt.Add(2 * time.Hour)
	alerts := []models.Alert{
		{
			ID: "operational-1", ProjectID: "hidden-project", IdempotencyKey: "hidden-idempotency-key",
			TaskID: &operationalTaskID, ExecutionID: &executionID, Type: models.AlertTaskFailed,
			Severity: models.SeverityError, Title: "Build failed", Message: "Compiler exited",
			Source: "task-runner", DecisionState: models.AlertDecisionNotRequired,
			ProcessingState: models.AlertProcessingNotApplicable, IsRead: true,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		},
		{
			ID: "notification-1", ProjectID: "hidden-project", IdempotencyKey: "hidden-idempotency-key",
			SourceTaskID: &sourceTaskID, Type: models.AlertCustom, Severity: models.SeverityWarning,
			Title: "Review change", Message: "Approval requested", Body: "Check the proposed patch.",
			Source: "review-agent", Metadata: map[string]any{"attempt": float64(2), "nested": map[string]any{"safe": true}},
			DecisionState: models.AlertDecisionApproved, DecidedAt: &decidedAt,
			ProcessingState: models.AlertProcessingFailed, Claimant: "inbox-agent", ClaimedAt: &claimedAt,
			ClaimExpiresAt: &claimExpiresAt, ImplementationTaskID: &implementationTaskID,
			ProcessingError: "worker unavailable", CreatedAt: createdAt, UpdatedAt: updatedAt,
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
		`data-alert-copy`, `aria-label="Copy inspected alert details"`, `aria-label="Copy inspected notification details"`,
		`onclick="copyAlertDetails(this)"`, `data-alert-copy-text`, `aria-live="polite"`,
		`navigator.clipboard.writeText(text)`, `Copied`, `Copy failed`,
		"ID: operational-1", "Title: Build failed", "Message: Compiler exited", "Severity: error",
		"Type: task_failed", "Source: task-runner", "Read: yes", "Task ID: operational-task-1",
		"Execution ID: execution-1", "Created at: 2026-08-04T09:08:07Z", "Updated at: 2026-08-04T11:08:07Z",
		"ID: notification-1", "Body:\nCheck the proposed patch.", "Decision state: approved",
		"Processing state: failed", "Decided at: 2026-08-04T10:11:12Z", "Claimant: inbox-agent",
		"Claimed at: 2026-08-04T10:12:12Z", "Claim expires at: 2026-08-04T10:27:12Z",
		"Source task ID: source-task-1", "Implementation task ID: implementation-task-1",
		"Processing error:\nworker unavailable", "Metadata:\n{\n  &#34;attempt&#34;: 2,\n  &#34;nested&#34;: {\n    &#34;safe&#34;: true\n  }\n}",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("inspect copy markup missing %q", required)
		}
	}
	for _, forbidden := range []string{"Project ID: hidden-project", "Idempotency key: hidden-idempotency-key"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("inspect copy leaked hidden value %q", forbidden)
		}
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
