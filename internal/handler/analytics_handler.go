package handler

import (
	"log"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// Analytics displays the analytics dashboard
func (h *Handler) Analytics(c echo.Context) error {
	isHTMX := isHTMX(c)
	log.Printf("[handler] Analytics requested, project_id=%s, htmx=%v", c.QueryParam("project_id"), isHTMX)

	projectID := c.QueryParam("project_id")

	// For HTMX requests, we still need to get projects for the current project lookup
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] Analytics error listing projects: %v", err)
		return err
	}

	// Default to the first project
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	for i := range projects {
		if projects[i].ID == projectID {
			currentProject = &projects[i]
			break
		}
	}

	// For HTMX requests, return just the analytics content
	if isHTMX {
		return render(c, http.StatusOK, pages.AnalyticsContent(currentProject))
	}

	return render(c, http.StatusOK, pages.Analytics(projects, currentProject))
}

// GetSuccessFailureRates returns success/failure rates data
// @Summary Get success/failure rates
// @Description Returns execution success/failure rates grouped by day, week, or month.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param group_by query string false "Grouping period: day, week, or month" default(day)
// @Param date_from query string false "Optional start datetime filter"
// @Param date_to query string false "Optional end datetime filter"
// @Success 200 {array} repository.SuccessFailureRate "Success/failure rates"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/success-failure-rates [get]
func (h *Handler) GetSuccessFailureRates(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	groupBy := c.QueryParam("group_by")
	if groupBy == "" {
		groupBy = "day"
	}
	dateFrom := c.QueryParam("date_from")
	dateTo := c.QueryParam("date_to")

	rates, err := h.execRepo.GetSuccessFailureRates(c.Request().Context(), projectID, groupBy, dateFrom, dateTo)
	if err != nil {
		log.Printf("[handler] GetSuccessFailureRates error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, rates)
}

// GetAvgExecutionTimeByTask returns average execution times by task
// @Summary Get average execution time by task
// @Description Returns average task execution durations for completed executions.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param limit query int false "Maximum number of tasks to return" default(10)
// @Success 200 {array} repository.AvgExecutionTime "Average execution time by task"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/avg-execution-time-by-task [get]
func (h *Handler) GetAvgExecutionTimeByTask(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	limitStr := c.QueryParam("limit")
	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	times, err := h.execRepo.GetAvgExecutionTimeByTask(c.Request().Context(), projectID, limit)
	if err != nil {
		log.Printf("[handler] GetAvgExecutionTimeByTask error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, times)
}

// GetAvgExecutionTimeByAgent returns average execution times by agent
// @Summary Get average execution time by model
// @Description Returns average execution durations grouped by configured model.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Success 200 {array} repository.AvgExecutionTime "Average execution time by model"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/avg-execution-time-by-agent [get]
func (h *Handler) GetAvgExecutionTimeByAgent(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	times, err := h.execRepo.GetAvgExecutionTimeByAgent(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] GetAvgExecutionTimeByAgent error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, times)
}

// GetExecutionTrendsByHour returns execution counts by hour
// @Summary Get execution trends by hour
// @Description Returns execution counts grouped by hour-of-day.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param date_from query string false "Optional start datetime filter"
// @Param date_to query string false "Optional end datetime filter"
// @Success 200 {array} repository.ExecutionTrend "Execution counts by hour"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/execution-trends-by-hour [get]
func (h *Handler) GetExecutionTrendsByHour(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	dateFrom := c.QueryParam("date_from")
	dateTo := c.QueryParam("date_to")

	trends, err := h.execRepo.GetExecutionTrendsByHour(c.Request().Context(), projectID, dateFrom, dateTo)
	if err != nil {
		log.Printf("[handler] GetExecutionTrendsByHour error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, trends)
}

// GetAgentUsageByProject returns agent usage breakdown
// @Summary Get model usage by project
// @Description Returns model usage, success count, and failure count grouped by project.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Success 200 {array} repository.AgentUsage "Model usage breakdown"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/agent-usage-by-project [get]
func (h *Handler) GetAgentUsageByProject(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	usage, err := h.execRepo.GetAgentUsageByProject(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] GetAgentUsageByProject error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, usage)
}

// GetMostFrequentTasks returns the most frequently executed tasks
// @Summary Get most frequent tasks
// @Description Returns tasks ordered by execution count.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param limit query int false "Maximum number of tasks to return" default(10)
// @Success 200 {array} repository.TaskFrequency "Most frequently executed tasks"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/most-frequent-tasks [get]
func (h *Handler) GetMostFrequentTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	limitStr := c.QueryParam("limit")
	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	frequencies, err := h.execRepo.GetMostFrequentTasks(c.Request().Context(), projectID, limit)
	if err != nil {
		log.Printf("[handler] GetMostFrequentTasks error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, frequencies)
}

// GetFailedTaskPatterns returns tasks with failure patterns
// @Summary Get failed task patterns
// @Description Returns tasks with repeated failures, grouped by last observed error.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param limit query int false "Maximum number of patterns to return" default(10)
// @Success 200 {array} repository.FailedTaskPattern "Failed task patterns"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/failed-task-patterns [get]
func (h *Handler) GetFailedTaskPatterns(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	limitStr := c.QueryParam("limit")
	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	patterns, err := h.execRepo.GetFailedTaskPatterns(c.Request().Context(), projectID, limit)
	if err != nil {
		log.Printf("[handler] GetFailedTaskPatterns error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, patterns)
}
