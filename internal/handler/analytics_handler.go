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
// @Description Returns token/cache/reasoning/cost totals, daily usage, usage rate, model breakdowns, account limit snapshots, and bounded supporting usage events for an exact chart selection.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter; required when requesting supporting usage evidence"
// @Param provider query string false "Provider filter"
// @Param range query string false "Convenience range: 7d, 30d, 90d, 365d, month, all" default(30d)
// @Param group_by query string false "Usage rate grouping: hour, day, week, month" default(day)
// @Param date_from query string false "Optional start datetime filter"
// @Param date_to query string false "Optional end datetime filter"
// @Param usage_period query string false "Exact grouped period for supporting usage events"
// @Param usage_provider query string false "Exact provider for supporting usage events"
// @Param usage_model_name query string false "Exact model for supporting usage events"
// @Param projection query string false "Optional compact projection; account_limits returns only provider/account-limit rows"
// @Success 200 {object} models.AnalyticsUsageViewModel "Usage analytics"
// @Failure 400 {object} ErrorResponse "Supporting evidence requires project_id"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/usage [get]
func (h *Handler) GetAnalyticsUsage(c echo.Context) error {
	if h.usageAnalyticsSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "usage analytics service is not configured")
	}
	filter := parseUsageFilter(c)
	if usageEvidenceRequested(filter) && strings.TrimSpace(filter.ProjectID) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required for supporting usage evidence")
	}
	if analyticsUsageProjection(c) == "account_limits" {
		view, err := h.usageAnalyticsSvc.BuildAnalyticsAccountLimits(c.Request().Context(), filter)
		if err != nil {
			applog.Infof("[handler] GetAnalyticsUsage account_limits error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, view)
	}
	view, err := h.usageAnalyticsSvc.BuildAnalyticsUsage(c.Request().Context(), filter)
	if err != nil {
		applog.Infof("[handler] GetAnalyticsUsage error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, view)
}

// GetSkillAnalytics returns the Skill Curator analytics dashboard data.
// @Summary Get skill analytics
// @Description Returns skill usage over time, top skills, selection follow-through, agent heatmap, underused skill metrics, and bounded supporting skill events for an exact chart selection.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter; required when requesting supporting skill evidence"
// @Param range query string false "Convenience range: 7d, 30d, 90d, 365d, all" default(30d)
// @Param group_by query string false "Usage trend grouping: day, week, or month" default(day)
// @Param agent_id query string false "Agent ID filter"
// @Param surface query string false "Surface filter"
// @Param skill_scope query string false "Skill scope filter"
// @Param event_type query string false "Event type filter (selected, loaded, viewed, created, edited)"
// @Param skill_period query string false "Exact grouped period for supporting skill events"
// @Param skill_event query string false "Supporting event type (used, followed, ignored, selected, loaded, viewed, created, edited)"
// @Param skill_agent query string false "Exact Agent ID or __unassigned__ for supporting skill events"
// @Param skill_handle query string false "Exact skill handle for supporting skill events"
// @Param skill_evidence_scope query string false "Exact skill scope for supporting skill events (global, project, agent_owned)"
// @Success 200 {object} models.SkillAnalyticsDashboard "Skill analytics"
// @Failure 400 {object} ErrorResponse "Supporting evidence requires project_id"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/skills [get]
func (h *Handler) GetSkillAnalytics(c echo.Context) error {
	if h.skillAnalyticsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "skill analytics repository is not configured")
	}
	filter := parseSkillAnalyticsFilter(c)
	if skillEvidenceRequested(filter) && strings.TrimSpace(filter.ProjectID) == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required for supporting skill evidence")
	}
	enabled := h.enabledSkillsForAnalytics(c)
	view, err := h.skillAnalyticsRepo.BuildDashboard(c.Request().Context(), filter, enabled)
	if err != nil {
		applog.Infof("[handler] GetSkillAnalytics error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, view)
}

func parseSkillAnalyticsFilter(c echo.Context) repository.SkillAnalyticsFilter {
	agentID := strings.TrimSpace(c.QueryParam("agent_id"))
	if agentID == "" {
		agentID = strings.TrimSpace(c.QueryParam("agent"))
	}
	filter := repository.SkillAnalyticsFilter{
		ProjectID:           strings.TrimSpace(c.QueryParam("project_id")),
		AgentID:             agentID,
		WorkflowID:          strings.TrimSpace(c.QueryParam("workflow")),
		Surface:             strings.TrimSpace(c.QueryParam("surface")),
		SkillScope:          strings.TrimSpace(c.QueryParam("skill_scope")),
		EventType:           strings.TrimSpace(c.QueryParam("event_type")),
		Limit:               10,
		GroupBy:             strings.TrimSpace(c.QueryParam("group_by")),
		EvidencePeriod:      strings.TrimSpace(c.QueryParam("skill_period")),
		EvidenceEvent:       strings.TrimSpace(c.QueryParam("skill_event")),
		EvidenceAgentID:     strings.TrimSpace(c.QueryParam("skill_agent")),
		EvidenceSkillHandle: strings.TrimSpace(c.QueryParam("skill_handle")),
		EvidenceSkillScope:  strings.TrimSpace(c.QueryParam("skill_evidence_scope")),
		EvidenceLimit:       50,
	}
	if filter.GroupBy == "" {
		filter.GroupBy = "day"
	}
	now := time.Now()
	if days, ok := service.ParseUsageAnalyticsRangeDays(c.QueryParam("range")); ok {
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

func skillEvidenceRequested(filter repository.SkillAnalyticsFilter) bool {
	return filter.EvidencePeriod != "" || filter.EvidenceEvent != "" || filter.EvidenceAgentID != "" || filter.EvidenceSkillHandle != "" || filter.EvidenceSkillScope != ""
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

func analyticsUsageProjection(c echo.Context) string {
	return strings.ToLower(strings.TrimSpace(c.QueryParam("projection")))
}

func parseUsageFilter(c echo.Context) repository.UsageFilter {
	filter, _ := service.NormalizeUsageFilter(service.UsageFilterInput{
		ProjectID:  c.QueryParam("project_id"),
		Provider:   c.QueryParam("provider"),
		AgentID:    c.QueryParam("agent"),
		WorkflowID: c.QueryParam("workflow"),
		GroupBy:    c.QueryParam("group_by"),
		Range:      c.QueryParam("range"),
		DateFrom:   c.QueryParam("date_from"),
		DateTo:     c.QueryParam("date_to"),
		Refresh:    c.QueryParam("refresh") == "true" || c.QueryParam("refresh") == "1",
	})
	filter.EvidencePeriod = strings.TrimSpace(c.QueryParam("usage_period"))
	filter.EvidenceProvider = strings.TrimSpace(c.QueryParam("usage_provider"))
	filter.EvidenceModel = strings.TrimSpace(c.QueryParam("usage_model_name"))
	filter.EvidenceLimit = 50
	return filter
}

func usageEvidenceRequested(filter repository.UsageFilter) bool {
	return filter.EvidencePeriod != "" || filter.EvidenceProvider != "" || filter.EvidenceModel != ""
}

// GetAnalyticsDashboard returns project-scoped task outcome, Agent, workflow,
// evidence, and comparison metrics for the Analytics evaluation views.// @Summary Get outcome-oriented Analytics dashboard data
// @Description Returns task-level outcome KPIs, definitions, comparisons, evidence, reusable Agent performance, observed skill outcomes, and workflow performance for one project.
// @Tags analytics
// @Produce json
// @Param project_id query string true "Authoritative project ID"
// @Param range query string false "Convenience range: 7d, 30d, 90d, 365d, month, all" default(30d)
// @Param date_from query string false "Optional inclusive start datetime"
// @Param date_to query string false "Optional exclusive end datetime"
// @Param compare query boolean false "Compare with the immediately preceding equivalent period"
// @Param group_by query string false "Trend grouping: day, week, or month" default(day)
// @Param view query string false "Active Analytics view: overview, outcomes, agents, automations, learning, or usage"
// @Param agent query string false "Reusable Agent definition ID or __unassigned__"
// @Param workflow query string false "Automation workflow ID"
// @Param evidence_limit query int false "Evidence rows per page, 1-100" default(20)
// @Param evidence_offset query int false "Evidence rows to skip" default(0)
// @Param evidence_skill_handle query string false "Exact skill handle for task-outcome evidence"
// @Param evidence_skill_scope query string false "Exact skill scope for task-outcome evidence"
// @Param evidence_skill_agent query string false "Exact reusable Agent definition ID or __unassigned__ for skill task-outcome evidence"
// @Success 200 {object} models.AnalyticsDashboard "Outcome Analytics dashboard"
// @Failure 400 {object} ErrorResponse "Missing project ID"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/analytics/dashboard [get]
func analyticsDashboardView(value string) string {
	view := strings.ToLower(strings.TrimSpace(value))
	switch view {
	case "overview", "outcomes", "agents", "automations", "learning", "usage":
		return view
	default:
		return "overview"
	}
}

func (h *Handler) GetAnalyticsDashboard(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	usageFilter := parseUsageFilter(c)
	evidenceLimit := 20
	if parsed, err := strconv.Atoi(c.QueryParam("evidence_limit")); err == nil && parsed > 0 && parsed <= 100 {
		evidenceLimit = parsed
	}
	evidenceOffset := 0
	if parsed, err := strconv.Atoi(c.QueryParam("evidence_offset")); err == nil && parsed > 0 {
		evidenceOffset = parsed
	}
	dashboard, err := h.execRepo.GetAnalyticsDashboard(c.Request().Context(), repository.AnalyticsDashboardFilter{
		ProjectID:            projectID,
		View:                 analyticsDashboardView(c.QueryParam("view")),
		DateFrom:             usageFilter.DateFrom,
		DateTo:               usageFilter.DateTo,
		Compare:              c.QueryParam("compare") == "1" || c.QueryParam("compare") == "true",
		Limit:                evidenceLimit,
		EvidenceOffset:       evidenceOffset,
		GroupBy:              c.QueryParam("group_by"),
		AgentID:              strings.TrimSpace(c.QueryParam("agent")),
		WorkflowID:           strings.TrimSpace(c.QueryParam("workflow")),
		EvidenceSkillHandle:  strings.TrimSpace(c.QueryParam("evidence_skill_handle")),
		EvidenceSkillScope:   strings.TrimSpace(c.QueryParam("evidence_skill_scope")),
		EvidenceSkillAgentID: strings.TrimSpace(c.QueryParam("evidence_skill_agent")),
	})
	if err != nil {
		applog.Infof("[handler] GetAnalyticsDashboard error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, dashboard)
}

func parseAnalyticsTime(value string) time.Time {
	return service.ParseUsageAnalyticsTime(value)
}

func analyticsSQLBound(value string) string {
	parsed := parseAnalyticsTime(value)
	if parsed.IsZero() {
		return ""
	}
	return parsed.UTC().Format("2006-01-02 15:04:05.999999999")
}

func analyticsDimensionParams(c echo.Context) []string {
	return []string{strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow"))}
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
	dateFrom := analyticsSQLBound(c.QueryParam("date_from"))
	dateTo := analyticsSQLBound(c.QueryParam("date_to"))

	rates, err := h.execRepo.GetSuccessFailureRates(c.Request().Context(), projectID, groupBy, dateFrom, dateTo, analyticsDimensionParams(c)...)
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

	times, err := h.execRepo.GetAvgExecutionTimeByTask(c.Request().Context(), projectID, limit, analyticsSQLBound(c.QueryParam("date_from")), analyticsSQLBound(c.QueryParam("date_to")), strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow")))
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

	times, err := h.execRepo.GetAvgExecutionTimeByAgent(c.Request().Context(), projectID, analyticsSQLBound(c.QueryParam("date_from")), analyticsSQLBound(c.QueryParam("date_to")), strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow")))
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
	dateFrom := analyticsSQLBound(c.QueryParam("date_from"))
	dateTo := analyticsSQLBound(c.QueryParam("date_to"))

	trends, err := h.execRepo.GetExecutionTrendsByHour(c.Request().Context(), projectID, dateFrom, dateTo, analyticsDimensionParams(c)...)
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

	usage, err := h.execRepo.GetAgentUsageByProject(c.Request().Context(), projectID, analyticsSQLBound(c.QueryParam("date_from")), analyticsSQLBound(c.QueryParam("date_to")), strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow")))
	if err != nil {
		applog.Infof("[handler] GetAgentUsageByProject error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, usage)
}

// GetMostFrequentTasks returns the most frequently executed tasks
// @Summary Get most frequent tasks
// @Description Returns tasks ordered by execution count descending, then task ID ascending for deterministic ties. Omitted limit defaults to 10; limit=0 returns all matching tasks.
// @Tags analytics
// @Produce json
// @Param project_id query string false "Project ID filter"
// @Param limit query int false "Maximum number of tasks to return; 0 returns all matching tasks" default(10)
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

	frequencies, err := h.execRepo.GetMostFrequentTasks(c.Request().Context(), projectID, limit, analyticsSQLBound(c.QueryParam("date_from")), analyticsSQLBound(c.QueryParam("date_to")), strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow")))
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

	patterns, err := h.execRepo.GetFailedTaskPatternsInRange(c.Request().Context(), projectID, limit, analyticsSQLBound(c.QueryParam("date_from")), analyticsSQLBound(c.QueryParam("date_to")), strings.TrimSpace(c.QueryParam("agent")), strings.TrimSpace(c.QueryParam("workflow")))
	if err != nil {
		applog.Infof("[handler] GetFailedTaskPatterns error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, patterns)
}
