package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	alertListBenchRows = 100
)

// seedAlertListBenchProject creates a project populated with 100 notifications
// carrying large bodies and metadata so the list projection savings are
// measurable end to end through the HTTP handler.
func seedAlertListBenchProject(tb testing.TB, h *Handler) string {
	tb.Helper()
	project := createProjectTB(tb, h, "Alert List Bench")
	ctx := context.Background()
	largeBody := strings.Repeat("compiler diagnostics with a long payload line ", 900) // ~42 KiB
	largeMetadataValue := strings.Repeat("structured metadata payload segment ", 1100) // ~40 KiB
	for i := 0; i < alertListBenchRows; i++ {
		a := &models.Alert{
			ProjectID:       project.ID,
			Type:            models.AlertCustom,
			Severity:        models.SeverityWarning,
			Title:           "Bench notification",
			Message:         "Short triage message",
			Body:            largeBody,
			Source:          "benchmark",
			Metadata:        map[string]any{"component": "alerts", "payload": largeMetadataValue},
			DecisionState:   models.AlertDecisionPending,
			ProcessingState: models.AlertProcessingUnclaimed,
		}
		if err := h.alertSvc.Create(ctx, a); err != nil {
			tb.Fatalf("create bench alert %d: %v", i, err)
		}
	}
	return project.ID
}

// renderLazyInitial renders the production initial GET /alerts response using the
// bounded summary projection and returns the response byte length.
func renderLazyInitial(tb testing.TB, h *Handler, e *echo.Echo, projectID string) int {
	tb.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+projectID, nil)
	if err := h.ListAlerts(e.NewContext(req, rec)); err != nil {
		tb.Fatalf("initial /alerts render: %v", err)
	}
	if rec.Code != http.StatusOK {
		tb.Fatalf("initial /alerts status = %d", rec.Code)
	}
	return rec.Body.Len()
}

// renderLazyMutation renders one representative HTMX mutation refresh (a single
// delete) using the bounded summary projection and returns the response byte
// length.
func renderLazyMutation(tb testing.TB, h *Handler, e *echo.Echo, projectID, alertID string) int {
	tb.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alertID+"?project_id="+projectID, nil)
	req.Header.Set("HX-Request", "true")
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(alertID)
	if err := h.DeleteAlert(c); err != nil {
		tb.Fatalf("mutation refresh render: %v", err)
	}
	if rec.Code != http.StatusOK {
		tb.Fatalf("mutation refresh status = %d", rec.Code)
	}
	return rec.Body.Len()
}

// BenchmarkAlertListLazyDetailProjection measures the current bounded alert
// list and mutation responses over 100 notifications with large detail fields.
func BenchmarkAlertListLazyDetailProjection(b *testing.B) {
	h, e, _ := setupTestHandler(b)
	projectID := seedAlertListBenchProject(b, h)
	ctx := context.Background()

	alerts, err := h.alertSvc.ListSummariesByProject(ctx, projectID, 100)
	if err != nil {
		b.Fatalf("list alert summaries: %v", err)
	}
	if len(alerts) != alertListBenchRows {
		b.Fatalf("alert summaries = %d, want %d", len(alerts), alertListBenchRows)
	}

	initialBytes := renderLazyInitial(b, h, e, projectID)
	mutationBytes := renderLazyMutation(b, h, e, projectID, alerts[0].ID)
	b.ReportMetric(float64(initialBytes), "initial_response_B")
	b.ReportMetric(float64(mutationBytes), "mutation_response_B")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list, err := h.alertSvc.ListSummariesByProject(ctx, projectID, 100)
		if err != nil {
			b.Fatalf("list summaries: %v", err)
		}
		var buf bytes.Buffer
		if err := pages.AlertsContent(list, projectID, len(list)).Render(ctx, &buf); err != nil {
			b.Fatalf("render lazy list: %v", err)
		}
		if buf.Len() == 0 {
			b.Fatal("empty lazy list render")
		}
	}
}
