package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func newInsightsHandlerTestContext(t *testing.T) (*TestContext, *repository.InsightsRepo) {
	t.Helper()
	tc := NewTestContext(t)
	insightsRepo := repository.NewInsightsRepo(tc.db)
	tc.handler.insightsSvc = service.NewInsightsService(insightsRepo, tc.taskRepo, tc.projectRepo, tc.llmConfigRepo, tc.execRepo)
	return tc, insightsRepo
}

func createHandlerTestInsight(t *testing.T, repo *repository.InsightsRepo, projectID string, status models.InsightStatus, title string) *models.Insight {
	t.Helper()
	insight := &models.Insight{
		ProjectID:  projectID,
		Type:       models.InsightBugPattern,
		Severity:   models.InsightSeverityHigh,
		Status:     status,
		Title:      title,
		Evidence:   "{}",
		Confidence: 0.8,
	}
	if err := repo.CreateInsight(context.Background(), insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}
	return insight
}

// ProactiveInsights always renders (no nil-service guard upfront).
func TestProactiveInsights_NoProjects(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/insights").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestProactiveInsights_WithProject(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().WithName("Insights Project").Build()
	rec := tc.HTTP().Get("/insights").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK).Contains("Insights Project")
}

func TestProactiveInsights_WithProjectID(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	rec := tc.HTTP().Get("/insights?project_id=" + p.ID).Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestProactiveInsights_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().Build()
	rec := tc.HTMX().Get("/insights").Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}

func TestUpdateInsightStatus_RejectsForeignProject(t *testing.T) {
	tc, insightsRepo := newInsightsHandlerTestContext(t)

	projectA := tc.CreateProject().WithName("Project A").Build()
	projectB := tc.CreateProject().WithName("Project B").Build()
	foreign := &models.Insight{
		ProjectID:  projectB.ID,
		Type:       models.InsightBugPattern,
		Severity:   models.InsightSeverityHigh,
		Status:     models.InsightStatusNew,
		Title:      "Project B insight",
		Evidence:   "{}",
		Confidence: 0.8,
	}
	if err := insightsRepo.CreateInsight(context.Background(), foreign); err != nil {
		t.Fatalf("create foreign insight: %v", err)
	}

	rec := tc.HTTP().Patch("/insights/" + foreign.ID + "/status?project_id=" + projectA.ID).
		WithForm(url.Values{"status": []string{"accepted"}}).Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), foreign.Title) {
		t.Fatalf("foreign insight leaked in response: %s", rec.Body.String())
	}

	got, err := insightsRepo.GetInsight(context.Background(), projectB.ID, foreign.ID)
	if err != nil {
		t.Fatalf("get foreign insight: %v", err)
	}
	if got == nil || got.Status != models.InsightStatusNew {
		t.Fatalf("foreign insight status changed: %#v", got)
	}
}

func TestInsightsMutations_EnforceProjectOwnershipAndRenderScopedCards(t *testing.T) {
	tc, insightsRepo := newInsightsHandlerTestContext(t)
	projectA := tc.CreateProject().WithName("Project A").Build()
	projectB := tc.CreateProject().WithName("Project B").Build()

	foreign := createHandlerTestInsight(t, insightsRepo, projectB.ID, models.InsightStatusNew, "Project B insight")
	foreignStatus := tc.HTMX().Patch("/insights/" + foreign.ID + "/status?project_id=" + projectA.ID).
		WithForm(url.Values{"status": []string{"accepted"}}).Execute()
	if foreignStatus.Code != http.StatusNotFound || strings.Contains(foreignStatus.Body.String(), foreign.Title) {
		t.Fatalf("foreign status response = %d %q, want not found without card", foreignStatus.Code, foreignStatus.Body.String())
	}
	foreignAfterStatus, err := insightsRepo.GetInsight(context.Background(), projectB.ID, foreign.ID)
	if err != nil || foreignAfterStatus == nil || foreignAfterStatus.Status != models.InsightStatusNew || foreignAfterStatus.ResolvedAt != nil {
		t.Fatalf("foreign insight changed after status rejection: %#v err=%v", foreignAfterStatus, err)
	}

	foreignDelete := tc.HTMX().Delete("/insights/" + foreign.ID + "?project_id=" + projectA.ID).Execute()
	if foreignDelete.Code != http.StatusNotFound || strings.Contains(foreignDelete.Body.String(), foreign.Title) {
		t.Fatalf("foreign delete response = %d %q, want not found without card", foreignDelete.Code, foreignDelete.Body.String())
	}
	if stillThere, err := insightsRepo.GetInsight(context.Background(), projectB.ID, foreign.ID); err != nil || stillThere == nil {
		t.Fatalf("foreign insight disappeared after delete rejection: %#v err=%v", stillThere, err)
	}

	foreignKnowledge := &models.KnowledgeEntry{
		ProjectID: projectB.ID,
		Topic:     "Project B knowledge",
		Content:   "Project B content",
		Source:    "test",
		SourceRef: "project-b",
		Tags:      "[]",
	}
	if err := insightsRepo.CreateKnowledge(context.Background(), foreignKnowledge); err != nil {
		t.Fatalf("create foreign knowledge: %v", err)
	}
	foreignKnowledgeDelete := tc.HTMX().Delete("/insights/knowledge/" + foreignKnowledge.ID + "?project_id=" + projectA.ID).Execute()
	if foreignKnowledgeDelete.Code != http.StatusNotFound || strings.Contains(foreignKnowledgeDelete.Body.String(), foreignKnowledge.Content) {
		t.Fatalf("foreign knowledge delete response = %d %q, want not found", foreignKnowledgeDelete.Code, foreignKnowledgeDelete.Body.String())
	}
	if stillThere, err := insightsRepo.GetKnowledge(context.Background(), projectB.ID, foreignKnowledge.ID); err != nil || stillThere == nil {
		t.Fatalf("foreign knowledge disappeared after delete rejection: %#v err=%v", stillThere, err)
	}

	sameProject := createHandlerTestInsight(t, insightsRepo, projectA.ID, models.InsightStatusNew, "Project A insight")
	statusCases := []struct {
		status models.InsightStatus
		label  string
	}{
		{models.InsightStatusReviewed, "Reviewed"},
		{models.InsightStatusAccepted, "Accepted"},
		{models.InsightStatusResolved, "Resolved"},
		{models.InsightStatusRejected, "Rejected"},
	}
	var resolvedAt *time.Time
	for _, tcStatus := range statusCases {
		rec := tc.HTMX().Patch("/insights/" + sameProject.ID + "/status?project_id=" + projectA.ID).
			WithForm(url.Values{"status": []string{string(tcStatus.status)}}).Execute()
		if rec.Code != http.StatusOK {
			t.Fatalf("same-project %s status code = %d, want %d; body=%s", tcStatus.status, rec.Code, http.StatusOK, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `id="insight-`+sameProject.ID+`"`) || !strings.Contains(body, tcStatus.label) {
			t.Fatalf("same-project %s response did not replace card: %s", tcStatus.status, body)
		}
		if !strings.Contains(body, "project_id="+projectA.ID) {
			t.Fatalf("same-project %s card omitted project context: %s", tcStatus.status, body)
		}
		stored, err := insightsRepo.GetInsight(context.Background(), projectA.ID, sameProject.ID)
		if err != nil || stored == nil || stored.Status != tcStatus.status {
			t.Fatalf("same-project %s stored insight = %#v err=%v", tcStatus.status, stored, err)
		}
		if tcStatus.status == models.InsightStatusResolved {
			if stored.ResolvedAt == nil {
				t.Fatal("same-project resolve did not set resolved_at")
			}
			value := *stored.ResolvedAt
			resolvedAt = &value
		}
		if tcStatus.status == models.InsightStatusRejected && (resolvedAt == nil || stored.ResolvedAt == nil || !stored.ResolvedAt.Equal(*resolvedAt)) {
			t.Fatalf("same-project reject did not preserve resolved_at: %#v", stored.ResolvedAt)
		}
	}

	sameProjectDelete := tc.HTMX().Delete("/insights/" + sameProject.ID + "?project_id=" + projectA.ID).Execute()
	if sameProjectDelete.Code != http.StatusOK || sameProjectDelete.Body.Len() != 0 {
		t.Fatalf("same-project insight delete = %d %q, want empty 200", sameProjectDelete.Code, sameProjectDelete.Body.String())
	}
	if deleted, err := insightsRepo.GetInsight(context.Background(), projectA.ID, sameProject.ID); err != nil || deleted != nil {
		t.Fatalf("same-project insight remained after delete: %#v err=%v", deleted, err)
	}

	sameKnowledge := &models.KnowledgeEntry{
		ProjectID: projectA.ID,
		Topic:     "Project A knowledge",
		Content:   "Project A content",
		Source:    "test",
		SourceRef: "project-a",
		Tags:      "[]",
	}
	if err := insightsRepo.CreateKnowledge(context.Background(), sameKnowledge); err != nil {
		t.Fatalf("create same-project knowledge: %v", err)
	}
	sameKnowledgeDelete := tc.HTMX().Delete("/insights/knowledge/" + sameKnowledge.ID + "?project_id=" + projectA.ID).Execute()
	if sameKnowledgeDelete.Code != http.StatusOK || sameKnowledgeDelete.Body.Len() != 0 {
		t.Fatalf("same-project knowledge delete = %d %q, want empty 200", sameKnowledgeDelete.Code, sameKnowledgeDelete.Body.String())
	}
	if deleted, err := insightsRepo.GetKnowledge(context.Background(), projectA.ID, sameKnowledge.ID); err != nil || deleted != nil {
		t.Fatalf("same-project knowledge remained after delete: %#v err=%v", deleted, err)
	}

	missingContextInsight := createHandlerTestInsight(t, insightsRepo, projectA.ID, models.InsightStatusNew, "Missing project context")
	missingStatus := tc.HTMX().Patch("/insights/" + missingContextInsight.ID + "/status").
		WithForm(url.Values{"status": []string{"accepted"}}).Execute()
	if missingStatus.Code != http.StatusBadRequest {
		t.Fatalf("missing project status code = %d, want %d", missingStatus.Code, http.StatusBadRequest)
	}
	missingDelete := tc.HTMX().Delete("/insights/" + missingContextInsight.ID).Execute()
	if missingDelete.Code != http.StatusBadRequest {
		t.Fatalf("missing project insight delete code = %d, want %d", missingDelete.Code, http.StatusBadRequest)
	}
	missingKnowledge := &models.KnowledgeEntry{ProjectID: projectA.ID, Topic: "Missing context", Content: "content", Source: "test", SourceRef: "missing", Tags: "[]"}
	if err := insightsRepo.CreateKnowledge(context.Background(), missingKnowledge); err != nil {
		t.Fatalf("create missing-context knowledge: %v", err)
	}
	missingKnowledgeDelete := tc.HTMX().Delete("/insights/knowledge/" + missingKnowledge.ID).Execute()
	if missingKnowledgeDelete.Code != http.StatusBadRequest {
		t.Fatalf("missing project knowledge delete code = %d, want %d", missingKnowledgeDelete.Code, http.StatusBadRequest)
	}
}

func TestRunInsightsAnalysis_MissingProjectID(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/insights/analyze").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestRunInsightsAnalysis_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/insights/analyze?project_id=proj-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestExtractInsightsKnowledge_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/insights/extract-knowledge?project_id=proj-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestUpdateInsightStatus_NilService(t *testing.T) {
	tc := NewTestContext(t)
	form := url.Values{"status": []string{"acknowledged"}}
	rec := tc.HTTP().Patch("/insights/i-1/status").WithForm(form).Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestDeleteInsight_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Delete("/insights/i-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestListInsightsByType_MissingParams(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/insights/by-type").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestListInsightsByType_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/insights/by-type?project_id=proj-1&type=performance").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestListInsightReports_MissingParams(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/insights/reports").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestListInsightReports_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/insights/reports?project_id=proj-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestRunHealthCheck_MissingProjectID(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/insights/health-check").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestRunHealthCheck_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/insights/health-check?project_id=proj-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestGradeIdeas_MissingProjectID(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/history/grade-ideas").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestGradeIdeas_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/history/grade-ideas?project_id=proj-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}

func TestDeleteKnowledgeEntry_NilService(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Delete("/insights/knowledge/k-1").Execute()
	tc.Assert(rec).StatusCode(http.StatusBadRequest)
}
