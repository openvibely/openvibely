package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// AutonomousBuilds renders the autonomous build dashboard
func (h *Handler) AutonomousBuilds(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	var config *models.AutonomousConfig
	var recentBuilds []models.Task
	summaries := make(map[string]*service.BuildSummary)
	var trendData *models.TrendDashboardData

	if projectID != "" && h.autonomousTriggerSvc != nil {
		for i := range projects {
			if projects[i].ID == projectID {
				currentProject = &projects[i]
				break
			}
		}
		config, _ = h.autonomousTriggerSvc.GetConfig(c.Request().Context(), projectID)
		recentBuilds, _ = h.autonomousTriggerSvc.ListRecentBuilds(c.Request().Context(), projectID)

		// Fetch summaries for completed builds
		for _, b := range recentBuilds {
			if b.Status == models.StatusCompleted {
				if summary, err := h.autonomousTriggerSvc.GetBuildSummary(c.Request().Context(), b.ID); err == nil && summary.Summary != "" {
					summaries[b.ID] = summary
				}
			}
		}

		// Fetch trend intelligence data
		if h.trendSvc != nil {
			trendData, _ = h.trendSvc.GetDashboardData(c.Request().Context(), projectID)
		}
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutonomousContent(currentProject, config, recentBuilds, summaries, trendData))
	}
	return render(c, http.StatusOK, pages.AutonomousPage(projects, currentProject, config, recentBuilds, summaries, trendData))
}

// TriggerAutonomousBuild creates a chain of tasks for an autonomous build
// @Summary Trigger autonomous build
// @Description Starts a new autonomous build chain for a project and returns an HTML fragment with result status.
// @Tags autonomous
// @Produce html
// @Param project_id query string true "Project ID"
// @Success 200 {string} string "Autonomous trigger result HTML"
// @Failure 400 {string} string "Missing project_id"
// @Router /api/autonomous/trigger [post]
func (h *Handler) TriggerAutonomousBuild(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.autonomousTriggerSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	task, err := h.autonomousTriggerSvc.TriggerBuild(c.Request().Context(), projectID)
	if err != nil {
		return render(c, http.StatusOK, pages.AutonomousTriggerResult(nil, err))
	}

	return render(c, http.StatusOK, pages.AutonomousTriggerResult(task, nil))
}

// UpdateAutonomousConfig updates the autonomous build configuration
// @Summary Update autonomous build configuration
// @Description Updates autonomous build settings for a project and returns an updated HTML dashboard fragment.
// @Tags autonomous
// @Accept application/x-www-form-urlencoded
// @Produce html
// @Param project_id query string true "Project ID"
// @Param enabled formData boolean false "Enable autonomous builds"
// @Param max_execution_hours formData int false "Maximum execution hours" default(4)
// @Param schedule_hour formData int false "Daily schedule hour (0-23)" default(23)
// @Param protected_files formData string false "JSON array of protected file paths"
// @Param excluded_areas formData string false "JSON array of excluded areas"
// @Success 200 {string} string "Autonomous dashboard HTML fragment"
// @Failure 400 {string} string "Missing project_id"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/config [put]
func (h *Handler) UpdateAutonomousConfig(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.autonomousTriggerSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	enabled := c.FormValue("enabled") == "on" || c.FormValue("enabled") == "true"
	maxHours, _ := strconv.Atoi(c.FormValue("max_execution_hours"))
	if maxHours <= 0 {
		maxHours = 4
	}
	scheduleHour, _ := strconv.Atoi(c.FormValue("schedule_hour"))
	if scheduleHour < 0 || scheduleHour > 23 {
		scheduleHour = 23
	}

	protectedFiles := c.FormValue("protected_files")
	if protectedFiles == "" {
		protectedFiles = "[]"
	} else {
		var arr []string
		if err := json.Unmarshal([]byte(protectedFiles), &arr); err != nil {
			protectedFiles = "[]"
		}
	}

	excludedAreas := c.FormValue("excluded_areas")
	if excludedAreas == "" {
		excludedAreas = "[]"
	} else {
		var arr []string
		if err := json.Unmarshal([]byte(excludedAreas), &arr); err != nil {
			excludedAreas = "[]"
		}
	}

	config := &models.AutonomousConfig{
		ProjectID:         projectID,
		Enabled:           enabled,
		MaxExecutionHours: maxHours,
		ProtectedFiles:    protectedFiles,
		ExcludedAreas:     excludedAreas,
		ScheduleHour:      scheduleHour,
	}

	if err := h.autonomousTriggerSvc.UpdateConfig(c.Request().Context(), config); err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	// Re-render the dashboard
	var recentBuilds []models.Task
	recentBuilds, _ = h.autonomousTriggerSvc.ListRecentBuilds(c.Request().Context(), projectID)

	summaries := make(map[string]*service.BuildSummary)
	for _, b := range recentBuilds {
		if b.Status == models.StatusCompleted {
			if summary, err := h.autonomousTriggerSvc.GetBuildSummary(c.Request().Context(), b.ID); err == nil && summary.Summary != "" {
				summaries[b.ID] = summary
			}
		}
	}

	projects, _ := h.projectSvc.List(c.Request().Context())
	var currentProject *models.Project
	for i := range projects {
		if projects[i].ID == projectID {
			currentProject = &projects[i]
			break
		}
	}

	var trendData *models.TrendDashboardData
	if h.trendSvc != nil {
		trendData, _ = h.trendSvc.GetDashboardData(c.Request().Context(), projectID)
	}

	return render(c, http.StatusOK, pages.AutonomousContent(currentProject, config, recentBuilds, summaries, trendData))
}

// GetBuildSummary returns the summary for a specific autonomous build
// @Summary Get autonomous build summary
// @Description Returns the build summary panel HTML for an autonomous build root task.
// @Tags autonomous
// @Produce html
// @Param taskId path string true "Root task ID"
// @Success 200 {string} string "Build summary HTML fragment"
// @Failure 400 {string} string "Missing task_id"
// @Router /api/autonomous/summary/{taskId} [get]
func (h *Handler) GetBuildSummary(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" || h.autonomousTriggerSvc == nil {
		return c.String(http.StatusBadRequest, "missing task_id")
	}

	summary, err := h.autonomousTriggerSvc.GetBuildSummary(c.Request().Context(), taskID)
	if err != nil {
		return render(c, http.StatusOK, pages.AutonomousBuildSummaryPanel(nil))
	}

	return render(c, http.StatusOK, pages.AutonomousBuildSummaryPanel(summary))
}

// GetBuildChain returns the chain status for a specific autonomous build
// @Summary Get autonomous build chain status
// @Description Returns build-chain status HTML for an autonomous build root task.
// @Tags autonomous
// @Produce html
// @Param taskId path string true "Root task ID"
// @Success 200 {string} string "Build chain HTML fragment"
// @Failure 400 {string} string "Missing task_id"
// @Failure 500 {string} string "Internal server error"
// @Router /api/autonomous/chain/{taskId} [get]
func (h *Handler) GetBuildChain(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" || h.autonomousTriggerSvc == nil {
		return c.String(http.StatusBadRequest, "missing task_id")
	}

	chain, err := h.autonomousTriggerSvc.GetBuildChainStatus(c.Request().Context(), taskID)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.AutonomousBuildChain(chain))
}
