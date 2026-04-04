package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// UnifiedSuggestions renders the combined insights + backlog suggestions dashboard
func (h *Handler) UnifiedSuggestions(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	data := &models.SuggestionsDashboardData{}

	if projectID != "" {
		for i := range projects {
			if projects[i].ID == projectID {
				currentProject = &projects[i]
				break
			}
		}

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
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.SuggestionsContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.SuggestionsPage(projects, currentProject, data))
}

// RunCombinedAnalysis triggers both insights and backlog analysis
func (h *Handler) RunCombinedAnalysis(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	analysisType := c.QueryParam("type")

	var insightReport *models.InsightReport
	var backlogReport *models.BacklogAnalysisReport
	var insightErr, backlogErr error

	if analysisType == "" || analysisType == "all" || analysisType == "insights" {
		if h.insightsSvc != nil {
			insightReport, insightErr = h.insightsSvc.RunAnalysis(c.Request().Context(), projectID)
		}
	}

	if analysisType == "" || analysisType == "all" || analysisType == "backlog" {
		if h.backlogSvc != nil {
			backlogReport, backlogErr = h.backlogSvc.RunAnalysis(c.Request().Context(), projectID)
		}
	}

	return render(c, http.StatusOK, pages.CombinedAnalysisResult(insightReport, insightErr, backlogReport, backlogErr))
}
