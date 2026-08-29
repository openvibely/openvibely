package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// ScheduleActionErrorKind identifies failures that chat surfaces format differently.
type ScheduleActionErrorKind string

const (
	ScheduleActionReferenceError ScheduleActionErrorKind = "reference"
	ScheduleActionTimeError      ScheduleActionErrorKind = "time"
	ScheduleActionRepeatError    ScheduleActionErrorKind = "repeat"
	ScheduleActionDaysError      ScheduleActionErrorKind = "days"
	ScheduleActionIntervalError  ScheduleActionErrorKind = "interval"
	ScheduleActionPersistError   ScheduleActionErrorKind = "persist"
)

// ScheduleActionError preserves structured failure details for surface wrappers.
type ScheduleActionError struct {
	Kind  ScheduleActionErrorKind
	Value string
	Err   error
}

func (e *ScheduleActionError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Value
}

// ScheduleActionResult is the shared mutation outcome formatted by each chat surface.
type ScheduleActionResult struct {
	Task     *models.Task
	Schedule *models.Schedule
	Changes  []string
	Warnings []error
}

// ScheduleActionService owns schedule mutation semantics shared by runtime
// tools and browser forms. Transports parse and render their own shapes; this
// service persists schedules and applies linked task lifecycle side effects.
type ScheduleActionService struct {
	taskRepo     *repository.TaskRepo
	scheduleRepo *repository.ScheduleRepo
	workerSvc    *WorkerService
}

// CreateScheduleForTaskRequest is the normalized low-level schedule creation
// contract used after each surface has parsed its own input format. It persists
// only the schedule row; higher-level create methods apply linked task effects.
type CreateScheduleForTaskRequest struct {
	TaskID              string
	RunAt               time.Time
	RepeatType          models.RepeatType
	RepeatInterval      int
	ClearContextOnStart *bool
}

// CreateAbsoluteScheduleForTaskRequest is the browser-form creation contract:
// the form has already parsed an absolute local datetime into RunAt.
type CreateAbsoluteScheduleForTaskRequest struct {
	ProjectID              string
	TaskID                 string
	RunAt                  time.Time
	RepeatType             models.RepeatType
	RepeatInterval         int
	ClearContextOnStart    *bool
	AgentDefinitionPresent bool
	AgentDefinitionID      *string
}

// ModifyAbsoluteScheduleRequest is the browser-form update contract: the form
// replaces the schedule timing with an absolute RunAt value rather than the
// runtime-tool HH:MM grammar.
type ModifyAbsoluteScheduleRequest struct {
	ProjectID              string
	ScheduleID             string
	RunAt                  time.Time
	RepeatType             models.RepeatType
	RepeatInterval         int
	ClearContextOnStart    *bool
	AgentDefinitionPresent bool
	AgentDefinitionID      *string
}

type scheduleTaskLifecycleOptions struct {
	PromoteToScheduled bool
	ResetTerminal      bool
}

func NewScheduleActionService(taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, workerSvc ...*WorkerService) *ScheduleActionService {
	svc := &ScheduleActionService{taskRepo: taskRepo, scheduleRepo: scheduleRepo}
	if len(workerSvc) > 0 {
		svc.workerSvc = workerSvc[0]
	}
	return svc
}

func (s *ScheduleActionService) CreateForTask(ctx context.Context, req CreateScheduleForTaskRequest) (*models.Schedule, error) {
	if s.scheduleRepo == nil {
		return nil, fmt.Errorf("schedule repository not available")
	}
	interval := req.RepeatInterval
	if interval == 0 {
		interval = 1
	}
	if err := models.ValidateScheduleRepeatInterval(interval); err != nil {
		return nil, err
	}
	clearContext := true
	if req.ClearContextOnStart != nil {
		clearContext = *req.ClearContextOnStart
	}
	schedule := &models.Schedule{
		TaskID:              req.TaskID,
		RunAt:               req.RunAt,
		RepeatType:          req.RepeatType,
		RepeatInterval:      interval,
		Enabled:             true,
		ClearContextOnStart: clearContext,
	}
	// ScheduleRepo.Create owns the lower-level initial NextRun rule: absent
	// NextRun starts at RunAt so past recurring schedules are due immediately.
	if err := s.scheduleRepo.Create(ctx, schedule); err != nil {
		return nil, err
	}
	return schedule, nil
}

func (s *ScheduleActionService) CreateAbsoluteForTask(ctx context.Context, req CreateAbsoluteScheduleForTaskRequest) (*ScheduleActionResult, error) {
	task, err := s.resolveTask(ctx, req.ProjectID, req.TaskID, "")
	if err != nil {
		return nil, actionError(ScheduleActionReferenceError, "", err)
	}
	result := &ScheduleActionResult{Task: task}
	schedule, err := s.CreateForTask(ctx, CreateScheduleForTaskRequest{
		TaskID:              task.ID,
		RunAt:               req.RunAt,
		RepeatType:          req.RepeatType,
		RepeatInterval:      req.RepeatInterval,
		ClearContextOnStart: req.ClearContextOnStart,
	})
	if err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	result.Schedule = schedule
	if err := s.applyPrimaryAgentAssignment(ctx, task, req.AgentDefinitionPresent, req.AgentDefinitionID); err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	s.applyScheduleTaskLifecycle(ctx, result, scheduleTaskLifecycleOptions{
		ResetTerminal: schedule.RepeatType == models.RepeatOnce && schedule.NextRun != nil,
	})
	return result, nil
}

func (s *ScheduleActionService) ModifyAbsolute(ctx context.Context, req ModifyAbsoluteScheduleRequest) (*ScheduleActionResult, error) {
	schedule, task, err := s.resolveSchedule(ctx, req.ProjectID, req.ScheduleID, "", "")
	if err != nil {
		return nil, actionError(ScheduleActionReferenceError, "", err)
	}
	result := &ScheduleActionResult{Task: task, Schedule: schedule}
	if err := models.ValidateScheduleRepeatInterval(req.RepeatInterval); err != nil {
		return result, actionError(ScheduleActionIntervalError, fmt.Sprintf("%d", req.RepeatInterval), err)
	}
	schedule.RunAt = req.RunAt
	schedule.RepeatType = req.RepeatType
	schedule.RepeatInterval = req.RepeatInterval
	if req.ClearContextOnStart != nil {
		schedule.ClearContextOnStart = *req.ClearContextOnStart
	}
	schedule.NextRun = &req.RunAt
	result.Changes = []string{"absolute_form"}
	if err := s.scheduleRepo.Update(ctx, schedule); err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	if err := s.applyPrimaryAgentAssignment(ctx, task, req.AgentDefinitionPresent, req.AgentDefinitionID); err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	s.applyScheduleTaskLifecycle(ctx, result, scheduleTaskLifecycleOptions{
		ResetTerminal: schedule.NextRun != nil,
	})
	return result, nil
}

func (s *ScheduleActionService) Create(ctx context.Context, projectID string, req ScheduleTaskRequest) (*ScheduleActionResult, error) {
	task, err := s.resolveTask(ctx, projectID, req.TaskID, req.Title)
	if err != nil {
		return nil, actionError(ScheduleActionReferenceError, "", err)
	}
	result := &ScheduleActionResult{Task: task}
	hour, minute, err := parseScheduleActionTime(req.Time)
	if err != nil {
		return result, actionError(ScheduleActionTimeError, req.Time, err)
	}
	repeatType, known := scheduleActionRepeatType(req.Repeat, true)
	if !known {
		return result, actionError(ScheduleActionRepeatError, req.Repeat, fmt.Errorf("unknown repeat type %q", req.Repeat))
	}
	if repeatType == models.RepeatWeekly && len(req.Days) > 0 {
		if err := validateScheduleActionWeekdays(req.Days); err != nil {
			return result, actionError(ScheduleActionDaysError, strings.Join(req.Days, ","), err)
		}
	}
	interval := 1
	if req.Interval > 0 {
		interval = req.Interval
	}
	if err := models.ValidateScheduleRepeatInterval(interval); err != nil {
		return result, actionError(ScheduleActionIntervalError, fmt.Sprintf("%d", interval), err)
	}
	now := time.Now().Local()
	runAt := scheduleActionRunAt(now, hour, minute, repeatType, req.Days)
	schedule, err := s.CreateForTask(ctx, CreateScheduleForTaskRequest{
		TaskID:              task.ID,
		RunAt:               runAt.UTC(),
		RepeatType:          repeatType,
		RepeatInterval:      interval,
		ClearContextOnStart: req.ClearContextOnStart,
	})
	if err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	result.Schedule = schedule
	categoryChanged := s.applyScheduleTaskLifecycle(ctx, result, scheduleTaskLifecycleOptions{PromoteToScheduled: true})
	if repeatType == models.RepeatOnce && !categoryChanged {
		s.applyScheduleTaskLifecycle(ctx, result, scheduleTaskLifecycleOptions{ResetTerminal: true})
	}
	return result, nil
}

func (s *ScheduleActionService) Modify(ctx context.Context, projectID string, req ModifyScheduleRequest) (*ScheduleActionResult, error) {
	schedule, task, err := s.resolveSchedule(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
	if err != nil {
		return nil, actionError(ScheduleActionReferenceError, "", err)
	}
	result := &ScheduleActionResult{Task: task, Schedule: schedule}
	changes := make([]string, 0, 6)
	timeChanged := false
	var hour, minute int
	if req.Time != "" {
		hour, minute, err = parseScheduleActionTime(req.Time)
		if err != nil {
			return result, actionError(ScheduleActionTimeError, req.Time, err)
		}
	}
	repeatType := schedule.RepeatType
	if req.Repeat != "" {
		var known bool
		repeatType, known = scheduleActionRepeatType(req.Repeat, false)
		if !known {
			return result, actionError(ScheduleActionRepeatError, req.Repeat, fmt.Errorf("unknown repeat type %q", req.Repeat))
		}
	}
	if req.Interval != nil {
		if err := models.ValidateScheduleRepeatInterval(*req.Interval); err != nil {
			return result, actionError(ScheduleActionIntervalError, fmt.Sprintf("%d", *req.Interval), err)
		}
	}
	if len(req.Days) > 0 && repeatType == models.RepeatWeekly {
		if err := validateScheduleActionWeekdays(req.Days); err != nil {
			return result, actionError(ScheduleActionDaysError, strings.Join(req.Days, ","), err)
		}
	}

	if req.Time != "" {
		oldLocal := schedule.RunAt.Local()
		schedule.RunAt = time.Date(oldLocal.Year(), oldLocal.Month(), oldLocal.Day(), hour, minute, 0, 0, time.Local).UTC()
		changes = append(changes, fmt.Sprintf("time→%s", req.Time))
		timeChanged = true
	}
	if req.Repeat != "" {
		schedule.RepeatType = repeatType
		changes = append(changes, fmt.Sprintf("repeat→%s", req.Repeat))
		timeChanged = true
	}
	if req.Interval != nil {
		schedule.RepeatInterval = *req.Interval
		changes = append(changes, fmt.Sprintf("interval→%d", *req.Interval))
		timeChanged = true
	}
	if len(req.Days) > 0 && schedule.RepeatType == models.RepeatWeekly {
		now := time.Now().Local()
		runAt := schedule.RunAt.Local()
		base := time.Date(now.Year(), now.Month(), now.Day(), runAt.Hour(), runAt.Minute(), 0, 0, time.Local)
		schedule.RunAt = nextScheduleActionWeekday(base, now, req.Days).UTC()
		changes = append(changes, fmt.Sprintf("days→%s", strings.Join(req.Days, ",")))
		timeChanged = true
	}
	if req.Enabled != nil {
		schedule.Enabled = *req.Enabled
		changes = append(changes, fmt.Sprintf("enabled→%t", *req.Enabled))
	}
	if req.ClearContextOnStart != nil {
		schedule.ClearContextOnStart = *req.ClearContextOnStart
		changes = append(changes, fmt.Sprintf("clear_context_on_start→%t", *req.ClearContextOnStart))
	}
	result.Changes = changes
	if len(changes) == 0 {
		return result, nil
	}
	if timeChanged {
		schedule.NextRun = schedule.ComputeNextRun(time.Now())
	} else if req.Enabled != nil && *req.Enabled {
		now := time.Now()
		if schedule.NextRun == nil || schedule.NextRun.Before(now) {
			schedule.NextRun = schedule.ComputeNextRun(now)
		}
	}
	if err := s.scheduleRepo.Update(ctx, schedule); err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	if schedule.NextRun != nil && (timeChanged || (req.Enabled != nil && *req.Enabled)) {
		s.applyScheduleTaskLifecycle(ctx, result, scheduleTaskLifecycleOptions{ResetTerminal: true})
	}
	return result, nil
}

func (s *ScheduleActionService) Delete(ctx context.Context, projectID string, req DeleteScheduleRequest) (*ScheduleActionResult, error) {
	schedule, task, err := s.resolveSchedule(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
	if err != nil {
		return nil, actionError(ScheduleActionReferenceError, "", err)
	}
	result := &ScheduleActionResult{Task: task, Schedule: schedule}
	if err := s.scheduleRepo.Delete(ctx, schedule.ID); err != nil {
		return result, actionError(ScheduleActionPersistError, "", err)
	}
	remaining, err := s.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		result.Warnings = append(result.Warnings, err)
		return result, nil
	}
	if len(remaining) == 0 && task.Category == models.CategoryScheduled {
		if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
			result.Warnings = append(result.Warnings, err)
		}
	}
	return result, nil
}

func (s *ScheduleActionService) applyPrimaryAgentAssignment(ctx context.Context, task *models.Task, present bool, agentDefinitionID *string) error {
	if !present {
		return nil
	}
	if s.taskRepo == nil {
		return fmt.Errorf("task repository not configured")
	}
	if err := s.taskRepo.UpdateAgentDefinition(ctx, task.ID, agentDefinitionID); err != nil {
		return err
	}
	task.AgentDefinitionID = agentDefinitionID
	return nil
}

func (s *ScheduleActionService) applyScheduleTaskLifecycle(ctx context.Context, result *ScheduleActionResult, opts scheduleTaskLifecycleOptions) bool {
	if result == nil || result.Task == nil || s.taskRepo == nil {
		return false
	}
	task := result.Task
	categoryChanged := false
	if opts.PromoteToScheduled && task.Category != models.CategoryScheduled {
		if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled); err != nil {
			result.Warnings = append(result.Warnings, err)
		} else {
			task.Category = models.CategoryScheduled
			categoryChanged = true
		}
	}
	shouldResetStatus := task.Status != models.StatusPending && task.Status != models.StatusRunning && (opts.ResetTerminal || categoryChanged)
	if shouldResetStatus {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			result.Warnings = append(result.Warnings, err)
		} else {
			task.Status = models.StatusPending
		}
	}
	if task.Category == models.CategoryScheduled && task.Status == models.StatusPending && s.workerSvc != nil {
		s.workerSvc.ClearCancellationRequested(task.ID)
	}
	return categoryChanged
}

func (s *ScheduleActionService) resolveTask(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
	return ResolveTaskReference(ctx, s.taskRepo, projectID, taskID, title, TaskReferenceResolutionOptions{
		AllowUnscopedProject: true,
	})
}

func (s *ScheduleActionService) resolveSchedule(ctx context.Context, projectID, scheduleID, taskID, title string) (*models.Schedule, *models.Task, error) {
	if s.scheduleRepo == nil {
		return nil, nil, fmt.Errorf("schedule repository not available")
	}
	if scheduleID = strings.TrimSpace(scheduleID); scheduleID != "" {
		schedule, err := s.scheduleRepo.GetByID(ctx, scheduleID)
		if err != nil {
			return nil, nil, fmt.Errorf("error looking up schedule %s: %w", scheduleID, err)
		}
		if schedule == nil {
			return nil, nil, fmt.Errorf("schedule %s not found", scheduleID)
		}
		if s.taskRepo == nil {
			return nil, nil, fmt.Errorf("task repository not configured")
		}
		task, err := s.taskRepo.GetByID(ctx, schedule.TaskID)
		if err != nil || task == nil {
			return nil, nil, fmt.Errorf("task for schedule %s not found", scheduleID)
		}
		if projectID != "" && task.ProjectID != projectID {
			return nil, nil, fmt.Errorf("schedule %s belongs to a different project", scheduleID)
		}
		return schedule, task, nil
	}
	task, err := s.resolveTask(ctx, projectID, taskID, title)
	if err != nil {
		return nil, nil, err
	}
	schedules, err := s.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("error listing schedules for task %s: %w", task.ID, err)
	}
	if len(schedules) == 0 {
		return nil, nil, fmt.Errorf("no schedules found for task %q", task.Title)
	}
	return &schedules[0], task, nil
}

func parseScheduleActionTime(raw string) (int, int, error) {
	if len(raw) != 5 || raw[2] != ':' || raw[0] < '0' || raw[0] > '9' || raw[1] < '0' || raw[1] > '9' || raw[3] < '0' || raw[3] > '9' || raw[4] < '0' || raw[4] > '9' {
		return 0, 0, fmt.Errorf("invalid time %q: expected HH:MM (00:00-23:59)", raw)
	}
	hour := int(raw[0]-'0')*10 + int(raw[1]-'0')
	minute := int(raw[3]-'0')*10 + int(raw[4]-'0')
	if hour > 23 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid time %q: expected HH:MM (00:00-23:59)", raw)
	}
	return hour, minute, nil
}

func scheduleActionRepeatType(raw string, defaultDaily bool) (models.RepeatType, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "once":
		return models.RepeatOnce, true
	case "daily":
		return models.RepeatDaily, true
	case "weekly":
		return models.RepeatWeekly, true
	case "monthly":
		return models.RepeatMonthly, true
	case "hours", "hourly":
		return models.RepeatHours, true
	case "minutes":
		return models.RepeatMinutes, true
	case "seconds":
		return models.RepeatSeconds, true
	case "":
		return models.RepeatDaily, defaultDaily
	default:
		return models.RepeatDaily, false
	}
}

func validateScheduleActionWeekdays(days []string) error {
	if len(days) > 1 {
		return fmt.Errorf("weekly schedules support only one weekly day; create separate weekly schedules for multiple days")
	}
	for _, day := range days {
		if _, ok := scheduleActionWeekday(day); !ok {
			return fmt.Errorf("unknown weekly day %q: expected sun, mon, tue, wed, thu, fri, or sat", day)
		}
	}
	return nil
}

func scheduleActionWeekday(day string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(day)) {
	case "sun":
		return time.Sunday, true
	case "mon":
		return time.Monday, true
	case "tue":
		return time.Tuesday, true
	case "wed":
		return time.Wednesday, true
	case "thu":
		return time.Thursday, true
	case "fri":
		return time.Friday, true
	case "sat":
		return time.Saturday, true
	default:
		return 0, false
	}
}

func scheduleActionRunAt(now time.Time, hour, minute int, repeatType models.RepeatType, days []string) time.Time {
	runAt := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.Local)
	if repeatType == models.RepeatWeekly && len(days) > 0 {
		if next := nextScheduleActionWeekday(runAt, now, days); !next.IsZero() {
			return next
		}
	}
	return runAt
}

func nextScheduleActionWeekday(base, now time.Time, days []string) time.Time {
	bestOffset := 8
	for _, day := range days {
		target, ok := scheduleActionWeekday(day)
		if !ok {
			continue
		}
		offset := int(target - base.Weekday())
		if offset < 0 {
			offset += 7
		}
		if offset == 0 && base.Before(now) {
			offset = 7
		}
		if offset < bestOffset {
			bestOffset = offset
		}
	}
	if bestOffset == 8 {
		return time.Time{}
	}
	return base.AddDate(0, 0, bestOffset)
}

func actionError(kind ScheduleActionErrorKind, value string, err error) error {
	return &ScheduleActionError{Kind: kind, Value: value, Err: err}
}
