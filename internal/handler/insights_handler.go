package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// ProactiveInsights renders the insights dashboard page
func (h *Handler) ProactiveInsights(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	var data *models.InsightDashboardData

	if projectID != "" && h.insightsSvc != nil {
		for i := range projects {
			if projects[i].ID == projectID {
				currentProject = &projects[i]
				break
			}
		}
		var err error
		data, err = h.insightsSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			data = &models.InsightDashboardData{}
		}
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.InsightsContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.Insights(projects, currentProject, data))
}

// RunInsightsAnalysis triggers a full project analysis
func (h *Handler) RunInsightsAnalysis(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	report, err := h.insightsSvc.RunAnalysis(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.InsightAnalysisResult(nil, err))
	}

	return render(c, http.StatusOK, pages.InsightAnalysisResult(report, nil))
}

// ExtractInsightsKnowledge triggers knowledge extraction
func (h *Handler) ExtractInsightsKnowledge(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	entries, err := h.insightsSvc.ExtractKnowledge(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.KnowledgeExtractionResult(nil, err))
	}

	return render(c, http.StatusOK, pages.KnowledgeExtractionResult(entries, nil))
}

// UpdateInsightStatus updates the status of an insight
func (h *Handler) UpdateInsightStatus(c echo.Context) error {
	id := c.Param("id")
	status := models.InsightStatus(c.FormValue("status"))

	if h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "insights service not available")
	}

	if err := h.insightsSvc.UpdateInsightStatus(c.Request().Context(), id, status); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	insight, err := h.insightsSvc.GetInsight(c.Request().Context(), id)
	if err != nil || insight == nil {
		return c.String(http.StatusNotFound, "insight not found")
	}

	return render(c, http.StatusOK, pages.InsightCard(insight))
}

// DeleteInsight removes an insight
func (h *Handler) DeleteInsight(c echo.Context) error {
	id := c.Param("id")

	if h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "insights service not available")
	}

	if err := h.insightsSvc.DeleteInsight(c.Request().Context(), id); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.String(http.StatusOK, "")
}

// SearchInsightsKnowledge searches the knowledge base
func (h *Handler) SearchInsightsKnowledge(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	query := c.QueryParam("q")

	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing params")
	}

	entries, err := h.insightsSvc.SearchKnowledge(c.Request().Context(), projectID, query)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.KnowledgeList(entries))
}

// ListInsightsByType filters insights by type
func (h *Handler) ListInsightsByType(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	insightType := models.InsightType(c.QueryParam("type"))

	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing params")
	}

	insights, err := h.insightsSvc.ListByType(c.Request().Context(), projectID, insightType)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.InsightsList(insights))
}

// ListInsightReports lists recent insight reports
func (h *Handler) ListInsightReports(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing params")
	}

	reports, err := h.insightsSvc.ListReports(c.Request().Context(), projectID, 10)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.InsightReportsList(reports))
}

// RunHealthCheck triggers an AI-powered project health evaluation
func (h *Handler) RunHealthCheck(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	hc, err := h.insightsSvc.RunHealthCheck(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.HealthCheckResult(nil, err))
	}

	history, _ := h.insightsSvc.ListHealthChecks(c.Request().Context(), projectID, 10)
	return render(c, http.StatusOK, pages.HealthCheckResult(hc, nil, history...))
}

// GradeIdeas triggers an AI-powered evaluation of the user's idea quality
func (h *Handler) GradeIdeas(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	ig, err := h.insightsSvc.GradeIdeas(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.IdeaGradeResult(nil, err))
	}

	history, _ := h.insightsSvc.ListIdeaGrades(c.Request().Context(), projectID, 10)
	return render(c, http.StatusOK, pages.IdeaGradeResult(ig, nil, history...))
}

// DeleteKnowledgeEntry removes a knowledge entry
func (h *Handler) DeleteKnowledgeEntry(c echo.Context) error {
	id := c.Param("id")
	if h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "insights service not available")
	}
	if err := h.insightsSvc.DeleteKnowledge(c.Request().Context(), id); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}
	return c.String(http.StatusOK, "")
}

