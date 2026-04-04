package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// BacklogManagement renders the backlog management dashboard page
func (h *Handler) BacklogManagement(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	var data *models.BacklogDashboardData

	if projectID != "" && h.backlogSvc != nil {
		for i := range projects {
			if projects[i].ID == projectID {
				currentProject = &projects[i]
				break
			}
		}
		var err error
		data, err = h.backlogSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			data = &models.BacklogDashboardData{}
		}
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.BacklogContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.BacklogPage(projects, currentProject, data))
}

// RunBacklogAnalysis triggers a full backlog analysis
func (h *Handler) RunBacklogAnalysis(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	report, err := h.backlogSvc.RunAnalysis(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.BacklogAnalysisResult(nil, err))
	}

	return render(c, http.StatusOK, pages.BacklogAnalysisResult(report, nil))
}

// UpdateBacklogSuggestionStatus updates the status of a suggestion
func (h *Handler) UpdateBacklogSuggestionStatus(c echo.Context) error {
	id := c.Param("id")
	status := models.BacklogSuggestionStatus(c.FormValue("status"))

	if h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "backlog service not available")
	}

	if err := h.backlogSvc.UpdateSuggestionStatus(c.Request().Context(), id, status); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusOK, "OK")
	}

	return h.renderBacklogOrSuggestions(c, projectID)
}

// ApplyBacklogSuggestion applies an approved suggestion
func (h *Handler) ApplyBacklogSuggestion(c echo.Context) error {
	id := c.Param("id")

	if h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "backlog service not available")
	}

	if err := h.backlogSvc.ApplySuggestion(c.Request().Context(), id); err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusOK, "Applied")
	}

	return h.renderBacklogOrSuggestions(c, projectID)
}

// DeleteBacklogSuggestion removes a suggestion
func (h *Handler) DeleteBacklogSuggestion(c echo.Context) error {
	id := c.Param("id")

	if h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "backlog service not available")
	}

	if err := h.backlogSvc.DeleteSuggestion(c.Request().Context(), id); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusOK, "")
	}

	return h.renderBacklogOrSuggestions(c, projectID)
}

// SnapshotBacklogHealth captures a health snapshot
func (h *Handler) SnapshotBacklogHealth(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	if err := h.backlogSvc.SnapshotHealth(c.Request().Context(), projectID); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return h.renderBacklogOrSuggestions(c, projectID)
}

// ListBacklogReports returns analysis reports
func (h *Handler) ListBacklogReports(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.backlogSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	reports, err := h.backlogSvc.ListReports(c.Request().Context(), projectID)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.BacklogReportsList(reports))
}

// renderBacklogOrSuggestions returns the unified suggestions view if the request
// came from /suggestions, otherwise returns the standalone backlog view.
func (h *Handler) renderBacklogOrSuggestions(c echo.Context, projectID string) error {
	currentURL := c.Request().Header.Get("HX-Current-URL")
	if strings.Contains(currentURL, "/suggestions") {
		return h.renderSuggestionsContent(c, projectID)
	}

	data, err := h.backlogSvc.GetDashboard(c.Request().Context(), projectID)
	if err != nil {
		data = &models.BacklogDashboardData{}
	}

	var currentProject *models.Project
	projects, _ := h.projectSvc.List(c.Request().Context())
	for i := range projects {
		if projects[i].ID == projectID {
			currentProject = &projects[i]
			break
		}
	}

	return render(c, http.StatusOK, pages.BacklogContent(currentProject, data))
}

// renderSuggestionsContent builds and renders the unified suggestions dashboard.
func (h *Handler) renderSuggestionsContent(c echo.Context, projectID string) error {
	var currentProject *models.Project
	projects, _ := h.projectSvc.List(c.Request().Context())
	for i := range projects {
		if projects[i].ID == projectID {
			currentProject = &projects[i]
			break
		}
	}

	data := &models.SuggestionsDashboardData{}
	if h.insightsSvc != nil {
		insightData, err := h.insightsSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			insightData = &models.InsightDashboardData{}
		}
		data.Insights = insightData
	}
	if h.backlogSvc != nil {
		backlogData, err := h.backlogSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			backlogData = &models.BacklogDashboardData{}
		}
		data.Backlog = backlogData
	}

	return render(c, http.StatusOK, pages.SuggestionsContent(currentProject, data))
}
