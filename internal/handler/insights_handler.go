package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// ProactiveInsights renders the insights dashboard page
func (h *Handler) ProactiveInsights(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	currentProject = h.virtualProject(c.Request().Context(), projectID)
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

	report, err := actInsightsRunAnalysis(h, c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.InsightAnalysisResult(nil, err))
	}

	return render(c, http.StatusOK, pages.InsightAnalysisResult(report, nil))
}

// UpdateInsightStatus updates the status of an insight
func (h *Handler) UpdateInsightStatus(c echo.Context) error {
	id := c.Param("id")
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	status := models.InsightStatus(c.FormValue("status"))

	if h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "insights service not available")
	}
	if projectID == "" {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	if err := h.mutateInsight(c.Request().Context(), insightMutationArgs{ProjectID: projectID, InsightID: id, Operation: "status", Status: status}); err != nil {
		if errors.Is(err, service.ErrInsightNotFound) || isActionNotFound(err) {
			return c.String(http.StatusNotFound, "insight not found")
		}
		return c.String(http.StatusInternalServerError, err.Error())
	}

	insight, err := h.insightsSvc.GetInsight(c.Request().Context(), projectID, id)
	if err != nil {
		if errors.Is(err, service.ErrInsightNotFound) || isActionNotFound(err) {
			return c.String(http.StatusNotFound, "insight not found")
		}
		return c.String(http.StatusInternalServerError, err.Error())
	}
	if insight == nil {
		return c.String(http.StatusNotFound, "insight not found")
	}

	return render(c, http.StatusOK, pages.InsightCard(insight))
}

// DeleteInsight removes an insight
func (h *Handler) DeleteInsight(c echo.Context) error {
	id := c.Param("id")
	projectID := strings.TrimSpace(c.QueryParam("project_id"))

	if h.insightsSvc == nil {
		return c.String(http.StatusBadRequest, "insights service not available")
	}
	if projectID == "" {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	if err := h.mutateInsight(c.Request().Context(), insightMutationArgs{ProjectID: projectID, InsightID: id, Operation: "delete"}); err != nil {
		if errors.Is(err, service.ErrInsightNotFound) || isActionNotFound(err) {
			return c.String(http.StatusNotFound, "insight not found")
		}
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return c.String(http.StatusOK, "")
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

	hc, err := actInsightsRunHealthCheck(h, c.Request().Context(), projectID)
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

	ig, err := actInsightsGradeIdeas(h, c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.IdeaGradeResult(nil, err))
	}

	history, _ := h.insightsSvc.ListIdeaGrades(c.Request().Context(), projectID, 10)
	return render(c, http.StatusOK, pages.IdeaGradeResult(ig, nil, history...))
}
