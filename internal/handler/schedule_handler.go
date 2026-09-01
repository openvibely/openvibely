package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// GlobalCapacityResponse contains global worker capacity information.
// swagger:model

func parseScheduleRepeatInterval(raw string) (int, error) {
	if raw == "" {
		return 1, nil
	}
	interval, err := strconv.Atoi(raw)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "repeat interval must be a whole number")
	}
	if err := models.ValidateScheduleRepeatInterval(interval); err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return interval, nil
}

type scheduleFormValues struct {
	runAt          time.Time
	repeatType     models.RepeatType
	repeatInterval int
}

func parseScheduleForm(c echo.Context, defaultRepeatType models.RepeatType) (scheduleFormValues, error) {
	repeatInterval, err := parseScheduleRepeatInterval(c.FormValue("repeat_interval"))
	if err != nil {
		return scheduleFormValues{}, err
	}
	runAt, err := time.ParseInLocation("2006-01-02T15:04", c.FormValue("run_at"), time.Local)
	if err != nil {
		return scheduleFormValues{}, err
	}
	repeatType := models.RepeatType(strings.ToLower(strings.TrimSpace(c.FormValue("repeat_type"))))
	if repeatType == "" {
		repeatType = defaultRepeatType
	}
	switch repeatType {
	case models.RepeatOnce, models.RepeatSeconds, models.RepeatMinutes, models.RepeatHours,
		models.RepeatDaily, models.RepeatWeekly, models.RepeatMonthly:
	default:
		return scheduleFormValues{}, echo.NewHTTPError(http.StatusBadRequest, "invalid repeat type")
	}
	return scheduleFormValues{
		runAt:          runAt.UTC(),
		repeatType:     repeatType,
		repeatInterval: repeatInterval,
	}, nil
}

func scheduleFormHTTPError(err error) error {
	var parseErr *time.ParseError
	if errors.As(err, &parseErr) {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid date/time format")
	}
	return err
}

func (h *Handler) mutationProjectID(c echo.Context) string {
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" {
		return projectID
	}
	if h.settingsRepo == nil {
		return ""
	}
	selectedProjectID, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
	if err != nil {
		applog.Debugf("[handler] failed to load selected project preference for schedule mutation: %v", err)
		return ""
	}
	return strings.TrimSpace(selectedProjectID)
}

func (h *Handler) requireTaskInRequestProject(ctx context.Context, taskID, projectID string) (*models.Task, error) {
	var (
		task *models.Task
		err  error
	)
	if h.taskSvc != nil {
		task, err = h.taskSvc.GetByID(ctx, taskID)
	} else if h.taskRepo != nil {
		task, err = h.taskRepo.GetByID(ctx, taskID)
	} else {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, "task repository not available")
	}
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if projectID != "" && task.ProjectID != projectID {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "task does not belong to the active project")
	}
	return task, nil
}

func (h *Handler) requireScheduleInRequestProject(ctx context.Context, scheduleID, projectID string) (*models.Schedule, *models.Task, error) {
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	if err != nil {
		return nil, nil, err
	}
	if schedule == nil {
		return nil, nil, echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}
	task, err := h.requireTaskInRequestProject(ctx, schedule.TaskID, projectID)
	if err != nil {
		if httpErr, ok := err.(*echo.HTTPError); ok && httpErr.Code == http.StatusBadRequest {
			return nil, nil, echo.NewHTTPError(http.StatusBadRequest, "schedule does not belong to the active project")
		}
		return nil, nil, err
	}
	return schedule, task, nil
}

func (h *Handler) scheduleAgentAssignmentFromForm(c echo.Context, taskID string) (bool, *string, error) {
	if c.FormValue("schedule_agent_definition_present") == "" {
		return false, nil, nil
	}
	task, err := h.requireTaskInRequestProject(c.Request().Context(), taskID, h.mutationProjectID(c))
	if err != nil {
		return false, nil, err
	}
	agentDefinitionID, err := h.resolvePrimaryAgentDefinition(c.Request().Context(), task.ProjectID, c.FormValue("agent_definition_id"))
	if err != nil {
		return false, nil, err
	}
	return true, agentDefinitionID, nil
}

func (h *Handler) renderScheduleTaskDetail(c echo.Context, taskID, taskLookupErrorLog string, taskLookupErrorsAsNotFound bool) error {
	err := h.renderTaskDetailContent(c, taskID, "schedules")
	if err != nil {
		if taskLookupErrorLog != "" {
			applog.Infof("[handler] %s: %v", taskLookupErrorLog, err)
		}
		if taskLookupErrorsAsNotFound {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
	}
	return err
}

func (h *Handler) redirectToTaskSchedules(c echo.Context, taskID string) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" && h.taskSvc != nil {
		if task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID); task != nil {
			projectID = task.ProjectID
		}
	}
	redirectURL := "/tasks/" + taskID + "?tab=schedules"
	if projectID != "" {
		redirectURL += "&project_id=" + projectID
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *Handler) CreateSchedule(c echo.Context) error {
	taskID := c.Param("taskId")
	isHTMX := isHTMX(c)

	runAtStr := c.FormValue("run_at")
	applog.Infof("[handler] CreateSchedule task=%s run_at=%q repeat_type=%s interval=%s htmx=%v",
		taskID, runAtStr, c.FormValue("repeat_type"), c.FormValue("repeat_interval"), isHTMX)

	formValues, err := parseScheduleForm(c, models.RepeatOnce)
	if err != nil {
		if _, ok := err.(*time.ParseError); ok {
			applog.Infof("[handler] CreateSchedule invalid date: %v", err)
		}
		return scheduleFormHTTPError(err)
	}

	clearContextOnStart := formBoolEnabled(c, "clear_context_on_start", true)

	if _, err := h.requireTaskInRequestProject(c.Request().Context(), taskID, h.mutationProjectID(c)); err != nil {
		return err
	}

	agentAssignmentPresent, agentDefinitionID, err := h.scheduleAgentAssignmentFromForm(c, taskID)
	if err != nil {
		return err
	}

	result, err := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc).CreateAbsoluteForTask(c.Request().Context(), service.CreateAbsoluteScheduleForTaskRequest{
		ProjectID:              h.mutationProjectID(c),
		TaskID:                 taskID,
		RunAt:                  formValues.runAt,
		RepeatType:             formValues.repeatType,
		RepeatInterval:         formValues.repeatInterval,
		ClearContextOnStart:    &clearContextOnStart,
		AgentDefinitionPresent: agentAssignmentPresent,
		AgentDefinitionID:      agentDefinitionID,
	})
	if err != nil {
		applog.Infof("[handler] CreateSchedule error: %v", err)
		return err
	}
	applog.Infof("[handler] CreateSchedule success id=%s next_run=%v", result.Schedule.ID, result.Schedule.NextRun)
	for _, warning := range result.Warnings {
		applog.Infof("[handler] CreateSchedule warning: %v", warning)
	}

	// For HTMX requests, return the updated task detail content
	if isHTMX {
		return h.renderScheduleTaskDetail(c, taskID, "CreateSchedule error fetching task", false)
	}

	return h.redirectToTaskSchedules(c, taskID)
}

func (h *Handler) UpdateSchedule(c echo.Context) error {
	id := c.Param("id")
	isHTMX := isHTMX(c)

	runAtStr := c.FormValue("run_at")
	applog.Infof("[handler] UpdateSchedule id=%s run_at=%q repeat_type=%s interval=%s htmx=%v",
		id, runAtStr, c.FormValue("repeat_type"), c.FormValue("repeat_interval"), isHTMX)

	// Get the existing schedule and verify it belongs to the requested project.
	schedule, _, err := h.requireScheduleInRequestProject(c.Request().Context(), id, h.mutationProjectID(c))
	if err != nil {
		applog.Infof("[handler] UpdateSchedule error getting schedule: %v", err)
		return err
	}

	agentAssignmentPresent, agentDefinitionID, err := h.scheduleAgentAssignmentFromForm(c, schedule.TaskID)
	if err != nil {
		return err
	}

	formValues, err := parseScheduleForm(c, models.RepeatOnce)
	if err != nil {
		if _, ok := err.(*time.ParseError); ok {
			applog.Infof("[handler] UpdateSchedule invalid date: %v", err)
		}
		return scheduleFormHTTPError(err)
	}

	var clearContextOnStart *bool
	if _, present := c.Request().PostForm["clear_context_on_start"]; present {
		clearContext := formBoolEnabled(c, "clear_context_on_start", schedule.ClearContextOnStart)
		clearContextOnStart = &clearContext
	}

	result, err := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc).ModifyAbsolute(c.Request().Context(), service.ModifyAbsoluteScheduleRequest{
		ProjectID:              h.mutationProjectID(c),
		ScheduleID:             schedule.ID,
		RunAt:                  formValues.runAt,
		RepeatType:             formValues.repeatType,
		RepeatInterval:         formValues.repeatInterval,
		ClearContextOnStart:    clearContextOnStart,
		AgentDefinitionPresent: agentAssignmentPresent,
		AgentDefinitionID:      agentDefinitionID,
	})
	if err != nil {
		applog.Infof("[handler] UpdateSchedule error: %v", err)
		return err
	}
	schedule = result.Schedule
	applog.Infof("[handler] UpdateSchedule success id=%s next_run=%v", schedule.ID, schedule.NextRun)
	for _, warning := range result.Warnings {
		applog.Infof("[handler] UpdateSchedule warning: %v", warning)
	}

	// For HTMX requests, return the updated task detail content
	if isHTMX {
		return h.renderScheduleTaskDetail(c, schedule.TaskID, "UpdateSchedule error fetching task", false)
	}

	return h.redirectToTaskSchedules(c, schedule.TaskID)
}

type scheduleToggleResult struct {
	schedule       *models.Schedule
	errorOperation string
}

func scheduleToggleHTTPError(err error) error {
	var actionErr *service.ScheduleActionError
	if !errors.As(err, &actionErr) {
		return err
	}
	switch actionErr.Kind {
	case service.ScheduleActionReferenceError:
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	case service.ScheduleActionTimeError, service.ScheduleActionRepeatError,
		service.ScheduleActionDaysError, service.ScheduleActionIntervalError:
		return echo.NewHTTPError(http.StatusBadRequest, actionErr.Error())
	default:
		return err
	}
}

// toggleScheduleEnabled performs the schedule state transition shared by the
// browser and JSON API transports.
func (h *Handler) toggleScheduleEnabled(ctx context.Context, id, projectID string) (scheduleToggleResult, error) {
	var result scheduleToggleResult
	if schedule, _, err := h.requireScheduleInRequestProject(ctx, id, projectID); err != nil {
		result.errorOperation = "lookup"
		return result, err
	} else {
		result.schedule = schedule
	}

	actionResult, err := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc).Toggle(ctx, projectID, id)
	if err != nil {
		result.errorOperation = "toggle"
		return result, err
	}
	if actionResult == nil || actionResult.Schedule == nil {
		result.errorOperation = "toggle"
		return result, echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}
	result.schedule = actionResult.Schedule
	return result, nil
}

// ToggleScheduleEnabled pauses or resumes a schedule.
// When re-enabling a schedule whose NextRun has already passed, the shared
// schedule action service recomputes the next recurring occurrence.
func (h *Handler) ToggleScheduleEnabled(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()

	result, err := h.toggleScheduleEnabled(ctx, id, h.mutationProjectID(c))
	if err != nil {
		if result.errorOperation == "lookup" {
			applog.Infof("[handler] ToggleScheduleEnabled error getting schedule: %v", err)
		} else {
			applog.Infof("[handler] ToggleScheduleEnabled error toggling: %v", err)
		}
		if isHTMX(c) && result.schedule != nil {
			var actionErr *service.ScheduleActionError
			if errors.As(err, &actionErr) && actionErr.Kind == service.ScheduleActionTimeError {
				setHTMXToast(c, actionErr.Error(), "failed")
				return h.renderScheduleTaskDetail(c, result.schedule.TaskID, "", true)
			}
		}
		return scheduleToggleHTTPError(err)
	}
	if result.schedule == nil {
		applog.Infof("[handler] ToggleScheduleEnabled schedule not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}
	applog.Infof("[handler] ToggleScheduleEnabled id=%s enabled=%v", id, result.schedule.Enabled)

	taskID := result.schedule.TaskID
	if isHTMX(c) {
		message := "Schedule paused"
		if result.schedule.Enabled {
			message = "Schedule resumed"
		}
		setHTMXToast(c, message, "success")
		return h.renderScheduleTaskDetail(c, taskID, "", true)
	}

	return h.redirectToTaskSchedules(c, taskID)
}

// APIToggleScheduleEnabled pauses or resumes a schedule (JSON API).
// @Summary Toggle schedule enabled state
// @Description Pauses a running schedule or resumes a paused one. When re-enabling, NextRun is recomputed if stale.
// @Tags schedules
// @Produce json
// @Param id path string true "Schedule ID"
// @Success 200 {object} models.Schedule "Updated schedule"
// @Failure 404 {object} ErrorResponse "Schedule not found"
// @Router /api/schedules/{id}/toggle [post]
func (h *Handler) APIToggleScheduleEnabled(c echo.Context) error {
	id := c.Param("id")
	result, err := h.toggleScheduleEnabled(c.Request().Context(), id, h.mutationProjectID(c))
	if err != nil {
		return scheduleToggleHTTPError(err)
	}
	if result.schedule == nil {
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}
	return c.JSON(http.StatusOK, result.schedule)
}

func (h *Handler) DeleteSchedule(c echo.Context) error {
	id := c.Param("id")
	applog.Infof("[handler] DeleteSchedule id=%s", id)

	if _, _, err := h.requireScheduleInRequestProject(c.Request().Context(), id, h.mutationProjectID(c)); err != nil {
		applog.Infof("[handler] DeleteSchedule error getting schedule: %v", err)
		return err
	}

	// Browser delete intentionally removes only the schedule row. Runtime
	// delete_schedule uses ScheduleActionService.Delete, which moves a scheduled
	// task back to Backlog when the last schedule is removed. The browser route
	// preserves its historical category behavior because callers remain on the
	// current page and may manage the task category separately.
	if err := h.scheduleRepo.Delete(c.Request().Context(), id); err != nil {
		applog.Infof("[handler] DeleteSchedule error: %v", err)
		return err
	}
	applog.Infof("[handler] DeleteSchedule success id=%s", id)

	if isHTMX(c) {
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/")
}

// buildModelWorkerStatsList returns per-model worker stats for configured model worker pools.
func (h *Handler) buildModelWorkerStatsList(ctx context.Context) []pages.ModelWorkerStats {
	agents, err := h.llmConfigRepo.ListWorkerCapacities(ctx)
	if err != nil {
		applog.Infof("[handler] buildModelWorkerStatsList error: %v", err)
		return nil
	}
	stats := make([]pages.ModelWorkerStats, 0, len(agents))
	for _, agent := range agents {
		stats = append(stats, pages.ModelWorkerStats{
			ID:         agent.ID,
			Name:       agent.Name,
			Model:      agent.Model,
			Running:    h.workerSvc.ModelRunning(agent.ID),
			MaxWorkers: agent.MaxWorkers,
		})
	}
	return stats
}

func (h *Handler) WorkerSettings(c echo.Context) error {
	isHTMX := isHTMX(c)
	applog.Infof("[handler] WorkerSettings requested htmx=%v", isHTMX)
	maxWorkers, _ := h.workerRepo.GetMaxWorkers(c.Request().Context())
	queueSize := h.workerSvc.QueueSize()
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()

	projects, _ := h.projectSvc.List(c.Request().Context())

	// Get pending task counts by project
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] WorkerSettings error counting pending tasks: %v", err)
		pendingCounts = make(map[string]int) // fallback to empty map
	}

	// Build per-project utilization
	projectStats := make([]pages.ProjectWorkerStats, len(projects))
	for i, p := range projects {
		projectStats[i] = pages.ProjectWorkerStats{
			ID:         p.ID,
			Name:       p.Name,
			Running:    h.workerSvc.ProjectRunning(p.ID),
			QueueSize:  pendingCounts[p.ID],
			MaxWorkers: p.MaxWorkers,
		}
	}

	// Build per-model utilization
	modelStats := h.buildModelWorkerStatsList(c.Request().Context())

	applog.Infof("[handler] WorkerSettings max_workers=%d running_workers=%d total_running=%d queue_size=%d",
		maxWorkers, runningWorkers, totalRunning, queueSize)

	// For HTMX requests, return just the worker settings content
	if isHTMX {
		return render(c, http.StatusOK, pages.WorkerSettingsContent(maxWorkers, runningWorkers, totalRunning, queueSize, projectStats, modelStats))
	}

	currentProjectID, _ := h.getCurrentProjectID(c)

	return render(c, http.StatusOK, pages.WorkerSettings(projects, currentProjectID, maxWorkers, runningWorkers, totalRunning, queueSize, projectStats, modelStats))
}

func (h *Handler) UpdateWorkerSettings(c echo.Context) error {
	maxWorkers, err := strconv.Atoi(c.FormValue("max_workers"))
	if err != nil || maxWorkers < 0 {
		maxWorkers = 0
	}
	if maxWorkers > 10 {
		maxWorkers = 10
	}
	applog.Infof("[handler] UpdateWorkerSettings max_workers=%d", maxWorkers)

	if err := h.workerRepo.SetMaxWorkers(c.Request().Context(), maxWorkers); err != nil {
		applog.Infof("[handler] UpdateWorkerSettings error: %v", err)
		return err
	}

	// Apply the new worker count to the running worker pool
	h.workerSvc.Resize(maxWorkers)
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()
	applog.Infof("[handler] UpdateWorkerSettings success, resized to %d workers (actual running: %d)", maxWorkers, runningWorkers)

	// For HTMX requests, return the updated content instead of redirecting
	isHTMX := isHTMX(c)
	if isHTMX {
		queueSize := h.workerSvc.QueueSize()

		projects, _ := h.projectSvc.List(c.Request().Context())
		pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
		if err != nil {
			applog.Infof("[handler] UpdateWorkerSettings error counting pending tasks: %v", err)
			pendingCounts = make(map[string]int)
		}
		projectStats := make([]pages.ProjectWorkerStats, len(projects))
		for i, p := range projects {
			projectStats[i] = pages.ProjectWorkerStats{
				ID:         p.ID,
				Name:       p.Name,
				Running:    h.workerSvc.ProjectRunning(p.ID),
				QueueSize:  pendingCounts[p.ID],
				MaxWorkers: p.MaxWorkers,
			}
		}

		modelStats := h.buildModelWorkerStatsList(c.Request().Context())
		return render(c, http.StatusOK, pages.WorkerSettingsContent(maxWorkers, runningWorkers, totalRunning, queueSize, projectStats, modelStats))
	}

	return c.Redirect(http.StatusSeeOther, "/workers")
}

// GlobalWorkerStats returns just the global worker pool stats for polling
func (h *Handler) GlobalWorkerStats(c echo.Context) error {
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()
	queueSize := h.workerSvc.QueueSize()

	return render(c, http.StatusOK, pages.GlobalWorkerStats(runningWorkers, totalRunning, queueSize))
}

// ProjectWorkerStats returns just the project stats table body for polling
func (h *Handler) ProjectWorkerStats(c echo.Context) error {
	projects, _ := h.projectSvc.List(c.Request().Context())

	// Get pending task counts by project
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] ProjectWorkerStats error counting pending tasks: %v", err)
		pendingCounts = make(map[string]int)
	}

	// Build per-project utilization
	projectStats := make([]pages.ProjectWorkerStats, len(projects))
	for i, p := range projects {
		projectStats[i] = pages.ProjectWorkerStats{
			ID:         p.ID,
			Name:       p.Name,
			Running:    h.workerSvc.ProjectRunning(p.ID),
			QueueSize:  pendingCounts[p.ID],
			MaxWorkers: p.MaxWorkers,
		}
	}

	maxWorkers, _ := h.workerRepo.GetMaxWorkers(c.Request().Context())
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()
	queueSize := h.workerSvc.QueueSize()

	return render(c, http.StatusOK, pages.ProjectStatsTableBody(maxWorkers, runningWorkers, totalRunning, queueSize, projectStats))
}

// ModelWorkerStats returns per-model worker stats for polling
func (h *Handler) ModelWorkerStats(c echo.Context) error {
	modelStats := h.buildModelWorkerStatsList(c.Request().Context())
	return render(c, http.StatusOK, pages.ModelStatsTableBody(modelStats))
}

func parseScheduleSelection(anchorID, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{anchorID}, nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) > 100 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "too many schedules selected")
		}
	}
	if len(ids) == 0 {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "no schedules selected")
	}
	if _, ok := seen[anchorID]; !ok {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "dragged schedule is not in the selection")
	}
	return ids, nil
}

func updateScheduleAfterMove(schedule *models.Schedule, move func(time.Time) time.Time, now time.Time) {
	if schedule.RepeatType.IsSubDaily() {
		base := schedule.RunAt
		if schedule.NextRun != nil {
			base = *schedule.NextRun
		}
		next := move(base)
		schedule.NextRun = &next
	} else {
		schedule.RunAt = move(schedule.RunAt)
		if schedule.NextRun != nil {
			next := move(*schedule.NextRun)
			schedule.NextRun = &next
		} else {
			next := schedule.RunAt
			schedule.NextRun = &next
		}
	}
	if schedule.NextRun != nil && schedule.NextRun.Before(now) {
		if nextRun := schedule.ComputeNextRun(now); nextRun != nil && nextRun.After(now) {
			schedule.NextRun = nextRun
		}
	}
}

func (h *Handler) RescheduleTask(c echo.Context) error {
	scheduleID := c.Param("scheduleId")
	newDateStr := c.FormValue("new_date")
	hourStr := c.FormValue("hour")
	applog.Infof("[handler] RescheduleTask schedule=%s new_date=%s hour=%s", scheduleID, newDateStr, hourStr)

	newDate, err := time.Parse("2006-01-02", newDateStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid date format")
	}
	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 0 || hour > 23 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid hour")
	}
	ids, err := parseScheduleSelection(scheduleID, c.FormValue("schedule_ids"))
	if err != nil {
		return err
	}
	projectID := h.mutationProjectID(c)

	// Preserve the established exact-target behavior for ordinary single-card drags.
	if len(ids) == 1 {
		schedule, task, err := h.requireScheduleInRequestProject(c.Request().Context(), scheduleID, projectID)
		if err != nil {
			return err
		}
		runAtLocal := schedule.RunAt.Local()
		newScheduleTime := time.Date(newDate.Year(), newDate.Month(), newDate.Day(), hour, runAtLocal.Minute(), runAtLocal.Second(), 0, time.Local).UTC()
		updateScheduleAfterMove(schedule, func(time.Time) time.Time { return newScheduleTime }, time.Now())
		if err := h.scheduleRepo.UpdateForTask(c.Request().Context(), schedule, task.ID); err != nil {
			applog.Infof("[handler] RescheduleTask error updating schedule: %v", err)
			return err
		}
	} else {
		if projectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "project_id is required for grouped rescheduling")
		}
		sourceDate, err := time.Parse("2006-01-02", c.FormValue("source_date"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid source date format")
		}
		sourceHour, err := strconv.Atoi(c.FormValue("source_hour"))
		if err != nil || sourceHour < 0 || sourceHour > 23 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid source hour")
		}
		calendarDayDelta := int(time.Date(newDate.Year(), newDate.Month(), newDate.Day(), 0, 0, 0, 0, time.UTC).
			Sub(time.Date(sourceDate.Year(), sourceDate.Month(), sourceDate.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
		hourDelta := hour - sourceHour
		move := func(value time.Time) time.Time {
			local := value.Local().AddDate(0, 0, calendarDayDelta)
			moved := time.Date(local.Year(), local.Month(), local.Day(), local.Hour()+hourDelta,
				local.Minute(), local.Second(), local.Nanosecond(), time.Local)
			return moved.UTC()
		}
		for _, id := range ids {
			if _, _, err := h.requireScheduleInRequestProject(c.Request().Context(), id, projectID); err != nil {
				return err
			}
		}
		now := time.Now()
		if err := h.scheduleRepo.UpdateBatchForProject(c.Request().Context(), projectID, ids, func(schedule *models.Schedule) error {
			updateScheduleAfterMove(schedule, move, now)
			return nil
		}); err != nil {
			return err
		}
	}

	applog.Infof("[handler] RescheduleTask success schedule=%s selected=%d", scheduleID, len(ids))
	if isHTMX(c) {
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, "/")
}

func (h *Handler) GetExecution(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()
	applog.Infof("[handler] GetExecution id=%s", id)

	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		applog.Infof("[handler] GetExecution project error: %v", err)
		return err
	}

	exec, err := h.execRepo.GetByIDForProject(ctx, id, projectID)
	if err != nil {
		applog.Infof("[handler] GetExecution error: %v", err)
		return err
	}
	if exec == nil {
		applog.Infof("[handler] GetExecution not found id=%s project=%s", id, projectID)
		return echo.NewHTTPError(http.StatusNotFound, "execution not found")
	}

	task, err := h.taskSvc.GetByID(ctx, exec.TaskID)
	if err != nil {
		applog.Infof("[handler] GetExecution task error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] GetExecution task not found task=%s project=%s", exec.TaskID, projectID)
		return echo.NewHTTPError(http.StatusNotFound, "execution not found")
	}
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)

	applog.Infof("[handler] GetExecution id=%s project=%s status=%s tokens=%d duration=%dms",
		id, projectID, exec.Status, exec.TokensUsed, exec.DurationMs)
	return render(c, http.StatusOK, pages.ExecutionDetail(projects, exec, task))
}

func (h *Handler) UpdateProjectWorkerLimit(c echo.Context) error {
	projectID := c.Param("projectId")
	maxWorkersStr := c.FormValue("max_workers")

	maxWorkers, err := strconv.Atoi(maxWorkersStr)
	if err != nil || maxWorkers < 0 {
		maxWorkers = 0 // 0 means no limit
	}
	if maxWorkers > 10 {
		maxWorkers = 10 // Cap at 10
	}

	applog.Infof("[handler] UpdateProjectWorkerLimit project=%s max_workers=%d", projectID, maxWorkers)

	// Get the project
	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit error getting project: %v", err)
		return err
	}
	if project == nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit project not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	// Update max_workers (0 or nil means no limit)
	if maxWorkers == 0 {
		project.MaxWorkers = nil
	} else {
		project.MaxWorkers = &maxWorkers
	}

	if err := h.projectSvc.Update(c.Request().Context(), project); err != nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit error updating project: %v", err)
		return err
	}

	applog.Infof("[handler] UpdateProjectWorkerLimit success project=%s max_workers=%v", projectID, project.MaxWorkers)

	// Trigger dispatch check — if the limit was increased and there are queued
	// tasks for this project, they should start immediately.
	h.workerSvc.DispatchNext()

	// Return the updated worker settings content for HTMX
	maxGlobalWorkers, _ := h.workerRepo.GetMaxWorkers(c.Request().Context())
	queueSize := h.workerSvc.QueueSize()
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()

	projects, _ := h.projectSvc.List(c.Request().Context())
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] UpdateProjectWorkerLimit error counting pending tasks: %v", err)
		pendingCounts = make(map[string]int)
	}

	projectStats := make([]pages.ProjectWorkerStats, len(projects))
	for i, p := range projects {
		projectStats[i] = pages.ProjectWorkerStats{
			ID:         p.ID,
			Name:       p.Name,
			Running:    h.workerSvc.ProjectRunning(p.ID),
			QueueSize:  pendingCounts[p.ID],
			MaxWorkers: p.MaxWorkers,
		}
	}

	modelStats := h.buildModelWorkerStatsList(c.Request().Context())
	return render(c, http.StatusOK, pages.WorkerSettingsContent(maxGlobalWorkers, runningWorkers, totalRunning, queueSize, projectStats, modelStats))
}

// API endpoints for capacity information

type GlobalCapacityResponse struct {
	MaxWorkers     int  `json:"max_workers"`
	TotalRunning   int  `json:"total_running"`
	QueueSize      int  `json:"queue_size"`
	HasCapacity    bool `json:"has_capacity"`
	AvailableSlots int  `json:"available_slots"`
}

type ProjectCapacityResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Running        int    `json:"running"`
	QueueSize      int    `json:"queue_size"`
	MaxWorkers     *int   `json:"max_workers"`
	HasCapacity    bool   `json:"has_capacity"`
	AvailableSlots *int   `json:"available_slots,omitempty"`
}

type ModelCapacityResponse struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Model          string `json:"model"`
	Running        int    `json:"running"`
	MaxWorkers     int    `json:"max_workers"`
	HasCapacity    bool   `json:"has_capacity"`
	AvailableSlots int    `json:"available_slots"`
}

func modelCapacityResponse(agent *models.LLMConfig, running int, hasCapacity bool) ModelCapacityResponse {
	availableSlots := 0
	if agent.MaxWorkers > 0 {
		availableSlots = agent.MaxWorkers - running
		if availableSlots < 0 {
			availableSlots = 0
		}
	}

	return ModelCapacityResponse{
		ID:             agent.ID,
		Name:           agent.Name,
		Model:          agent.Model,
		Running:        running,
		MaxWorkers:     agent.MaxWorkers,
		HasCapacity:    hasCapacity,
		AvailableSlots: availableSlots,
	}
}

// GetGlobalCapacity returns global worker pool capacity information (API endpoint)
// @Summary Get global worker capacity
// @Description Returns global worker pool usage and available slots.
// @Tags capacity
// @Produce json
// @Success 200 {object} GlobalCapacityResponse "Global capacity information"
// @Router /api/capacity/global [get]
func (h *Handler) GetGlobalCapacity(c echo.Context) error {
	maxWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()
	queueSize := h.workerSvc.QueueSize()
	hasCapacity := maxWorkers <= 0 || totalRunning < maxWorkers
	availableSlots := 0
	if maxWorkers > 0 {
		availableSlots = maxWorkers - totalRunning
		if availableSlots < 0 {
			availableSlots = 0
		}
	}

	resp := GlobalCapacityResponse{
		MaxWorkers:     maxWorkers,
		TotalRunning:   totalRunning,
		QueueSize:      queueSize,
		HasCapacity:    hasCapacity,
		AvailableSlots: availableSlots,
	}

	return c.JSON(http.StatusOK, resp)
}

// GetProjectCapacities returns per-project capacity information (API endpoint)
// @Summary Get project worker capacities
// @Description Returns worker capacity and queue information for each project.
// @Tags capacity
// @Produce json
// @Success 200 {array} ProjectCapacityResponse "Per-project capacity information"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/capacity/projects [get]
func (h *Handler) GetProjectCapacities(c echo.Context) error {
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] GetProjectCapacities error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list projects")
	}

	// Get pending task counts by project
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] GetProjectCapacities error counting pending tasks: %v", err)
		pendingCounts = make(map[string]int)
	}

	capacities := make([]ProjectCapacityResponse, len(projects))
	for i, p := range projects {
		running := h.workerSvc.ProjectRunning(p.ID)
		hasCapacity := h.workerSvc.HasProjectCapacity(p.ID)

		var availableSlots *int
		if p.MaxWorkers != nil && *p.MaxWorkers > 0 {
			slots := *p.MaxWorkers - running
			if slots < 0 {
				slots = 0
			}
			availableSlots = &slots
		}

		capacities[i] = ProjectCapacityResponse{
			ID:             p.ID,
			Name:           p.Name,
			Running:        running,
			QueueSize:      pendingCounts[p.ID],
			MaxWorkers:     p.MaxWorkers,
			HasCapacity:    hasCapacity,
			AvailableSlots: availableSlots,
		}
	}

	return c.JSON(http.StatusOK, capacities)
}

// GetProjectCapacity returns capacity information for a specific project (API endpoint)
// @Summary Get project worker capacity
// @Description Returns worker capacity and queue information for a specific project.
// @Tags capacity
// @Produce json
// @Param projectId path string true "Project ID"
// @Success 200 {object} ProjectCapacityResponse "Project capacity information"
// @Failure 404 {object} ErrorResponse "Project not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/capacity/projects/{projectId} [get]
func (h *Handler) GetProjectCapacity(c echo.Context) error {
	projectID := c.Param("projectId")

	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] GetProjectCapacity error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get project")
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	running := h.workerSvc.ProjectRunning(project.ID)
	hasCapacity := h.workerSvc.HasProjectCapacity(project.ID)

	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] GetProjectCapacity error counting pending tasks: %v", err)
		pendingCounts = make(map[string]int)
	}

	var availableSlots *int
	if project.MaxWorkers != nil && *project.MaxWorkers > 0 {
		slots := *project.MaxWorkers - running
		if slots < 0 {
			slots = 0
		}
		availableSlots = &slots
	}

	resp := ProjectCapacityResponse{
		ID:             project.ID,
		Name:           project.Name,
		Running:        running,
		QueueSize:      pendingCounts[project.ID],
		MaxWorkers:     project.MaxWorkers,
		HasCapacity:    hasCapacity,
		AvailableSlots: availableSlots,
	}

	return c.JSON(http.StatusOK, resp)
}

// GetModelCapacities returns per-model capacity information (API endpoint)
// @Summary Get model worker capacities
// @Description Returns worker capacity information for models that have explicit model-level limits.
// @Tags capacity
// @Produce json
// @Success 200 {array} ModelCapacityResponse "Per-model capacity information"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/capacity/models [get]
func (h *Handler) GetModelCapacities(c echo.Context) error {
	agents, err := h.llmConfigRepo.ListWorkerCapacities(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] GetModelCapacities error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list models")
	}

	capacities := make([]ModelCapacityResponse, 0, len(agents))
	for i := range agents {
		agent := &agents[i]
		running := h.workerSvc.ModelRunning(agent.ID)
		capacities = append(capacities, modelCapacityResponse(agent, running, running < agent.MaxWorkers))
	}

	return c.JSON(http.StatusOK, capacities)
}

// GetModelCapacity returns capacity information for a specific model (API endpoint)
// @Summary Get model worker capacity
// @Description Returns worker capacity information for a specific model configuration.
// @Tags capacity
// @Produce json
// @Param modelId path string true "Model configuration ID"
// @Success 200 {object} ModelCapacityResponse "Model capacity information"
// @Failure 404 {object} ErrorResponse "Model not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/capacity/models/{modelId} [get]
func (h *Handler) GetModelCapacity(c echo.Context) error {
	modelID := c.Param("modelId")

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), modelID)
	if err != nil {
		applog.Infof("[handler] GetModelCapacity error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get model")
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "model not found")
	}

	running := h.workerSvc.ModelRunning(agent.ID)
	hasCapacity := h.workerSvc.HasModelCapacity(agent.ID)
	resp := modelCapacityResponse(agent, running, hasCapacity)

	return c.JSON(http.StatusOK, resp)
}
