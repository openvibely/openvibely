package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	alertListBenchRows = 100
	// alertListBenchReductionFloor is the minimum fractional reduction the lazy
	// list projection must achieve for both response bytes and allocated bytes,
	// per the acceptance criteria (at least 90% lower).
	alertListBenchReductionFloor = 0.90
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

// inlinedDetailBytes returns the body and pretty-printed metadata bytes the
// pre-change list embedded for every row. Adding these to the lazy response
// reconstructs the byte and allocation weight of the previous full-body list.
func inlinedDetailBytes(tb testing.TB, alerts []models.Alert) int {
	tb.Helper()
	total := 0
	for i := range alerts {
		total += len(alerts[i].Body)
		if len(alerts[i].Metadata) == 0 {
			continue
		}
		encoded, err := json.MarshalIndent(alerts[i].Metadata, "", "  ")
		if err != nil {
			tb.Fatalf("marshal metadata: %v", err)
		}
		total += len(encoded)
	}
	return total
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

func measureAllocBytes(fn func()) int {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return int(after.TotalAlloc - before.TotalAlloc)
}

func assertBenchReduction(b *testing.B, label string, baseline, lazy int) {
	b.Helper()
	if baseline <= 0 {
		b.Fatalf("%s baseline must be positive, got %d", label, baseline)
	}
	reduction := 1 - float64(lazy)/float64(baseline)
	b.ReportMetric(reduction*100, label+"_reduction_pct")
	if reduction < alertListBenchReductionFloor {
		b.Fatalf("%s reduction = %.4f (baseline=%d lazy=%d), want >= %.2f", label, reduction, baseline, lazy, alertListBenchReductionFloor)
	}
}

// BenchmarkAlertListLazyDetailProjection measures response bytes and allocated
// bytes for the initial GET /alerts render and one representative HTMX mutation
// refresh (single delete) over 100 notifications with large bodies and metadata.
// It asserts the lazy list projection achieves at least a 90% reduction in both
// response bytes and allocated bytes relative to the previous full-body list,
// which inlined every body and metadata blob into the list response.
func BenchmarkAlertListLazyDetailProjection(b *testing.B) {
	h, e, _ := setupTestHandler(b)
	projectID := seedAlertListBenchProject(b, h)
	ctx := context.Background()

	fullAlerts, err := h.alertSvc.ListByProject(ctx, projectID, 100)
	if err != nil {
		b.Fatalf("list full alerts: %v", err)
	}
	if len(fullAlerts) != alertListBenchRows {
		b.Fatalf("full alerts = %d, want %d", len(fullAlerts), alertListBenchRows)
	}

	// Initial GET /alerts: response bytes.
	lazyInitialBytes := renderLazyInitial(b, h, e, projectID)
	baselineInitialBytes := lazyInitialBytes + inlinedDetailBytes(b, fullAlerts)
	assertBenchReduction(b, "initial_response_B", baselineInitialBytes, lazyInitialBytes)

	// Initial GET /alerts: allocated bytes. The baseline additionally allocates
	// the inlined body/metadata text into the response buffer.
	lazyInitialAllocs := measureAllocBytes(func() {
		_ = renderLazyInitial(b, h, e, projectID)
	})
	baselineInitialAllocs := measureAllocBytes(func() {
		_ = renderLazyInitial(b, h, e, projectID)
		var sink bytes.Buffer
		for i := range fullAlerts {
			sink.WriteString(fullAlerts[i].Body)
			if len(fullAlerts[i].Metadata) > 0 {
				encoded, _ := json.MarshalIndent(fullAlerts[i].Metadata, "", "  ")
				sink.Write(encoded)
			}
		}
		_ = sink.Len()
	})
	assertBenchReduction(b, "initial_alloc_B", baselineInitialAllocs, lazyInitialAllocs)

	// Representative HTMX mutation refresh (single delete).
	deletedID := fullAlerts[0].ID
	survivors := fullAlerts[1:]
	lazyMutationBytes := renderLazyMutation(b, h, e, projectID, deletedID)
	baselineMutationBytes := lazyMutationBytes + inlinedDetailBytes(b, survivors)
	assertBenchReduction(b, "mutation_response_B", baselineMutationBytes, lazyMutationBytes)

	lazyMutationAllocs := measureAllocBytes(func() {
		list, _ := h.alertSvc.ListSummariesByProject(ctx, projectID, 100)
		var buf bytes.Buffer
		_ = pages.AlertsContent(list, projectID, len(list)).Render(ctx, &buf)
	})
	baselineMutationAllocs := measureAllocBytes(func() {
		list, _ := h.alertSvc.ListSummariesByProject(ctx, projectID, 100)
		var buf bytes.Buffer
		_ = pages.AlertsContent(list, projectID, len(list)).Render(ctx, &buf)
		var sink bytes.Buffer
		for i := range survivors {
			sink.WriteString(survivors[i].Body)
			if len(survivors[i].Metadata) > 0 {
				encoded, _ := json.MarshalIndent(survivors[i].Metadata, "", "  ")
				sink.Write(encoded)
			}
		}
		_ = sink.Len()
	})
	assertBenchReduction(b, "mutation_alloc_B", baselineMutationAllocs, lazyMutationAllocs)

	b.ReportMetric(float64(baselineInitialBytes), "baseline_initial_B")
	b.ReportMetric(float64(lazyInitialBytes), "lazy_initial_B")

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
