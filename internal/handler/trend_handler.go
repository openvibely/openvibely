package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// SaveXCredentials saves X API credentials for a project
// @Summary Save X credentials
// @Description Saves X API credentials for trend intelligence and returns an HTML confirmation fragment.
// @Tags autonomous
// @Accept application/x-www-form-urlencoded
// @Produce html
// @Param project_id query string true "Project ID"
// @Param api_key formData string false "X API key"
// @Param api_secret formData string false "X API secret"
// @Param access_token formData string false "X access token"
// @Param access_token_secret formData string false "X access token secret"
// @Param bearer_token formData string false "X bearer token"
// @Success 200 {string} string "Credentials saved HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/x-credentials [put]
func (h *Handler) SaveXCredentials(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	creds := &models.XCredentials{
		ProjectID:         projectID,
		APIKey:            c.FormValue("api_key"),
		APISecret:         c.FormValue("api_secret"),
		AccessToken:       c.FormValue("access_token"),
		AccessTokenSecret: c.FormValue("access_token_secret"),
		BearerToken:       c.FormValue("bearer_token"),
	}

	if err := h.trendSvc.SaveXCredentials(c.Request().Context(), creds); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.TrendXCredentialsSaved())
}

// AddTrendSource adds a new trend monitoring source
// @Summary Add trend source
// @Description Adds a monitored trend source and returns updated source-list HTML.
// @Tags autonomous
// @Accept application/x-www-form-urlencoded
// @Produce html
// @Param project_id query string true "Project ID"
// @Param source_type formData string true "Source type (hashtag, account, keyword, competitor)"
// @Param value formData string true "Source value"
// @Success 200 {string} string "Updated source list HTML fragment"
// @Failure 400 {string} string "Missing or invalid parameters"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/trends/sources [post]
func (h *Handler) AddTrendSource(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	sourceType := models.TrendSourceType(c.FormValue("source_type"))
	value := c.FormValue("value")
	if value == "" {
		return c.String(http.StatusBadRequest, "value is required")
	}

	source := &models.TrendSource{
		ProjectID:  projectID,
		SourceType: sourceType,
		Value:      value,
		Enabled:    true,
	}

	if err := h.trendSvc.AddSource(c.Request().Context(), source); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	// Return updated source list
	return h.renderTrendSources(c, projectID)
}

// DeleteTrendSource removes a trend monitoring source
// @Summary Delete trend source
// @Description Deletes a trend source and returns updated source-list HTML.
// @Tags autonomous
// @Produce html
// @Param id path string true "Trend source ID"
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Updated source list HTML fragment"
// @Failure 400 {string} string "Missing parameters"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/trends/sources/{id} [delete]
func (h *Handler) DeleteTrendSource(c echo.Context) error {
	id := c.Param("id")
	projectID := c.QueryParam("project_id")
	if id == "" || projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing parameters")
	}

	if err := h.trendSvc.DeleteSource(c.Request().Context(), id); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return h.renderTrendSources(c, projectID)
}

// ToggleTrendSource enables or disables a trend source
// @Summary Toggle trend source
// @Description Enables or disables a trend source and returns updated source-list HTML.
// @Tags autonomous
// @Accept application/x-www-form-urlencoded
// @Produce html
// @Param id path string true "Trend source ID"
// @Param project_id query string true "Project ID"
// @Param enabled formData boolean true "Whether source is enabled"
// @Success 200 {string} string "Updated source list HTML fragment"
// @Failure 400 {string} string "Missing parameters"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/trends/sources/{id}/toggle [patch]
func (h *Handler) ToggleTrendSource(c echo.Context) error {
	id := c.Param("id")
	projectID := c.QueryParam("project_id")
	if id == "" || projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing parameters")
	}

	enabled := c.FormValue("enabled") == "true" || c.FormValue("enabled") == "on"
	if err := h.trendSvc.ToggleSource(c.Request().Context(), id, enabled); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return h.renderTrendSources(c, projectID)
}

// CollectTrends triggers X API data collection
// @Summary Collect trends
// @Description Collects trend data from configured sources and returns collection result HTML.
// @Tags autonomous
// @Produce html
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Collection result HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Router /api/autonomous/trends/collect [post]
func (h *Handler) CollectTrends(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	count, err := h.trendSvc.CollectFromX(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.TrendCollectResult(0, err))
	}

	return render(c, http.StatusOK, pages.TrendCollectResult(count, nil))
}

// AnalyzeTrends triggers AI-powered trend analysis
// @Summary Analyze trends
// @Description Runs trend pattern analysis and returns analysis result HTML.
// @Tags autonomous
// @Produce html
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Trend analysis HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Router /api/autonomous/trends/analyze [post]
func (h *Handler) AnalyzeTrends(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	patterns, err := h.trendSvc.AnalyzeTrends(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.TrendAnalysisResult(nil, err))
	}

	return render(c, http.StatusOK, pages.TrendAnalysisResult(patterns, nil))
}

// AnalyzeCompetitors triggers AI-powered competitor analysis
// @Summary Analyze competitors
// @Description Runs competitor update analysis and returns result HTML.
// @Tags autonomous
// @Produce html
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Competitor analysis HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Router /api/autonomous/trends/competitors [post]
func (h *Handler) AnalyzeCompetitors(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	updates, err := h.trendSvc.AnalyzeCompetitors(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.TrendCompetitorResult(nil, err))
	}

	return render(c, http.StatusOK, pages.TrendCompetitorResult(updates, nil))
}

// UpdateTrendPatternStatus updates the status of a trend pattern
// @Summary Update trend pattern status
// @Description Updates a trend pattern status and returns updated patterns HTML.
// @Tags autonomous
// @Accept application/x-www-form-urlencoded
// @Produce html
// @Param id path string true "Trend pattern ID"
// @Param project_id query string true "Project ID"
// @Param status formData string true "Pattern status (implemented or dismissed)"
// @Param feature_name formData string false "Feature name when status is implemented"
// @Success 200 {string} string "Updated trend patterns HTML fragment"
// @Failure 400 {string} string "Missing or invalid parameters"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/trends/patterns/{id}/status [patch]
func (h *Handler) UpdateTrendPatternStatus(c echo.Context) error {
	id := c.Param("id")
	projectID := c.QueryParam("project_id")
	if id == "" || projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing parameters")
	}

	status := c.FormValue("status")
	featureName := c.FormValue("feature_name")

	switch status {
	case "implemented":
		if err := h.trendSvc.RecordFeatureOutcome(c.Request().Context(), id, featureName); err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}
	case "dismissed":
		if err := h.trendSvc.DismissPattern(c.Request().Context(), id); err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}
	default:
		return c.String(http.StatusBadRequest, "invalid status: must be 'implemented' or 'dismissed'")
	}

	return h.renderTrendPatterns(c, projectID)
}

// GetTrendDashboard returns the trend intelligence section of the autonomous dashboard
// @Summary Get trend dashboard section
// @Description Returns the trend dashboard HTML section for a project.
// @Tags autonomous
// @Produce html
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Trend dashboard HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/trends/dashboard [get]
func (h *Handler) GetTrendDashboard(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.trendSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	data, err := h.trendSvc.GetDashboardData(c.Request().Context(), projectID)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.TrendDashboardSection(projectID, data))
}

// --- Helpers ---

func (h *Handler) renderTrendSources(c echo.Context, projectID string) error {
	sources, _ := h.trendSvc.ListSources(c.Request().Context(), projectID)
	return render(c, http.StatusOK, pages.TrendSourcesList(projectID, sources))
}

func (h *Handler) renderTrendPatterns(c echo.Context, projectID string) error {
	data, _ := h.trendSvc.GetDashboardData(c.Request().Context(), projectID)
	if data == nil {
		data = &models.TrendDashboardData{}
	}
	return render(c, http.StatusOK, pages.TrendPatternsPanel(projectID, data.ActivePatterns))
}
