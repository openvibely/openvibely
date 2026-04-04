package handler

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// GlobalCapacityResponse contains global worker capacity information.
// swagger:model

func (h *Handler) CreateSchedule(c echo.Context) error {
	taskID := c.Param("taskId")
	isHTMX := isHTMX(c)

	runAtStr := c.FormValue("run_at")
	log.Printf("[handler] CreateSchedule task=%s run_at=%q repeat_type=%s interval=%s htmx=%v",
		taskID, runAtStr, c.FormValue("repeat_type"), c.FormValue("repeat_interval"), isHTMX)

	// Parse the time in local timezone since the browser sends datetime-local values,
	// then convert to UTC for consistent storage
	runAt, err := time.ParseInLocation("2006-01-02T15:04", runAtStr, time.Local)
	if err != nil {
		log.Printf("[handler] CreateSchedule invalid date: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid date/time format")
	}
	runAt = runAt.UTC()

	repeatInterval, _ := strconv.Atoi(c.FormValue("repeat_interval"))
	if repeatInterval < 1 {
		repeatInterval = 1
	}

	s := &models.Schedule{
		TaskID:         taskID,
		RunAt:          runAt,
		RepeatType:     models.RepeatType(c.FormValue("repeat_type")),
		RepeatInterval: repeatInterval,
		Enabled:        true,
	}
	if s.RepeatType == "" {
		s.RepeatType = models.RepeatOnce
	}

	// For recurring schedules with a past RunAt, keep NextRun = RunAt so the
	// scheduler picks it up immediately on its next tick (within 5 seconds).
	// The scheduler will execute the task once for the missed occurrence and
	// then advance NextRun to the next future occurrence via ComputeNextRun.
	// Previously this pre-computed the next future occurrence, which skipped
	// the current day's execution (e.g., creating a daily 1:33 AM schedule
	// at 1:34 AM would skip today and not run until tomorrow).

	if err := h.scheduleRepo.Create(c.Request().Context(), s); err != nil {
		log.Printf("[handler] CreateSchedule error: %v", err)
		return err
	}
	log.Printf("[handler] CreateSchedule success id=%s next_run=%v", s.ID, s.NextRun)

	// For HTMX requests, return the updated task detail content
	if isHTMX {
		task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
		if err != nil {
			log.Printf("[handler] CreateSchedule error fetching task: %v", err)
			return err
		}
		if task == nil {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}

		executions, _ := h.execRepo.ListByTask(c.Request().Context(), taskID)
		schedules, _ := h.scheduleRepo.ListByTask(c.Request().Context(), taskID)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
		var adefs []models.Agent
		if h.agentRepo != nil {
			adefs, _ = h.agentRepo.List(c.Request().Context())
		}
		var rc []models.ReviewComment
		if h.reviewCommentRepo != nil {
			rc, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
		}

		return render(c, http.StatusOK, pages.TaskDetailContent(task, executions, schedules, agents, adefs, attachments, "schedules", rc))
	}

	return c.Redirect(http.StatusSeeOther, "/tasks/"+taskID)
}

func (h *Handler) UpdateSchedule(c echo.Context) error {
	id := c.Param("id")
	isHTMX := isHTMX(c)

	runAtStr := c.FormValue("run_at")
	log.Printf("[handler] UpdateSchedule id=%s run_at=%q repeat_type=%s interval=%s htmx=%v",
		id, runAtStr, c.FormValue("repeat_type"), c.FormValue("repeat_interval"), isHTMX)

	// Get the existing schedule
	schedule, err := h.scheduleRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] UpdateSchedule error getting schedule: %v", err)
		return err
	}
	if schedule == nil {
		log.Printf("[handler] UpdateSchedule schedule not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}

	// Parse the time in local timezone since the browser sends datetime-local values,
	// then convert to UTC for consistent storage
	runAt, err := time.ParseInLocation("2006-01-02T15:04", runAtStr, time.Local)
	if err != nil {
		log.Printf("[handler] UpdateSchedule invalid date: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid date/time format")
	}
	runAt = runAt.UTC()

	repeatInterval, _ := strconv.Atoi(c.FormValue("repeat_interval"))
	if repeatInterval < 1 {
		repeatInterval = 1
	}

	repeatType := models.RepeatType(c.FormValue("repeat_type"))
	if repeatType == "" {
		repeatType = models.RepeatOnce
	}

	// Update schedule fields
	schedule.RunAt = runAt
	schedule.RepeatType = repeatType
	schedule.RepeatInterval = repeatInterval

	// Set NextRun to RunAt. For recurring schedules with a past RunAt, the
	// scheduler will pick it up on its next tick, execute the task once for
	// the missed occurrence, and advance NextRun to the next future time.
	// Previously this pre-computed the next future occurrence for past RunAt,
	// which skipped the current day's execution.
	schedule.NextRun = &runAt

	if err := h.scheduleRepo.Update(c.Request().Context(), schedule); err != nil {
		log.Printf("[handler] UpdateSchedule error: %v", err)
		return err
	}
	log.Printf("[handler] UpdateSchedule success id=%s next_run=%v", schedule.ID, schedule.NextRun)

	// Reset task status to pending so the scheduler can pick it up.
	// This allows completed/failed tasks to run again after schedule changes.
	if h.taskSvc != nil && schedule.NextRun != nil {
		task, err := h.taskSvc.GetByID(c.Request().Context(), schedule.TaskID)
		if err == nil && task != nil && task.Status != models.StatusPending && task.Status != models.StatusRunning {
			if err := h.taskSvc.UpdateStatus(c.Request().Context(), task.ID, models.StatusPending); err != nil {
				log.Printf("[handler] UpdateSchedule error resetting task status to pending: %v", err)
			} else {
				log.Printf("[handler] UpdateSchedule reset task=%s status to pending (was %s)", task.ID, task.Status)
			}
		}
	}

	// For HTMX requests, return the updated task detail content
	if isHTMX {
		task, err := h.taskSvc.GetByID(c.Request().Context(), schedule.TaskID)
		if err != nil {
			log.Printf("[handler] UpdateSchedule error fetching task: %v", err)
			return err
		}
		if task == nil {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}

		executions, _ := h.execRepo.ListByTask(c.Request().Context(), schedule.TaskID)
		schedules, _ := h.scheduleRepo.ListByTask(c.Request().Context(), schedule.TaskID)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), schedule.TaskID)
		var adefs []models.Agent
		if h.agentRepo != nil {
			adefs, _ = h.agentRepo.List(c.Request().Context())
		}
		var rc []models.ReviewComment
		if h.reviewCommentRepo != nil {
			rc, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), schedule.TaskID)
		}

		return render(c, http.StatusOK, pages.TaskDetailContent(task, executions, schedules, agents, adefs, attachments, "schedules", rc))
	}

	return c.Redirect(http.StatusSeeOther, "/")
}

func (h *Handler) DeleteSchedule(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] DeleteSchedule id=%s", id)

	if err := h.scheduleRepo.Delete(c.Request().Context(), id); err != nil {
		log.Printf("[handler] DeleteSchedule error: %v", err)
		return err
	}
	log.Printf("[handler] DeleteSchedule success id=%s", id)

	if isHTMX(c) {
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/")
}

// buildModelWorkerStatsList returns per-model worker stats for all model configs
func (h *Handler) buildModelWorkerStatsList(ctx context.Context) []pages.ModelWorkerStats {
	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] buildModelWorkerStatsList error: %v", err)
		return nil
	}
	stats := make([]pages.ModelWorkerStats, 0, len(agents))
	for _, agent := range agents {
		if agent.MaxWorkers > 0 {
			stats = append(stats, pages.ModelWorkerStats{
				ID:         agent.ID,
				Name:       agent.Name,
				Model:      agent.Model,
				Running:    h.workerSvc.ModelRunning(agent.ID),
				MaxWorkers: agent.MaxWorkers,
			})
		}
	}
	return stats
}

func (h *Handler) WorkerSettings(c echo.Context) error {
	isHTMX := isHTMX(c)
	log.Printf("[handler] WorkerSettings requested htmx=%v", isHTMX)
	maxWorkers, _ := h.workerRepo.GetMaxWorkers(c.Request().Context())
	queueSize := h.workerSvc.QueueSize()
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()

	projects, _ := h.projectSvc.List(c.Request().Context())

	// Get pending task counts by project
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		log.Printf("[handler] WorkerSettings error counting pending tasks: %v", err)
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

	log.Printf("[handler] WorkerSettings max_workers=%d running_workers=%d total_running=%d queue_size=%d",
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
	if err != nil || maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWorkers > 10 {
		maxWorkers = 10
	}
	log.Printf("[handler] UpdateWorkerSettings max_workers=%d", maxWorkers)

	if err := h.workerRepo.SetMaxWorkers(c.Request().Context(), maxWorkers); err != nil {
		log.Printf("[handler] UpdateWorkerSettings error: %v", err)
		return err
	}

	// Apply the new worker count to the running worker pool
	h.workerSvc.Resize(maxWorkers)
	runningWorkers := h.workerSvc.NumWorkers()
	totalRunning := h.workerSvc.TotalRunning()
	log.Printf("[handler] UpdateWorkerSettings success, resized to %d workers (actual running: %d)", maxWorkers, runningWorkers)

	// For HTMX requests, return the updated content instead of redirecting
	isHTMX := isHTMX(c)
	if isHTMX {
		queueSize := h.workerSvc.QueueSize()

		projects, _ := h.projectSvc.List(c.Request().Context())
		pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
		if err != nil {
			log.Printf("[handler] UpdateWorkerSettings error counting pending tasks: %v", err)
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
		log.Printf("[handler] ProjectWorkerStats error counting pending tasks: %v", err)
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

func (h *Handler) RescheduleTask(c echo.Context) error {
	scheduleID := c.Param("scheduleId")
	newDateStr := c.FormValue("new_date")
	hourStr := c.FormValue("hour")
	log.Printf("[handler] RescheduleTask schedule=%s new_date=%s hour=%s", scheduleID, newDateStr, hourStr)

	// Parse the new date and hour
	newDate, err := time.Parse("2006-01-02", newDateStr)
	if err != nil {
		log.Printf("[handler] RescheduleTask invalid date: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid date format")
	}

	hour, err := strconv.Atoi(hourStr)
	if err != nil || hour < 0 || hour > 23 {
		log.Printf("[handler] RescheduleTask invalid hour: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid hour")
	}

	// Get the existing schedule
	schedule, err := h.scheduleRepo.GetByID(c.Request().Context(), scheduleID)
	if err != nil {
		log.Printf("[handler] RescheduleTask error getting schedule: %v", err)
		return err
	}
	if schedule == nil {
		log.Printf("[handler] RescheduleTask schedule not found id=%s", scheduleID)
		return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
	}

	// Preserve the minute and second from the original RunAt (in local time for display consistency)
	runAtLocal := schedule.RunAt.Local()
	minute := runAtLocal.Minute()
	second := runAtLocal.Second()

	// Create the new scheduled time in local timezone (hour from form is local),
	// then convert to UTC for consistent storage
	newScheduleTime := time.Date(newDate.Year(), newDate.Month(), newDate.Day(), hour, minute, second, 0, time.Local).UTC()

	// For sub-daily schedules (seconds, minutes, hours), only update NextRun.
	// RunAt defines when the schedule originally started, which controls calendar display.
	// Changing RunAt would shift the display start point and hide earlier entries.
	if schedule.RepeatType.IsSubDaily() {
		schedule.NextRun = &newScheduleTime
	} else {
		// For daily/weekly/monthly, update both RunAt and NextRun
		// RunAt defines the base time-of-day pattern, NextRun is when it actually executes next
		schedule.RunAt = newScheduleTime
		schedule.NextRun = &newScheduleTime
	}

	// CRITICAL: If drag/drop results in a past time, compute the next FUTURE occurrence.
	// This prevents the scheduler from immediately executing the task as a "missed schedule."
	// Users expect drag/drop to reschedule the task, not trigger immediate execution.
	now := time.Now()
	if schedule.NextRun != nil && schedule.NextRun.Before(now) {
		nextRun := schedule.ComputeNextRun(now)
		if nextRun != nil && nextRun.After(now) {
			schedule.NextRun = nextRun
			log.Printf("[handler] RescheduleTask adjusted past time to next occurrence: %v → %v", newScheduleTime, *nextRun)
		}
	}

	if err := h.scheduleRepo.Update(c.Request().Context(), schedule); err != nil {
		log.Printf("[handler] RescheduleTask error updating schedule: %v", err)
		return err
	}

	log.Printf("[handler] RescheduleTask success schedule=%s new_time=%v next_run=%v", scheduleID, newScheduleTime, schedule.NextRun)

	// NOTE: Do NOT reset task status to pending here. Drag-and-drop reschedule
	// should only update the schedule time. The scheduler will handle status
	// management (resetting to pending, submitting to worker) when the scheduled
	// time arrives and next_run becomes due.

	if isHTMX(c) {
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, "/")
}

func (h *Handler) GetExecution(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] GetExecution id=%s", id)

	exec, err := h.execRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] GetExecution error: %v", err)
		return err
	}
	if exec == nil {
		log.Printf("[handler] GetExecution not found id=%s", id)
		return echo.NewHTTPError(http.StatusNotFound, "execution not found")
	}

	task, _ := h.taskSvc.GetByID(c.Request().Context(), exec.TaskID)
	projects, _ := h.projectSvc.List(c.Request().Context())

	log.Printf("[handler] GetExecution id=%s status=%s tokens=%d duration=%dms",
		id, exec.Status, exec.TokensUsed, exec.DurationMs)
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

	log.Printf("[handler] UpdateProjectWorkerLimit project=%s max_workers=%d", projectID, maxWorkers)

	// Get the project
	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] UpdateProjectWorkerLimit error getting project: %v", err)
		return err
	}
	if project == nil {
		log.Printf("[handler] UpdateProjectWorkerLimit project not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	// Update max_workers (0 or nil means no limit)
	if maxWorkers == 0 {
		project.MaxWorkers = nil
	} else {
		project.MaxWorkers = &maxWorkers
	}

	if err := h.projectSvc.Update(c.Request().Context(), project); err != nil {
		log.Printf("[handler] UpdateProjectWorkerLimit error updating project: %v", err)
		return err
	}

	log.Printf("[handler] UpdateProjectWorkerLimit success project=%s max_workers=%v", projectID, project.MaxWorkers)

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
		log.Printf("[handler] UpdateProjectWorkerLimit error counting pending tasks: %v", err)
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
	hasCapacity := totalRunning < maxWorkers
	availableSlots := maxWorkers - totalRunning
	if availableSlots < 0 {
		availableSlots = 0
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
		log.Printf("[handler] GetProjectCapacities error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list projects")
	}

	// Get pending task counts by project
	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		log.Printf("[handler] GetProjectCapacities error counting pending tasks: %v", err)
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
		log.Printf("[handler] GetProjectCapacity error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get project")
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	running := h.workerSvc.ProjectRunning(project.ID)
	hasCapacity := h.workerSvc.HasProjectCapacity(project.ID)

	pendingCounts, err := h.taskRepo.CountPendingByProject(c.Request().Context())
	if err != nil {
		log.Printf("[handler] GetProjectCapacity error counting pending tasks: %v", err)
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
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] GetModelCapacities error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list models")
	}

	capacities := make([]ModelCapacityResponse, 0, len(agents))
	for _, agent := range agents {
		if agent.MaxWorkers > 0 {
			running := h.workerSvc.ModelRunning(agent.ID)
			hasCapacity := h.workerSvc.HasModelCapacity(agent.ID)
			availableSlots := agent.MaxWorkers - running
			if availableSlots < 0 {
				availableSlots = 0
			}

			capacities = append(capacities, ModelCapacityResponse{
				ID:             agent.ID,
				Name:           agent.Name,
				Model:          agent.Model,
				Running:        running,
				MaxWorkers:     agent.MaxWorkers,
				HasCapacity:    hasCapacity,
				AvailableSlots: availableSlots,
			})
		}
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
		log.Printf("[handler] GetModelCapacity error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get model")
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "model not found")
	}

	running := h.workerSvc.ModelRunning(agent.ID)
	hasCapacity := h.workerSvc.HasModelCapacity(agent.ID)
	availableSlots := 0
	if agent.MaxWorkers > 0 {
		availableSlots = agent.MaxWorkers - running
		if availableSlots < 0 {
			availableSlots = 0
		}
	}

	resp := ModelCapacityResponse{
		ID:             agent.ID,
		Name:           agent.Name,
		Model:          agent.Model,
		Running:        running,
		MaxWorkers:     agent.MaxWorkers,
		HasCapacity:    hasCapacity,
		AvailableSlots: availableSlots,
	}

	return c.JSON(http.StatusOK, resp)
}
