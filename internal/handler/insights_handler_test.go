package handler

import (
	"net/http"
	"net/url"
	"testing"
)

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

// insightsSvc is nil in NewTestContext → 400 for all action endpoints.

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
