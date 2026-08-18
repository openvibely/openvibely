package pages

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestExecutionStatusBadgesUseSharedMappingAcrossHistoryAndDetail(t *testing.T) {
	tests := []struct {
		name   string
		status models.ExecutionStatus
	}{
		{name: "completed", status: models.ExecCompleted},
		{name: "failed", status: models.ExecFailed},
		{name: "cancelled", status: models.ExecCancelled},
		{name: "running", status: models.ExecRunning},
		{name: "queued", status: models.ExecQueued},
		{name: "unknown", status: models.ExecutionStatus("unknown")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label := components.ExecutionStatusLabel(tt.status)
			badgeClass := components.ExecutionStatusBadgeClass(tt.status)

			historyHTML := renderHistoryStatusBadgeForTest(t, tt.status)
			detailHTML := renderExecutionDetailStatusBadgeForTest(t, tt.status)

			assertRenderedStatusBadge(t, historyHTML, label, badgeClass, "badge-sm")
			assertRenderedStatusBadge(t, detailHTML, label, badgeClass, "ml-2")
		})
	}
}

func TestExecutionCancelledBadgeDoesNotFallBackOnExecutionDetail(t *testing.T) {
	detailHTML := renderExecutionDetailStatusBadgeForTest(t, models.ExecCancelled)

	assertRenderedStatusBadge(t, detailHTML, "Cancelled", "badge-warning", "ml-2")
	if strings.Contains(detailHTML, `badge-ghost ml-2`) || strings.Contains(detailHTML, `>cancelled</span>`) {
		t.Fatalf("cancelled execution detail badge fell back to ghost/lowercase rendering: %s", detailHTML)
	}
}

func renderHistoryStatusBadgeForTest(t *testing.T, status models.ExecutionStatus) string {
	t.Helper()

	de := models.HistoryExecution{
		Execution: models.Execution{
			ID:        fmt.Sprintf("exec-%s", status),
			TaskID:    fmt.Sprintf("task-%s", status),
			Status:    status,
			StartedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		},
		TaskTitle: "Shared status badge task",
		AgentName: "Test Agent",
	}

	var buf bytes.Buffer
	if err := historyExecutionCard(de, "project-1").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render history execution card: %v", err)
	}
	return buf.String()
}

func renderExecutionDetailStatusBadgeForTest(t *testing.T, status models.ExecutionStatus) string {
	t.Helper()

	exec := &models.Execution{
		ID:        fmt.Sprintf("exec-%s", status),
		TaskID:    fmt.Sprintf("task-%s", status),
		Status:    status,
		StartedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}

	var buf bytes.Buffer
	if err := ExecutionDetail(nil, exec, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render execution detail: %v", err)
	}
	return buf.String()
}

func assertRenderedStatusBadge(t *testing.T, html, label, badgeClass, pageClass string) {
	t.Helper()

	classSnippet := fmt.Sprintf(`class="badge %s %s"`, badgeClass, pageClass)
	if !strings.Contains(html, classSnippet) {
		t.Fatalf("expected shared status badge class %q in rendered HTML: %s", classSnippet, html)
	}
	labelSnippet := fmt.Sprintf(`>%s</span>`, label)
	if !strings.Contains(html, labelSnippet) {
		t.Fatalf("expected shared status badge label %q in rendered HTML: %s", label, html)
	}
}
