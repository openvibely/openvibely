package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// Analytics displays the analytics dashboard
func (h *Handler) Analytics(c echo.Context) error {
	isHTMX := isHTMX(c)
	applog.Debugf("[handler] Analytics requested, project_id=%s, htmx=%v", c.QueryParam("project_id"), isHTMX)

	projectID := c.QueryParam("project_id")

	// For HTMX requests, we still need to get projects for the current project lookup.
	// The analytics shell only renders the sidebar selector and the current project's
	// id/name, so the compact selector projection is sufficient here.
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] Analytics error listing projects: %v", err)
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

// GetAnalyticsUsage returns detailed LLM usage analytics.
// @Summary Get LLM usage analytics
// @Description Returns token/cache/reasoning/cost totals, daily usage, usage rate, model breakdowns, and account limit snapshots.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param provider query string false "Provider filter"
// @Param range query string false "Convenience range: 7d, 30d, 90d, 365d, month, all" default(30d)
// @Param group_by query string false "Usage rate grouping: hour, day, week, month" default(day)
// @Param date_from query string false "Optional start datetime filter"
// @Param date_to query string false "Optional end datetime filter"
// @Success 200 {object} models.AnalyticsUsageViewModel "Usage analytics"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/usage [get]
func (h *Handler) GetAnalyticsUsage(c echo.Context) error {
	if h.usageAnalyticsSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "usage analytics service is not configured")
	}
	filter := parseUsageFilter(c)
	view, err := h.usageAnalyticsSvc.BuildAnalyticsUsage(c.Request().Context(), filter)
	if err != nil {
		applog.Infof("[handler] GetAnalyticsUsage error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, view)
}

// GetSkillAnalytics returns the Skill Curator analytics dashboard data.
// @Summary Get skill analytics
// @Description Returns skill usage over time, top skills, selection follow-through, agent heatmap, and underused skill metrics.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param range query string false "Convenience range: 7d, 30d, 90d, 365d, all" default(30d)
// @Param group_by query string false "Usage trend grouping: day, week, or month" default(day)
// @Param agent_id query string false "Agent ID filter"
// @Param surface query string false "Surface filter"
// @Param skill_scope query string false "Skill scope filter"
// @Param event_type query string false "Event type filter (selected, loaded, viewed, created, edited)"
// @Success 200 {object} models.SkillAnalyticsDashboard "Skill analytics"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/skills [get]
func (h *Handler) GetSkillAnalytics(c echo.Context) error {
	if h.skillAnalyticsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "skill analytics repository is not configured")
	}
	filter := parseSkillAnalyticsFilter(c)
	enabled := h.enabledSkillsForAnalytics(c)
	view, err := h.skillAnalyticsRepo.BuildDashboard(c.Request().Context(), filter, enabled)
	if err != nil {
		applog.Infof("[handler] GetSkillAnalytics error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, view)
}

func parseSkillAnalyticsFilter(c echo.Context) repository.SkillAnalyticsFilter {
	filter := repository.SkillAnalyticsFilter{
		ProjectID:  strings.TrimSpace(c.QueryParam("project_id")),
		AgentID:    strings.TrimSpace(c.QueryParam("agent_id")),
		Surface:    strings.TrimSpace(c.QueryParam("surface")),
		SkillScope: strings.TrimSpace(c.QueryParam("skill_scope")),
		EventType:  strings.TrimSpace(c.QueryParam("event_type")),
		Limit:      10,
		GroupBy:    strings.TrimSpace(c.QueryParam("group_by")),
	}
	if filter.GroupBy == "" {
		filter.GroupBy = "day"
	}
	now := time.Now()
	if days, ok := analyticsRangeDays(c.QueryParam("range")); ok {
		filter.DateFrom = now.AddDate(0, 0, -days)
		filter.DateTo = now
	} else if c.QueryParam("range") == "all" {
		// no date bounds
	} else {
		filter.DateFrom = now.AddDate(0, 0, -30)
		filter.DateTo = now
	}
	return filter
}

func (h *Handler) enabledSkillsForAnalytics(c echo.Context) []repository.EnabledSkillInfo {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	projectRoot := h.currentProjectSkillRoot(c)
	out := []repository.EnabledSkillInfo{}
	alwaysGlobal := alwaysUseSet(h.agentSkillRoot)
	alwaysProject := alwaysUseSet(projectRoot)
	if catalog, err := agentskills.BuildCatalogAll("skill-analytics", h.agentSkillRoot, projectRoot); err == nil {
		for _, entry := range catalog.Entries() {
			scope := models.SkillScopeGlobal
			always := alwaysGlobal[entry.Handle]
			if entry.Source == agentskills.SourceProject {
				scope = models.SkillScopeProject
				always = alwaysProject[entry.Handle]
			}
			out = append(out, repository.EnabledSkillInfo{Handle: entry.Handle, Scope: scope, Enabled: true, AlwaysUse: always})
		}
	}
	if h.agentRepo != nil {
		agents, err := h.agentRepo.ListSkillCatalogRefs(c.Request().Context())
		if err == nil {
			seen := map[string]bool{}
			for _, agent := range agents {
				if projectID != "" && agent.ProjectID != "" && agent.ProjectID != projectID {
					continue
				}
				projectForAgent := projectRoot
				if agent.ProjectID != "" && agent.ProjectID != projectID && h.projectRepo != nil {
					projectForAgent = serviceProjectSkillRoot(c, h, agent.ProjectID)
				}
				catalog, err := agentskills.BuildAgentCatalog("skill-analytics-agent", h.agentSkillRoot, projectForAgent, agent.Key)
				if err != nil {
					continue
				}
				for _, entry := range catalog.Entries() {
					key := agent.ID + "\x00" + entry.Handle
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, repository.EnabledSkillInfo{Handle: entry.Handle, Scope: models.SkillScopeAgentOwned, Enabled: true})
				}
			}
		}
	}
	return out
}

func serviceProjectSkillRoot(c echo.Context, h *Handler, projectID string) string {
	if h == nil || h.projectRepo == nil || strings.TrimSpace(projectID) == "" {
		return ""
	}
	return service.ProjectSkillRootForResolver(c.Request().Context(), h.projectRepo, projectID)
}

func parseUsageFilter(c echo.Context) repository.UsageFilter {
	filter := repository.UsageFilter{
		ProjectID: c.QueryParam("project_id"),
		Provider:  c.QueryParam("provider"),
		GroupBy:   c.QueryParam("group_by"),
		Refresh:   c.QueryParam("refresh") == "true" || c.QueryParam("refresh") == "1",
	}
	if filter.GroupBy == "" {
		filter.GroupBy = "day"
	}
	if from := parseAnalyticsTime(c.QueryParam("date_from")); !from.IsZero() {
		filter.DateFrom = from
	}
	if to := parseAnalyticsTime(c.QueryParam("date_to")); !to.IsZero() {
		filter.DateTo = to
	}
	if filter.DateFrom.IsZero() && filter.DateTo.IsZero() {
		// Use local time so the range boundaries match the user's calendar day,
		// consistent with how the Schedules page uses time.Local / time.Now().
		now := time.Now()
		if days, ok := analyticsRangeDays(c.QueryParam("range")); ok {
			filter.DateFrom = now.AddDate(0, 0, -days)
			filter.DateTo = now
		} else {
			switch c.QueryParam("range") {
			case "month":
				// Start of the current local month at midnight local time.
				filter.DateFrom = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
				filter.DateTo = now
			case "all":
				// no date bounds
			default:
				filter.DateFrom = now.AddDate(0, 0, -30)
				filter.DateTo = now
			}
		}
	}
	return filter
}

func analyticsRangeDays(value string) (int, bool) {
	if !strings.HasSuffix(value, "d") {
		return 0, false
	}
	days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
	if err != nil || days <= 0 {
		return 0, false
	}
	return days, true
}

func parseAnalyticsTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC()
	}
	return time.Time{}
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
		applog.Infof("[handler] GetSuccessFailureRates error: %v", err)
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
		applog.Infof("[handler] GetAvgExecutionTimeByTask error: %v", err)
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
		applog.Infof("[handler] GetAvgExecutionTimeByAgent error: %v", err)
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
		applog.Infof("[handler] GetExecutionTrendsByHour error: %v", err)
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
		applog.Infof("[handler] GetAgentUsageByProject error: %v", err)
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
		applog.Infof("[handler] GetMostFrequentTasks error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, frequencies)
}

// GetFailedTaskPatterns returns tasks with failure patterns
// @Summary Get failed task patterns
// @Description Returns task-level failure patterns with the latest observed error.
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
		applog.Infof("[handler] GetFailedTaskPatterns error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, patterns)
}
