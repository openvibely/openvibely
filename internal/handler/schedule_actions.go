package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/applog"
	"net/http"
	"strconv"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type scheduleRef struct{ ScheduleID, ProjectID string }

func ownSchedule(h *Handler, ctx context.Context, a scheduleRef) (string, authz.Permission, error) {
	_, task, err := h.requireScheduleInRequestProject(ctx, a.ScheduleID, a.ProjectID)
	if err != nil {
		return "", "", err
	}
	return task.ProjectID, authz.Edit, nil
}

type scheduleSaveResult struct {
	Schedule *models.Schedule
	Warnings []string
}

func scheduleSaved(result *service.ScheduleActionResult, err error) (scheduleSaveResult, error) {
	if err != nil {
		return scheduleSaveResult{}, scheduleFormHTTPError(err)
	}
	out := scheduleSaveResult{Schedule: result.Schedule}
	for _, warning := range result.Warnings {
		out.Warnings = append(out.Warnings, warning.Error())
	}
	return out, nil
}

var actCreateSchedule = defineAction("schedule.create", func(h *Handler, ctx context.Context, a service.CreateAbsoluteScheduleForTaskRequest) (string, authz.Permission, error) {
	task, err := h.requireTaskInRequestProject(ctx, a.TaskID, a.ProjectID)
	if err != nil {
		return "", "", err
	}
	return task.ProjectID, authz.Edit, nil
}, func(h *Handler, ctx context.Context, a service.CreateAbsoluteScheduleForTaskRequest) (scheduleSaveResult, error) {
	return scheduleSaved(service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc).CreateAbsoluteForTask(ctx, a))
})

var actModifySchedule = defineAction("schedule.modify", func(h *Handler, ctx context.Context, a service.ModifyAbsoluteScheduleRequest) (string, authz.Permission, error) {
	return ownSchedule(h, ctx, scheduleRef{a.ScheduleID, a.ProjectID})
}, func(h *Handler, ctx context.Context, a service.ModifyAbsoluteScheduleRequest) (scheduleSaveResult, error) {
	return scheduleSaved(service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc).ModifyAbsolute(ctx, a))
})

var actDeleteSchedule = defineAction("schedule.delete", ownSchedule, func(h *Handler, ctx context.Context, a scheduleRef) (*models.Task, error) {
	_, task, err := h.requireScheduleInRequestProject(ctx, a.ScheduleID, a.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := h.scheduleRepo.Delete(ctx, a.ScheduleID); err != nil {
		return nil, err
	}
	return task, nil
})

type scheduleToggleWire struct {
	Schedule  *models.Schedule
	Task      *models.Task
	TimeError string
}

var actToggleSchedule = defineAction("schedule.toggle", ownSchedule, func(h *Handler, ctx context.Context, a scheduleRef) (scheduleToggleWire, error) {
	result, err := h.toggleScheduleLocal(ctx, a.ScheduleID, a.ProjectID)
	out := scheduleToggleWire{Schedule: result.schedule, Task: result.task}
	if actionErr, ok := err.(*service.ScheduleActionError); ok && actionErr.Kind == service.ScheduleActionTimeError {
		out.TimeError = actionErr.Error()
		return out, nil
	}
	if err != nil {
		return out, scheduleToggleHTTPError(err)
	}
	if out.Schedule == nil {
		return out, statusErr(http.StatusNotFound, "schedule not found")
	}
	return out, nil
})

func (h *Handler) toggleScheduleEnabled(ctx context.Context, id, projectID string) (scheduleToggleResult, error) {
	out, err := actToggleSchedule(h, ctx, scheduleRef{ScheduleID: id, ProjectID: projectID})
	result := scheduleToggleResult{schedule: out.Schedule, task: out.Task}
	if out.TimeError != "" {
		err = &service.ScheduleActionError{Kind: service.ScheduleActionTimeError, Value: out.TimeError}
	}
	return result, err
}

type rescheduleArgs struct {
	ScheduleID, ProjectID  string
	ScheduleIDs            []string
	Date                   time.Time
	Hour                   int
	SourceDate, SourceHour string
}

var actReschedule = defineAction("schedule.move", func(h *Handler, ctx context.Context, a rescheduleArgs) (string, authz.Permission, error) {
	return ownSchedule(h, ctx, scheduleRef{a.ScheduleID, a.ProjectID})
}, func(h *Handler, ctx context.Context, a rescheduleArgs) (struct{}, error) {
	scheduleID, projectID, ids, newDate, hour := a.ScheduleID, a.ProjectID, a.ScheduleIDs, a.Date, a.Hour
	if hour < 0 || hour > 23 || len(ids) == 0 {
		return struct{}{}, statusErr(http.StatusBadRequest, "invalid schedule move")
	}
	// Preserve the established exact-target behavior for ordinary single-card drags.
	if len(ids) == 1 {
		schedule, task, err := h.requireScheduleInRequestProject(ctx, scheduleID, projectID)
		if err != nil {
			return struct{}{}, err
		}
		runAtLocal := schedule.RunAt.Local()
		newScheduleTime := time.Date(newDate.Year(), newDate.Month(), newDate.Day(), hour, runAtLocal.Minute(), runAtLocal.Second(), 0, time.Local).UTC()
		updateScheduleAfterMove(schedule, func(time.Time) time.Time { return newScheduleTime }, time.Now())
		if err := h.scheduleRepo.UpdateForTask(ctx, schedule, task.ID); err != nil {
			applog.Infof("[handler] RescheduleTask error updating schedule: %v", err)
			return struct{}{}, err
		}
	} else {
		if projectID == "" {
			return struct{}{}, statusErr(http.StatusBadRequest, "project_id is required for grouped rescheduling")
		}
		sourceDate, err := time.Parse("2006-01-02", a.SourceDate)
		if err != nil {
			return struct{}{}, statusErr(http.StatusBadRequest, "invalid source date format")
		}
		sourceHour, err := strconv.Atoi(a.SourceHour)
		if err != nil || sourceHour < 0 || sourceHour > 23 {
			return struct{}{}, statusErr(http.StatusBadRequest, "invalid source hour")
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
			if _, _, err := h.requireScheduleInRequestProject(ctx, id, projectID); err != nil {
				return struct{}{}, err
			}
		}
		now := time.Now()
		if err := h.scheduleRepo.UpdateBatchForProject(ctx, projectID, ids, func(schedule *models.Schedule) error {
			updateScheduleAfterMove(schedule, move, now)
			return nil
		}); err != nil {
			return struct{}{}, err
		}
	}

	return struct{}{}, nil
})
