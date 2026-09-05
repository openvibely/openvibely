package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	llmworkflow "github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	backlogSortCookieName             = "backlog_sort"
	completedSortCookieName           = "completed_sort"
	defaultBacklogSort                = "created_desc"
	defaultCompletedSort              = "completed_desc"
	taskThreadWindowLimitDefault      = 5
	taskThreadWindowLimitMax          = 100
	taskExecutionHistoryWindowDefault = 18
	taskExecutionHistoryWindowMax     = 100
)

type taskSortPreferences struct {
	Backlog   string
	Completed string
}

type taskDetailContentData struct {
	task             *models.Task
	taskGoal         *models.TaskGoal
	executionMetrics models.TaskExecutionMetrics
	schedules        []models.Schedule
	agents           []models.LLMConfig
	agentDefs        []models.Agent
	attachments      []models.Attachment
	reviewComments   []models.ReviewComment
}

func (h *Handler) loadTaskReviewComments(ctx context.Context, taskID string) []models.ReviewComment {
	if h.reviewCommentRepo == nil {
		return nil
	}
	comments, _ := h.reviewCommentRepo.ListByTask(ctx, taskID)
	return comments
}

func getSortPreference(c echo.Context, cookieName string) string {
	if cookie, err := c.Cookie(cookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func getSortPreferences(c echo.Context) taskSortPreferences {
	preferences := taskSortPreferences{
		Backlog:   getSortPreference(c, backlogSortCookieName),
		Completed: getCompletedSortPreference(c),
	}
	if preferences.Backlog == "" {
		preferences.Backlog = defaultBacklogSort
	}
	if preferences.Completed == "" {
		preferences.Completed = defaultCompletedSort
	}
	return preferences
}

// getCompletedSortPreference reads the completed-sort cookie and migrates
// stale values written by older code that used created_asc/created_desc
// for the completed column.
func getCompletedSortPreference(c echo.Context) string {
	cookie, err := c.Cookie(completedSortCookieName)
	if err != nil {
		return ""
	}
	switch cookie.Value {
	case "created_asc":
		return "completed_asc"
	case "created_desc":
		return "completed_desc"
	}
	return cookie.Value
}

func isValidBacklogSort(sortBy string) bool {
	switch sortBy {
	case "title_asc", "title_desc", "created_asc", "created_desc", "priority_asc", "priority_desc":
		return true
	default:
		return false
	}
}

func formBoolEnabled(c echo.Context, name string, defaultValue bool) bool {
	values := c.Request().PostForm[name]
	if len(values) == 0 {
		return defaultValue
	}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "on", "yes":
			return true
		}
	}
	return false
}

func swarmMergerEnabledFormValue(c echo.Context) bool {
	if _, ok := c.Request().PostForm["swarm_merger_enabled"]; ok {
		return formBoolEnabled(c, "swarm_merger_enabled", true)
	}
	return formBoolEnabled(c, "swarm_integrator_enabled", true)
}

func isValidCompletedSort(sortBy string) bool {
	switch sortBy {
	case "title_asc", "title_desc", "completed_asc", "completed_desc", "priority_asc", "priority_desc":
		return true
	default:
		return false
	}
}

func setTaskSortCookie(c echo.Context, cookieName string, sortBy string) {
	c.SetCookie(&http.Cookie{
		Name:     cookieName,
		Value:    sortBy,
		Path:     "/",
		MaxAge:   31536000, // 1 year
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) listAgentDefinitions(ctx context.Context) []models.Agent {
	if h.agentRepo == nil {
		return nil
	}
	agentDefs, err := h.agentRepo.List(ctx)
	if err != nil {
		applog.Infof("[handler] listAgentDefinitions error: %v", err)
		return nil
	}
	return agentDefs
}

func (h *Handler) loadTaskGoal(ctx context.Context, taskID string) *models.TaskGoal {
	if h.taskGoalSvc == nil || taskID == "" {
		return nil
	}
	goal, err := h.taskGoalSvc.GetGoal(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] loadTaskGoal task=%s error: %v", taskID, err)
		return nil
	}
	return goal
}

func (h *Handler) listTaskFormAgentDefinitions(ctx context.Context, projectID string, currentAgentID *string) []models.Agent {
	agentDefs := h.listAgentDefinitions(ctx)
	out := selectableTaskAgentDefinitionsForProject(agentDefs, projectID)
	if currentAgentID == nil || *currentAgentID == "" {
		return out
	}
	for _, agent := range out {
		if agent.ID == *currentAgentID {
			return out
		}
	}
	for _, agent := range agentDefs {
		if agent.ID == *currentAgentID && agentDefinitionAvailableToProject(agent, projectID) && agent.GeneratedStatus != models.AgentStatusArchived && agent.ArchivedAt == nil {
			return append([]models.Agent{agent}, out...)
		}
	}
	return out
}

func (h *Handler) listScheduleAgentOptions(ctx context.Context, projectID string) []repository.AgentScheduleOption {
	if h.agentRepo == nil {
		return nil
	}
	options, err := h.agentRepo.ListScheduleOptions(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] listScheduleAgentOptions error: %v", err)
		return nil
	}
	return options
}

func (h *Handler) taskDetailAgentLabel(ctx context.Context, projectID string, agentDefinitionID *string) string {
	if h.agentRepo == nil || agentDefinitionID == nil || *agentDefinitionID == "" {
		return ""
	}
	option, err := h.agentRepo.GetTaskDetailAgentLabel(ctx, projectID, *agentDefinitionID)
	if err != nil {
		applog.Infof("[handler] taskDetailAgentLabel error: %v", err)
		return ""
	}
	if option == nil {
		return ""
	}
	return option.Name
}

func agentDefinitionAvailableToProject(agent models.Agent, projectID string) bool {
	if agent.Scope == models.AgentScopeProject {
		return agent.ProjectID != "" && agent.ProjectID == projectID
	}
	return true
}

func selectablePrimaryAgentDefinition(agent models.Agent) bool {
	return agent.Enabled && agent.SelectableAsPrimary && agent.GeneratedStatus != models.AgentStatusArchived && agent.ArchivedAt == nil
}

func selectableTaskAgentDefinitionsForProject(agentDefs []models.Agent, projectID string) []models.Agent {
	out := make([]models.Agent, 0, len(agentDefs))
	for _, agent := range agentDefs {
		if !selectablePrimaryAgentDefinition(agent) || !agentDefinitionAvailableToProject(agent, projectID) {
			continue
		}
		out = append(out, agent)
	}
	return out
}

func selectableTaskAgentDefinitions(agentDefs []models.Agent) []models.Agent {
	out := make([]models.Agent, 0, len(agentDefs))
	for _, agent := range agentDefs {
		if selectablePrimaryAgentDefinition(agent) {
			out = append(out, agent)
		}
	}
	return out
}

func (h *Handler) resolvePrimaryAgentDefinition(ctx context.Context, projectID, agentDefinitionID string) (*string, error) {
	agentDefinitionID = strings.TrimSpace(agentDefinitionID)
	if agentDefinitionID == "" {
		return nil, nil
	}
	if h.agentRepo == nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "primary agent is unavailable")
	}
	agent, err := h.agentRepo.GetByID(ctx, agentDefinitionID)
	if err != nil {
		return nil, err
	}
	if agent == nil || !selectablePrimaryAgentDefinition(*agent) || !agentDefinitionAvailableToProject(*agent, projectID) {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "invalid primary agent")
	}
	return &agent.ID, nil
}

func (h *Handler) renderKanbanBoard(c echo.Context, tasks []models.Task, projectID string, sortPrefs taskSortPreferences, llmModels []models.LLMConfig) error {
	tasks = service.AttachSwarmChildren(tasks)
	agentDefs := h.listAgentDefinitions(c.Request().Context())
	return render(c, http.StatusOK, components.KanbanBoard(tasks, projectID, sortPrefs.Backlog, sortPrefs.Completed, llmModels, agentDefs))
}

func (h *Handler) renderTaskBoardRefresh(c echo.Context, projectID string, adjustSort func(*taskSortPreferences)) error {
	sortPrefs := getSortPreferences(c)
	if adjustSort != nil {
		adjustSort(&sortPrefs)
	}

	tasks, err := h.taskSvc.ListBoardByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
	if err != nil {
		applog.Infof("[handler] renderTaskBoardRefresh error listing tasks project=%s: %v", projectID, err)
		return err
	}
	agents, err := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] renderTaskBoardRefresh error listing LLM configs project=%s: %v", projectID, err)
		return err
	}
	return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
}

func (h *Handler) ListTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	isHTMX := isHTMX(c)
	htmxTarget := c.Request().Header.Get("HX-Target")
	applog.Infof("[handler] ListTasks project=%s htmx=%v target=%s", projectID, isHTMX, htmxTarget)

	// Read sort preferences from cookies
	sortPrefs := getSortPreferences(c)
	if sortPrefs.Backlog != "" || sortPrefs.Completed != "" {
		applog.Infof("[handler] ListTasks using sort preferences: backlog=%s completed=%s", sortPrefs.Backlog, sortPrefs.Completed)
	}

	// For kanban-board-only refreshes (SSE, etc.), project_id must be provided
	if isHTMX && htmxTarget != "main-content" {
		if projectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
		}
		tasks, err := h.taskSvc.ListBoardByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			applog.Infof("[handler] ListTasks error: %v", err)
			return err
		}
		tasks = service.AttachSwarmChildren(tasks)
		applog.Infof("[handler] ListTasks found %d tasks", len(tasks))
		agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	// For full page and main-content swaps, default to first project.
	// The Tasks shell renders only the sidebar selector and the current project's
	// id, so the compact selector projection is sufficient; the full current
	// project is loaded via GetByID below.
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	tasks, err := h.taskSvc.ListBoardByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
	if err != nil {
		applog.Infof("[handler] ListTasks error: %v", err)
		return err
	}
	tasks = service.AttachSwarmChildren(tasks)
	applog.Infof("[handler] ListTasks found %d tasks", len(tasks))

	project, _ := h.projectSvc.GetByID(c.Request().Context(), projectID)
	agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
	agentDefs := h.listTaskFormAgentDefinitions(c.Request().Context(), projectID, nil)

	if isHTMX {
		return render(c, http.StatusOK, pages.TasksContent(project, tasks, agents, agentDefs, sortPrefs.Backlog, sortPrefs.Completed))
	}

	return render(c, http.StatusOK, pages.Tasks(projects, project, tasks, agents, agentDefs, sortPrefs.Backlog, sortPrefs.Completed))
}

func isSwarmTaskForm(c echo.Context) bool {
	v := c.FormValue("swarm_mode")
	return v == "on" || v == "true" || v == "1"
}

func taskRequiredFieldHTTPError(err error) *echo.HTTPError {
	switch {
	case errors.Is(err, service.ErrTaskTitleRequired):
		return echo.NewHTTPError(http.StatusBadRequest, "Task title is required")
	case errors.Is(err, service.ErrTaskPromptRequired):
		return echo.NewHTTPError(http.StatusBadRequest, "Task prompt is required")
	case errors.Is(err, service.ErrInvalidTaskPriority):
		return echo.NewHTTPError(http.StatusBadRequest, "Task priority must be between 1 and 4")
	default:
		return nil
	}
}

func (h *Handler) CreateTask(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if _, err := parseBoundedMultipartForm(c, maxUploadSize, maxTaskAttachmentFilesPerRequest); err != nil {
		applog.Infof("[handler] CreateTask error parsing form: %v", err)
		if httpErr, ok := err.(*echo.HTTPError); ok {
			return httpErr
		}
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	category := models.TaskCategory(c.FormValue("category"))
	if category == "" {
		category = models.CategoryActive
	}
	var scheduledFormValues scheduleFormValues
	if category == models.CategoryScheduled {
		var err error
		scheduledFormValues, err = parseScheduleForm(c, models.RepeatDaily)
		if err != nil {
			return scheduleFormHTTPError(err)
		}
	}

	// Creating an active task immediately submits it to the worker pool.
	// Block this when no models are configured so tasks do not get stuck queued.
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			applog.Infof("[handler] CreateTask model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			applog.Infof("[handler] CreateTask blocked: no models configured project=%s title=%q", projectID, c.FormValue("title"))
			return noModelsConfiguredResponse(c)
		}
	}

	t := &models.Task{
		ProjectID:         projectID,
		Title:             c.FormValue("title"),
		Category:          category,
		Priority:          priority,
		Prompt:            c.FormValue("prompt"),
		Tag:               models.TaskTag(c.FormValue("tag")),
		AutoMerge:         c.FormValue("auto_merge") == "on" || c.FormValue("auto_merge") == "true",
		MergeTargetBranch: c.FormValue("merge_target_branch"),
	}

	// Handle optional agent (LLM config) selection
	if agentID := c.FormValue("agent_id"); agentID != "" {
		t.AgentID = &agentID
	}
	// Handle optional primary Agent definition selection separately from the model config.
	if agentDefID := c.FormValue("agent_definition_id"); agentDefID != "" {
		resolvedAgentDefID, err := h.resolvePrimaryAgentDefinition(c.Request().Context(), projectID, agentDefID)
		if err != nil {
			return err
		}
		t.AgentDefinitionID = resolvedAgentDefID
	}
	applog.Infof("[handler] CreateTask project=%s title=%q category=%s priority=%d tag=%s prompt_len=%d",
		projectID, t.Title, t.Category, t.Priority, t.Tag, len(t.Prompt))

	if isSwarmTaskForm(c) {
		if h.swarmSvc == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
		}
		maxWorkers, _ := strconv.Atoi(c.FormValue("swarm_max_workers"))
		parent, err := h.swarmSvc.CreateSwarmTask(c.Request().Context(), service.CreateSwarmTaskRequest{ProjectID: projectID, Title: t.Title, Prompt: t.Prompt, Goal: c.FormValue("goal"), Category: category, Priority: priority, AgentID: t.AgentID, AgentDefinitionID: t.AgentDefinitionID, Tag: t.Tag, MaxWorkers: maxWorkers, WorkerIsolation: c.FormValue("swarm_worker_isolation"), ReviewerEnabled: formBoolEnabled(c, "swarm_reviewer_enabled", true), MergerEnabled: swarmMergerEnabledFormValue(c), MergeTargetBranch: t.MergeTargetBranch})
		if err != nil {
			if errors.Is(err, service.ErrDuplicateTask) {
				return echo.NewHTTPError(http.StatusConflict, "A task with this name already exists in this project")
			}
			if httpErr := taskRequiredFieldHTTPError(err); httpErr != nil {
				return httpErr
			}
			return err
		}
		*t = *parent
	} else if err := h.taskSvc.CreateWithGoal(c.Request().Context(), t, c.FormValue("goal")); err != nil {
		if errors.Is(err, service.ErrDuplicateTask) {
			applog.Infof("[handler] CreateTask duplicate title=%q", t.Title)
			return echo.NewHTTPError(http.StatusConflict, "A task with this name already exists in this project")
		}
		if httpErr := taskRequiredFieldHTTPError(err); httpErr != nil {
			return httpErr
		}
		applog.Infof("[handler] CreateTask error: %v", err)
		return err
	}
	applog.Infof("[handler] CreateTask success id=%s", t.ID)

	// If category is scheduled, create its schedule before reporting success.
	if t.Category == models.CategoryScheduled {
		clearContextOnStart := formBoolEnabled(c, "clear_context_on_start", true)
		sched, err := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo).CreateForTask(c.Request().Context(), service.CreateScheduleForTaskRequest{
			TaskID:              t.ID,
			RunAt:               scheduledFormValues.runAt,
			RepeatType:          scheduledFormValues.repeatType,
			RepeatInterval:      scheduledFormValues.repeatInterval,
			ClearContextOnStart: &clearContextOnStart,
		})
		if err != nil {
			applog.Infof("[handler] CreateTask schedule create error: %v", err)
			rollbackCtx := context.WithoutCancel(c.Request().Context())
			if rollbackErr := h.taskRepo.Delete(rollbackCtx, t.ID); rollbackErr != nil {
				return fmt.Errorf("creating schedule: %w; rolling back task: %v", err, rollbackErr)
			}
			return err
		}
		applog.Infof("[handler] CreateTask schedule created id=%s next_run=%v", sched.ID, sched.NextRun)
	}

	// Handle optional file attachments (multiple files supported)
	form, err := c.MultipartForm()
	if err == nil && form != nil {
		if files := form.File["files"]; len(files) > 0 {
			h.persistTaskAttachmentFiles(c.Request().Context(), t.ID, files, "CreateTask")
		}
	}

	// If created from the schedule page, return the updated schedule content for HTMX
	// or redirect native form submissions back to the project-scoped schedule page.
	if c.QueryParam("from") == "schedule" {
		if !isHTMX(c) {
			return c.Redirect(http.StatusSeeOther, "/schedule?project_id="+projectID)
		}
		project, _ := h.projectSvc.GetByID(c.Request().Context(), projectID)
		scheduledTasks, _ := h.taskSvc.GetTasksWithSchedulesByProject(c.Request().Context(), projectID)
		agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
		agentDefs := h.listScheduleAgentOptions(c.Request().Context(), projectID)
		weekOffset := 0
		if weekParam := c.QueryParam("week"); weekParam != "" {
			if w, err := strconv.Atoi(weekParam); err == nil {
				weekOffset = w
			}
		}
		return render(c, http.StatusOK, pages.ScheduleContent(project, scheduledTasks, weekOffset, agents, agentDefs))
	}

	// Return the full kanban board
	return h.renderTaskBoardRefresh(c, projectID, nil)
}

func (h *Handler) loadTaskDetailContentData(ctx context.Context, taskID string) (*taskDetailContentData, error) {
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if children, childErr := h.taskRepo.ListSwarmChildren(ctx, task.ID); childErr == nil {
			task.SwarmChildren = children
		}
	}

	executionMetrics, _ := h.execRepo.GetTaskExecutionMetrics(ctx, taskID)
	schedules, _ := h.scheduleRepo.ListByTask(ctx, taskID)
	agents, _ := h.llmConfigRepo.ListBadgeOptions(ctx)
	attachments, _ := h.attachmentRepo.ListByTask(ctx, taskID)
	agentDefs := h.listTaskFormAgentDefinitions(ctx, task.ProjectID, task.AgentDefinitionID)

	return &taskDetailContentData{
		task:             task,
		taskGoal:         h.loadTaskGoal(ctx, taskID),
		executionMetrics: executionMetrics,
		schedules:        schedules,
		agents:           agents,
		agentDefs:        agentDefs,
		attachments:      attachments,
		reviewComments:   h.loadTaskReviewComments(ctx, taskID),
	}, nil
}

func (h *Handler) renderTaskDetailContent(c echo.Context, taskID, selectedTab string) error {
	data, err := h.loadTaskDetailContentData(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	return h.renderTaskDetailContentData(c, data, selectedTab)
}

func (h *Handler) renderTaskDetailContentData(c echo.Context, data *taskDetailContentData, selectedTab string) error {
	return render(c, http.StatusOK, pages.TaskDetailContent(data.task, data.taskGoal, &data.executionMetrics, data.schedules, data.agents, data.agentDefs, data.attachments, selectedTab, data.reviewComments))
}

func (h *Handler) GetTask(c echo.Context) error {
	taskID := c.Param("taskId")
	isHTMX := isHTMX(c)
	applog.Infof("[handler] GetTask id=%s htmx=%v", taskID, isHTMX)

	data, err := h.loadTaskDetailContentData(c.Request().Context(), taskID)
	if err != nil {
		if httpErr, ok := err.(*echo.HTTPError); ok && httpErr.Code == http.StatusNotFound {
			applog.Infof("[handler] GetTask not found id=%s", taskID)
		} else {
			applog.Infof("[handler] GetTask error: %v", err)
		}
		return err
	}
	task := data.task
	applog.Infof("[handler] GetTask id=%s schedules=%d attachments=%d", taskID, len(data.schedules), len(data.attachments))

	// Determine default tab
	defaultTab := c.QueryParam("tab")
	if defaultTab == "" {
		if task.Status == models.StatusCompleted ||
			task.Status == models.StatusFailed ||
			task.Status == models.StatusCancelled ||
			task.Status == models.StatusRunning {
			defaultTab = "chat"
		} else {
			defaultTab = "details"
		}
	}
	// Migrate old/alternate thread tab params to the internal chat tab key.
	if defaultTab == "history" || defaultTab == "thread" {
		defaultTab = "chat"
	}
	applog.Infof("[handler] GetTask id=%s defaultTab=%s", taskID, defaultTab)

	// HTMX request: return just the task detail content partial
	if isHTMX {
		return h.renderTaskDetailContentData(c, data, defaultTab)
	}

	// Full page load: wrap in layout
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	return render(c, http.StatusOK, pages.TaskDetailPage(projects, data.task, data.taskGoal, &data.executionMetrics, data.schedules, data.agents, data.agentDefs, data.attachments, defaultTab, data.reviewComments))
}

// GetTaskExecutions returns the bounded execution-history fragment for a task.
// The default polling path renders only the latest window; older pages are loaded
// deliberately through ?before=<exec_id> so recurring polls do not reload all
// historical output bytes.
func (h *Handler) GetTaskExecutions(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	limit := parseThreadWindowLimit(c.QueryParam("limit"), taskExecutionHistoryWindowDefault, taskExecutionHistoryWindowMax)
	executions, hasOlder, err := h.loadTaskExecutionHistoryWindow(c.Request().Context(), taskID, c.QueryParam("before"), limit)
	if err != nil {
		return err
	}
	if c.QueryParam("before") != "" {
		return render(c, http.StatusOK, components.TaskExecutionHistoryOlderPage(task, executions, hasOlder, limit))
	}

	return render(c, http.StatusOK, components.TaskExecutionHistory(task, executions, hasOlder, limit))
}

// GetTaskDetailStatus returns just the task detail metrics (status badges) for polling updates
func (h *Handler) GetTaskDetailStatus(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	metrics, err := h.execRepo.GetTaskExecutionMetrics(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] GetTaskDetailStatus error loading execution metrics task=%s: %v", taskID, err)
	}

	agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
	agentLabel := h.taskDetailAgentLabel(c.Request().Context(), task.ProjectID, task.AgentDefinitionID)

	return render(c, http.StatusOK, pages.TaskDetailMetrics(task, metrics, agents, agentLabel))
}

// GetTaskDetailActions returns just the action buttons fragment (Run Now / Edit / Delete).
// Called by the task detail page when a task_status_changed SSE event or polling detects
// that a task has transitioned to a terminal state, so the buttons update without a full refresh.
func (h *Handler) GetTaskDetailActions(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	return render(c, http.StatusOK, pages.TaskDetailActions(task))
}

func taskBranchSlug(title string) string {
	slug := strings.ToLower(title)
	slug = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(slug, "-")
	slug = regexp.MustCompile(`-+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	return slug
}

func expectedTaskBranchName(task *models.Task) string {
	if task == nil || len(task.ID) < 8 {
		return ""
	}
	slug := taskBranchSlug(task.Title)
	if slug == "" {
		slug = task.ID[:8]
	}
	return fmt.Sprintf("task/%s-%s", task.ID[:8], slug)
}

func gitRefExists(repoDir, ref string) bool {
	if repoDir == "" || ref == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func gitIsAncestor(repoDir, ancestorRef, descendantRef string) bool {
	if repoDir == "" || ancestorRef == "" || descendantRef == "" {
		return false
	}
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestorRef, descendantRef)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func gitBranchHasCommitsBeyondTarget(repoDir, targetRef, branchRef string) bool {
	if repoDir == "" || targetRef == "" || branchRef == "" {
		return false
	}
	cmd := exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", targetRef, branchRef))
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "0"
}

func worktreeCurrentBranch(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

// recoverTaskWorktreeState reattaches conventional task worktree metadata and
// reconciles stale merge_status with current Git ancestry before render/merge
// decisions hide local actions.
func (h *Handler) recoverTaskWorktreeState(ctx context.Context, task *models.Task, project *models.Project) bool {
	if task == nil || project == nil || project.RepoPath == "" {
		return false
	}

	changed := false
	worktreePath := task.WorktreePath
	if worktreePath == "" && task.ID != "" {
		candidate := filepath.Join(project.RepoPath, ".worktrees", fmt.Sprintf("task_%s", task.ID))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && service.IsGitRepo(candidate) {
			worktreePath = candidate
			task.WorktreePath = candidate
			changed = true
		}
	}

	worktreeBranch := task.WorktreeBranch
	if worktreePath != "" {
		// The checked-out branch is authoritative for an active worktree. Follow-up
		// lineage can replace the stored branch while retaining/replacing the path;
		// using the stale DB branch makes headers, file stats, and lazy file indices
		// disagree with the live target-to-working-tree diff.
		if currentBranch := worktreeCurrentBranch(worktreePath); currentBranch != "" && currentBranch != worktreeBranch {
			if worktreeBranch != "" {
				applog.Infof("[task-changes] recovering task=%s worktree branch metadata stored=%s current=%s", task.ID, worktreeBranch, currentBranch)
			}
			worktreeBranch = currentBranch
		}
	}
	if worktreeBranch == "" {
		candidate := expectedTaskBranchName(task)
		if gitRefExists(project.RepoPath, candidate) {
			worktreeBranch = candidate
		}
	}
	if worktreeBranch != "" && task.WorktreeBranch != worktreeBranch {
		task.WorktreeBranch = worktreeBranch
		changed = true
	}

	if changed && task.WorktreeBranch != "" {
		if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, task.WorktreePath, task.WorktreeBranch); err != nil {
			applog.Infof("[handler] recoverTaskWorktreeState: failed to update worktree info for task %s: %v", task.ID, err)
		}
	}

	if task.WorktreeBranch == "" || task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return changed
	}
	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		return changed
	}

	branchTipMerged := service.IsBranchTipMergedInto(project.RepoPath, task.WorktreeBranch, targetBranch)
	if branchTipMerged {
		if task.WorktreePath != "" {
			if statusOut, statusErr := service.GitStatusPorcelain(task.WorktreePath); statusErr == nil && strings.TrimSpace(statusOut) != "" {
				return changed
			}
		}
		if task.MergeStatus != models.MergeStatusMerged {
			if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged); err != nil {
				applog.Infof("[handler] recoverTaskWorktreeState: failed to update merge_status for task %s: %v", task.ID, err)
			} else {
				task.MergeStatus = models.MergeStatusMerged
			}
		}
		return changed
	}

	if task.MergeStatus == models.MergeStatusMerged && gitBranchHasCommitsBeyondTarget(project.RepoPath, targetBranch, task.WorktreeBranch) {
		if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
			applog.Infof("[handler] recoverTaskWorktreeState: failed to reset stale merge_status for task %s: %v", task.ID, err)
		} else {
			task.MergeStatus = models.MergeStatusPending
			changed = true
		}
	}
	return changed
}

// reconcileAlreadyMergedBranch detects task branches that are already
// reachable from their target branch while stored merge_status is stale. When
// such a stale state is found, it back-fills `tasks.merge_status = merged` so
// the rest of the UI does not keep offering local merge actions for an
// already-merged branch. Returns true when the branch is currently merged into
// the resolved target branch.
//
// Active tasks (running/queued) are skipped because their worktree is in use
// and the branch may legitimately match the target tip mid-execution.
func (h *Handler) taskRebaseAvailable(task *models.Task, project *models.Project, branchAlreadyMerged bool) bool {
	if task == nil || project == nil || project.RepoPath == "" || task.WorktreeBranch == "" || task.WorktreePath == "" {
		return false
	}
	if branchAlreadyMerged || task.MergeStatus == models.MergeStatusMerged || task.MergeStatus == models.MergeStatusConflict {
		return false
	}
	if len(service.ActiveConflictFiles(project.RepoPath)) > 0 || service.IsGitWorktreeLocked(project.RepoPath, task.WorktreePath) {
		return false
	}
	if status, err := service.GitStatusPorcelain(task.WorktreePath); err != nil || strings.TrimSpace(status) != "" {
		return false
	}
	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		return false
	}
	return service.IsBranchDivergedFromTarget(project.RepoPath, task.WorktreeBranch, targetBranch)
}

func (h *Handler) reconcileAlreadyMergedBranch(ctx context.Context, task *models.Task) bool {
	if task == nil || task.WorktreeBranch == "" {
		return false
	}
	if task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return false
	}

	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return false
	}
	h.recoverTaskWorktreeState(ctx, task, project)
	if task.WorktreePath != "" {
		if statusOut, statusErr := service.GitStatusPorcelain(task.WorktreePath); statusErr == nil && strings.TrimSpace(statusOut) != "" {
			return false
		}
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		return false
	}

	return service.IsBranchTipMergedInto(project.RepoPath, task.WorktreeBranch, targetBranch)
}

type taskMergeActionState struct {
	UseWorktreeContent  bool
	BranchAlreadyMerged bool
	RebaseAvailable     bool
}

func (h *Handler) resolveTaskMergeActionState(ctx context.Context, task *models.Task) taskMergeActionState {
	var state taskMergeActionState
	if task == nil {
		return state
	}
	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	h.recoverTaskWorktreeState(ctx, task, project)
	if task.WorktreeBranch == "" {
		return state
	}
	state.UseWorktreeContent = true
	state.BranchAlreadyMerged = h.reconcileAlreadyMergedBranch(ctx, task)
	state.RebaseAvailable = h.taskRebaseAvailable(task, project, state.BranchAlreadyMerged)
	return state
}

type taskChangesWorktreeState struct {
	UseWorktreeContent    bool
	DiffOutput            string
	FileStats             []service.WorktreeFileStat
	BranchAlreadyMerged   bool
	LocalMergeUnavailable bool
	ConflictRecovery      bool
	RebaseAvailable       bool
}

type taskMergeEligibility struct {
	MergeAvailable   bool
	ConflictRecovery bool
	Reason           string
	TargetBranch     string
}

func (h *Handler) resolveTaskMergeEligibility(ctx context.Context, task *models.Task, project *models.Project, branchAlreadyMerged bool) taskMergeEligibility {
	if task == nil || project == nil || project.RepoPath == "" || !service.IsGitRepo(project.RepoPath) {
		return taskMergeEligibility{Reason: "project repository is unavailable"}
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	result := taskMergeEligibility{TargetBranch: targetBranch}

	hasActiveMerge := service.HasActiveMerge(project.RepoPath)
	activeConflictFiles := service.ActiveConflictFiles(project.RepoPath)
	ownsLiveConflict := (hasActiveMerge && service.ActiveMergeMatchesBranch(project.RepoPath, task.WorktreeBranch)) ||
		(!hasActiveMerge && task.MergeStatus == models.MergeStatusConflict && len(activeConflictFiles) > 0)
	if ownsLiveConflict {
		if task.MergeStatus != models.MergeStatusConflict {
			if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err == nil {
				task.MergeStatus = models.MergeStatusConflict
			}
		}
		result.ConflictRecovery = true
		return result
	}
	if hasActiveMerge || len(activeConflictFiles) > 0 {
		result.Reason = "another merge or conflict is active in the project repository"
		return result
	}

	if task.MergeStatus == models.MergeStatusConflict {
		switch task.Status {
		case models.StatusCompleted, models.StatusFailed, models.StatusCancelled:
			if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
				result.Reason = "merge conflict status could not be refreshed"
				return result
			}
			task.MergeStatus = models.MergeStatusPending
		default:
			result.Reason = "task conflict recovery is not ready"
			return result
		}
	}

	if branchAlreadyMerged || task.MergeStatus == models.MergeStatusMerged {
		result.Reason = "task branch is already merged"
		return result
	}
	switch task.Status {
	case models.StatusCompleted, models.StatusFailed, models.StatusCancelled:
	default:
		result.Reason = fmt.Sprintf("task status %s is not mergeable", task.Status)
		return result
	}
	if task.WorktreeBranch == "" || !gitRefExists(project.RepoPath, task.WorktreeBranch) {
		result.Reason = "task branch does not exist"
		return result
	}
	if targetBranch == "" || !gitRefExists(project.RepoPath, targetBranch) {
		result.Reason = "target branch does not exist"
		return result
	}

	result.MergeAvailable = true
	return result
}

// resolveTaskChangesWorktreeState is the canonical handler-level resolver for
// Changes surfaces that need to choose live worktree state versus preserved
// execution diffs. Endpoint handlers remain responsible only for request
// parsing, review/PR loading, and rendering their specific fragments.
func (h *Handler) resolveTaskChangesWorktreeState(ctx context.Context, task *models.Task) taskChangesWorktreeState {
	var state taskChangesWorktreeState
	if task == nil {
		return state
	}

	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	var preservedDiffOnce struct {
		val  string
		done bool
	}
	preservedDiff := func() string {
		if !preservedDiffOnce.done {
			preservedDiffOnce.val, _ = h.execRepo.GetLatestNonEmptyDiffOutput(ctx, task.ID)
			preservedDiffOnce.done = true
		}
		return preservedDiffOnce.val
	}

	mergeState := h.resolveTaskMergeActionState(ctx, task)
	if !mergeState.UseWorktreeContent {
		state.DiffOutput = preservedDiff()
		return state
	}

	state.UseWorktreeContent = true
	state.BranchAlreadyMerged = mergeState.BranchAlreadyMerged
	mergeEligibility := h.resolveTaskMergeEligibility(ctx, task, project, state.BranchAlreadyMerged)
	state.LocalMergeUnavailable = !mergeEligibility.MergeAvailable && !mergeEligibility.ConflictRecovery
	state.ConflictRecovery = mergeEligibility.ConflictRecovery
	state.RebaseAvailable = mergeEligibility.MergeAvailable && mergeState.RebaseAvailable
	isActive := task.Status == models.StatusRunning || task.Status == models.StatusQueued

	// For non-active merged tasks, live git diff is empty after integration;
	// preserve the execution diff for review while hiding local merge actions.
	if !isActive && task.MergeStatus == models.MergeStatusMerged {
		state.DiffOutput = preservedDiff()
		return state
	}

	if task.WorktreePath != "" && project != nil && project.RepoPath != "" {
		if _, err := os.Stat(task.WorktreePath); err == nil {
			targetBranch := task.MergeTargetBranch
			if targetBranch == "" {
				targetBranch = service.GetDefaultBranch(project.RepoPath)
			}
			useLiveUncommitted := isActive || task.MergeStatus != models.MergeStatusMerged
			if useLiveUncommitted {
				state.DiffOutput = service.GetWorktreeDiffWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, task.WorktreePath)
				state.FileStats = service.GetWorktreeFileStatsWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, task.WorktreePath)
			} else {
				state.DiffOutput = service.GetWorktreeDiff(project.RepoPath, task.WorktreeBranch, targetBranch)
				state.FileStats = service.GetWorktreeFileStats(project.RepoPath, task.WorktreeBranch, targetBranch)
			}
			if strings.TrimSpace(state.DiffOutput) == "" && !isActive && (state.BranchAlreadyMerged || service.IsBranchMerged(project.RepoPath, task.WorktreeBranch, targetBranch)) {
				if diff := preservedDiff(); diff != "" {
					state.DiffOutput = diff
					state.FileStats = nil
				}
			}
			return state
		}
	}

	// Missing/unavailable worktrees fall back to the preserved execution diff.
	state.DiffOutput = preservedDiff()
	state.FileStats = nil
	return state
}

// resolveTaskChangesDiffOutput resolves only the diff payload used by diff-only
// Changes fragments. The state decision is shared with full worktree renders.
func (h *Handler) resolveTaskChangesDiffOutput(ctx context.Context, task *models.Task) string {
	return h.resolveTaskChangesWorktreeState(ctx, task).DiffOutput
}

// resolveTaskChangesFileMeta resolves one lazy diff-card target without forcing
// worktree-backed requests through whole-task diff and stats generation.
func (h *Handler) resolveTaskChangesFileMeta(ctx context.Context, task *models.Task, fileIndex int) (components.DiffFileRenderMeta, bool) {
	if task == nil || fileIndex < 0 {
		return components.DiffFileRenderMeta{}, false
	}

	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	h.recoverTaskWorktreeState(ctx, task, project)

	preservedMeta := func() (components.DiffFileRenderMeta, bool) {
		diffOutput, _ := h.execRepo.GetLatestNonEmptyDiffOutput(ctx, task.ID)
		return components.DiffRenderMetaByIndex(diffOutput, fileIndex)
	}

	if task.WorktreeBranch == "" {
		return preservedMeta()
	}

	isActive := task.Status == models.StatusRunning || task.Status == models.StatusQueued
	if !isActive && task.MergeStatus == models.MergeStatusMerged {
		return preservedMeta()
	}

	if task.WorktreePath == "" || project == nil || project.RepoPath == "" {
		return preservedMeta()
	}
	if _, err := os.Stat(task.WorktreePath); err != nil {
		return preservedMeta()
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		return components.DiffFileRenderMeta{}, false
	}

	useLiveUncommitted := isActive || task.MergeStatus != models.MergeStatusMerged
	var diffOutput string
	var ok bool
	if useLiveUncommitted {
		diffOutput, ok = service.GetWorktreeDiffFileWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, task.WorktreePath, fileIndex)
	} else {
		diffOutput, ok = service.GetWorktreeDiffFileWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, "", fileIndex)
	}
	if !ok {
		if !isActive && (h.reconcileAlreadyMergedBranch(ctx, task) || service.IsBranchMerged(project.RepoPath, task.WorktreeBranch, targetBranch)) {
			return preservedMeta()
		}
		return components.DiffFileRenderMeta{}, false
	}
	meta, ok := components.DiffRenderMetaByIndex(diffOutput, 0)
	if !ok {
		return components.DiffFileRenderMeta{}, false
	}
	meta.Index = fileIndex
	return meta, true
}

// GetTaskChanges returns just the changes tab content for fresh updates when switching tabs.
// If the task has a worktree branch, it shows the worktree-specific diff.
func (h *Handler) GetTaskChanges(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	ctx := c.Request().Context()
	state := h.resolveTaskChangesWorktreeState(ctx, task)

	reviewComments := h.loadTaskReviewComments(ctx, taskID)

	diffView := h.uiDiffViewPreference(ctx)

	if state.UseWorktreeContent {
		var taskPR *models.TaskPullRequest
		if h.taskPullRequestRepo != nil {
			taskPR, _ = h.taskPullRequestRepo.GetByTaskID(ctx, taskID)
		}
		return render(c, http.StatusOK, pages.TaskChangesWorktreeContentWithView(
			state.DiffOutput, task, state.FileStats, reviewComments, taskPR, state.LocalMergeUnavailable, state.ConflictRecovery, state.RebaseAvailable, diffView,
		))
	}

	return render(c, http.StatusOK, pages.TaskChangesContentWithView(state.DiffOutput, task.ID, reviewComments, diffView))
}

// GetTaskChangesFile returns a single diff file card for per-file lazy loading.
func (h *Handler) GetTaskChangesFile(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	fileIndex, err := strconv.Atoi(c.QueryParam("file_index"))
	if err != nil || fileIndex < 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid file_index")
	}

	view := c.QueryParam("view")
	if view != "split" {
		view = "inline"
	}

	reviewMode := strings.EqualFold(c.QueryParam("review"), "true")
	var reviewComments []models.ReviewComment
	if reviewMode {
		reviewComments = h.loadTaskReviewComments(c.Request().Context(), taskID)
	}

	meta, exists := h.resolveTaskChangesFileMeta(c.Request().Context(), task, fileIndex)
	return render(c, http.StatusOK, components.LoadDiffFileCardMeta(meta, exists, view, taskID, reviewComments, reviewMode))
}

// GetTaskChangesLive returns only the diff viewer fragment for realtime updates.
func (h *Handler) GetTaskChangesLive(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	diffOutput := c.FormValue("diff_output")
	if diffOutput == "" {
		diffOutput = c.QueryParam("diff_output")
	}
	resolvedDiff := h.resolveTaskChangesDiffOutput(c.Request().Context(), task)
	if task.WorktreeBranch != "" {
		// A worktree Changes fragment never trusts an execution/ client HEAD diff:
		// use the same recovered target-to-current-worktree view as the full tab.
		// An empty resolved diff is valid when the worktree matches the target.
		diffOutput = resolvedDiff
	} else if diffOutput == "" {
		diffOutput = resolvedDiff
	}

	reviewComments := h.loadTaskReviewComments(c.Request().Context(), taskID)

	component := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<div id="diff-viewer-container">`); err != nil {
			return err
		}
		diffView := h.uiDiffViewPreference(c.Request().Context())
		if err := components.DiffViewerWithReviewView(diffOutput, task.ID, reviewComments, diffView).Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</div>`)
		return err
	})

	return render(c, http.StatusOK, component)
}

func (h *Handler) updateTaskGoalFromEditForm(c echo.Context, taskID string) error {
	if c.FormValue("goal_present") == "" || h.taskGoalSvc == nil {
		return nil
	}

	goalText := strings.TrimSpace(c.FormValue("goal"))
	existingGoal := h.loadTaskGoal(c.Request().Context(), taskID)
	if goalText == "" {
		if existingGoal != nil && existingGoal.Status != models.TaskGoalStatusCleared {
			return h.taskGoalSvc.ClearGoal(c.Request().Context(), taskID, "user")
		}
		return nil
	}

	if existingGoal == nil || existingGoal.Status == models.TaskGoalStatusCleared || strings.TrimSpace(existingGoal.Objective) != goalText {
		if _, err := h.taskGoalSvc.SetGoal(c.Request().Context(), taskID, goalText, service.GoalOptions{Actor: "user"}); err != nil {
			if err == service.ErrTaskGoalEmpty || err == service.ErrTaskGoalTooLong {
				return echo.NewHTTPError(http.StatusBadRequest, err.Error())
			}
			return err
		}
	}

	if c.FormValue("goal_status_present") == "" {
		return nil
	}
	latestGoal := h.loadTaskGoal(c.Request().Context(), taskID)
	if latestGoal == nil || latestGoal.Status == models.TaskGoalStatusCleared {
		return nil
	}
	goalActive := c.FormValue("goal_active") == "on" || c.FormValue("goal_active") == "true"
	if goalActive && (latestGoal.Status == models.TaskGoalStatusPaused || latestGoal.Status == models.TaskGoalStatusBlocked) {
		return h.taskGoalSvc.ResumeGoal(c.Request().Context(), taskID, "user")
	}
	if !goalActive && latestGoal.Status == models.TaskGoalStatusActive {
		return h.taskGoalSvc.PauseGoal(c.Request().Context(), taskID, "user")
	}
	return nil
}

func (h *Handler) UpdateTask(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] UpdateTask id=%s", taskID)
	if _, err := parseBoundedMultipartForm(c, maxUploadSize, maxTaskAttachmentFilesPerRequest); err != nil {
		applog.Infof("[handler] UpdateTask error parsing form: %v", err)
		if httpErr, ok := err.(*echo.HTTPError); ok {
			return httpErr
		}
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] UpdateTask fetch error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] UpdateTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	oldCategory := task.Category
	oldStatus := task.Status
	newCategory := models.TaskCategory(c.FormValue("category"))
	isCancellableActiveWork := oldStatus == models.StatusRunning || oldStatus == models.StatusQueued || (oldStatus == models.StatusPending && models.IsSwarmChildRole(task.SwarmRole))
	stopActiveViaCategoryUpdate := oldCategory == models.CategoryActive && newCategory != oldCategory && newCategory != models.CategoryActive && isCancellableActiveWork
	if oldCategory != newCategory && newCategory == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			applog.Infof("[handler] UpdateTask model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			applog.Infof("[handler] UpdateTask blocked: no models configured task=%s", taskID)
			return noModelsConfiguredResponse(c)
		}
	}

	task.Title = c.FormValue("title")
	// Category transitions must go through UpdateCategory after the generic field
	// update so completed_at, display order, events, and execution lifecycle stay
	// consistent with drag/drop transitions.
	task.Category = oldCategory
	task.Prompt = c.FormValue("prompt")
	task.Tag = models.TaskTag(c.FormValue("tag"))
	priorityValue := strings.TrimSpace(c.FormValue("priority"))
	priority, err := strconv.Atoi(priorityValue)
	if priorityValue == "" || err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Task priority must be between 1 and 4")
	}
	task.Priority = priority

	// Handle optional agent (LLM config) selection
	if agentID := c.FormValue("agent_id"); agentID != "" {
		task.AgentID = &agentID
	} else {
		task.AgentID = nil
	}
	// Handle optional primary Agent definition selection separately from the model config.
	agentDefID, err := h.resolvePrimaryAgentDefinition(c.Request().Context(), task.ProjectID, c.FormValue("agent_definition_id"))
	if err != nil {
		return err
	}
	task.AgentDefinitionID = agentDefID

	// Handle auto-merge settings — if the hidden sentinel is present, the edit form
	// was submitted and we always update (unchecked checkbox sends no value).
	if c.FormValue("auto_merge_present") != "" {
		task.AutoMerge = c.FormValue("auto_merge") == "on" || c.FormValue("auto_merge") == "true"
	}
	if targetBranch := c.FormValue("merge_target_branch"); targetBranch != "" {
		task.MergeTargetBranch = targetBranch
	}

	applog.Infof("[handler] UpdateTask id=%s title=%q category=%s->%s tag=%s", taskID, task.Title, oldCategory, newCategory, task.Tag)
	if err := h.taskSvc.Update(c.Request().Context(), task); err != nil {
		if errors.Is(err, service.ErrDuplicateTask) {
			applog.Infof("[handler] UpdateTask duplicate title=%q", task.Title)
			return echo.NewHTTPError(http.StatusConflict, "A task with this name already exists in this project")
		}
		if httpErr := taskRequiredFieldHTTPError(err); httpErr != nil {
			return httpErr
		}
		applog.Infof("[handler] UpdateTask error: %v", err)
		return err
	}
	if err := h.updateTaskGoalFromEditForm(c, taskID); err != nil {
		return err
	}

	// Handle file uploads if present (multipart form)
	if form, err := c.MultipartForm(); err == nil && form != nil {
		if files := form.File["files"]; len(files) > 0 {
			h.persistTaskAttachmentFiles(c.Request().Context(), taskID, files, "UpdateTask")
		}
	}

	// Handle removal of attachments (comma-separated IDs)
	if removeIDs := c.FormValue("remove_attachments"); removeIDs != "" {
		for _, attID := range strings.Split(removeIDs, ",") {
			attID = strings.TrimSpace(attID)
			if attID == "" {
				continue
			}
			att, err := h.attachmentRepo.GetByID(c.Request().Context(), attID)
			if err != nil || att == nil || att.TaskID != taskID {
				continue
			}
			if err := h.attachmentRepo.Delete(c.Request().Context(), attID); err != nil {
				applog.Infof("[handler] UpdateTask error deleting attachment %s: %v", attID, err)
				continue
			}
			os.Remove(att.FilePath)
			applog.Infof("[handler] UpdateTask removed attachment %s from task %s", attID, taskID)
		}
	}

	// Task detail edits are metadata saves. They may update the stored category for
	// display/sorting, but must not activate or enqueue work; explicit Run Now,
	// drag/drop category changes, schedules, and task-thread follow-ups own those
	// execution side effects.
	if stopActiveViaCategoryUpdate {
		applog.Infof("[handler] UpdateTask category changed from Active while %s, cancelling id=%s", oldStatus, taskID)
		if err := h.taskSvc.UpdateCategory(c.Request().Context(), taskID, newCategory); err != nil {
			applog.Infof("[handler] UpdateTask error stopping active task: %v", err)
			return err
		}
	} else if oldCategory != newCategory {
		applog.Infof("[handler] UpdateTask metadata category changed %s->%s id=%s", oldCategory, newCategory, taskID)
		if err := h.taskRepo.UpdateCategory(c.Request().Context(), taskID, newCategory); err != nil {
			applog.Infof("[handler] UpdateTask error changing metadata category: %v", err)
			return err
		}
	}

	applog.Infof("[handler] UpdateTask success id=%s", taskID)

	// Re-fetch updated task data for rendering
	if isHTMX(c) {
		return h.renderTaskDetailContent(c, taskID, "details")
	}

	return c.Redirect(http.StatusSeeOther, "/tasks/"+task.ID)
}

func (h *Handler) DeleteTask(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] DeleteTask task=%s", taskID)

	// Fetch task before deleting to get projectID for kanban board response
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] DeleteTask fetch error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] DeleteTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	projectID := task.ProjectID

	if err := h.taskSvc.Delete(c.Request().Context(), taskID); err != nil {
		applog.Infof("[handler] DeleteTask error: %v", err)
		return err
	}
	applog.Infof("[handler] DeleteTask success id=%s", taskID)

	// If redirect=list (from task detail page), redirect to the safe return target.
	if isHTMX(c) && c.QueryParam("redirect") == "list" {
		redirectURL := "/tasks?project_id=" + projectID
		if c.QueryParam("return_to") == "schedule" {
			redirectURL = "/schedule?project_id=" + projectID
		}
		c.Response().Header().Set("HX-Redirect", redirectURL)
		return c.NoContent(http.StatusOK)
	}

	// Return the full kanban board for HTMX requests (consistent with other task operations)
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) RunTask(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] RunTask task=%s", taskID)

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		applog.Infof("[handler] RunTask model availability check error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
	}
	if !hasModels {
		applog.Infof("[handler] RunTask blocked: no models configured task=%s", taskID)
		return noModelsConfiguredResponse(c)
	}

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] RunTask fetch error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] RunTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if h.swarmSvc == nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "swarm service unavailable")
		}
		if err := h.swarmSvc.StartPlanner(c.Request().Context(), taskID); err != nil {
			applog.Infof("[handler] RunTask swarm planner start error: %v", err)
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
	} else if err := h.taskSvc.RunTask(c.Request().Context(), taskID); err != nil {
		applog.Infof("[handler] RunTask error: %v", err)
		return err
	}
	applog.Infof("[handler] RunTask handled task=%s", taskID)

	// Return no content for HTMX requests — the dialog close handler on each page
	// will refresh relevant content (e.g., kanban board on tasks page)
	if isHTMX(c) {
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks/"+taskID)
}

type taskCancellationResult struct {
	OK               bool                `json:"ok"`
	Accepted         bool                `json:"accepted"`
	TaskID           string              `json:"task_id"`
	Title            string              `json:"title"`
	PreviousStatus   models.TaskStatus   `json:"previous_status"`
	PreviousCategory models.TaskCategory `json:"previous_category"`
	FinalStatus      models.TaskStatus   `json:"final_status"`
	FinalCategory    models.TaskCategory `json:"final_category"`
	Message          string              `json:"message"`
}

func taskIsCancellableByUser(task *models.Task) bool {
	if task == nil {
		return false
	}
	return task.Status == models.StatusRunning || task.Status == models.StatusQueued || (task.Status == models.StatusPending && task.Category == models.CategoryActive) || (task.SwarmRole == models.SwarmRoleParent && task.Status == models.StatusBlocked && task.Category == models.CategoryActive)
}

func (h *Handler) cancelTaskWork(ctx context.Context, task *models.Task, composerStop bool, operation string) (*taskCancellationResult, error) {
	if task == nil {
		return nil, fmt.Errorf("task not found")
	}
	result := &taskCancellationResult{
		OK:               true,
		TaskID:           task.ID,
		Title:            task.Title,
		PreviousStatus:   task.Status,
		PreviousCategory: task.Category,
		FinalStatus:      task.Status,
		FinalCategory:    task.Category,
	}
	if !taskIsCancellableByUser(task) {
		result.Accepted = false
		result.Message = fmt.Sprintf("Task is not currently cancellable (status=%s, category=%s).", task.Status, task.Category)
		return result, nil
	}

	if h.workerSvc != nil {
		h.workerSvc.MarkCancellationRequested(task.ID)
	}
	if !composerStop && h.threadInputRepo != nil {
		if err := h.threadInputRepo.CancelPendingForTask(ctx, task.ID); err != nil {
			applog.Infof("[handler] %s error cancelling pending thread inputs task=%s: %v", operation, task.ID, err)
		}
	}
	if task.SwarmRole == models.SwarmRoleParent && h.swarmSvc != nil {
		if err := h.swarmSvc.CancelSwarm(ctx, task.ID); err != nil {
			applog.Infof("[handler] %s swarm cascade error: %v", operation, err)
			return nil, err
		}
	} else if err := h.taskSvc.CancelTask(ctx, task.ID); err != nil {
		applog.Infof("[handler] %s error: %v", operation, err)
		return nil, err
	} else if models.IsSwarmChildRole(task.SwarmRole) {
		h.notifySwarmChildTerminal(ctx, task.ID)
	}
	h.cancelActiveExecutionsAndPublish(ctx, task.ID, operation)
	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		result.FinalStatus = updated.Status
		result.FinalCategory = updated.Category
	}
	result.Accepted = true
	result.Message = "Cancellation requested."
	return result, nil
}

func (h *Handler) CancelTask(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] CancelTask task=%s", taskID)

	// Fetch task to get projectID for kanban board response
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] CancelTask fetch error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] CancelTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	projectID := task.ProjectID

	composerStop := c.QueryParam("composer_stop") == "1"
	result, err := h.cancelTaskWork(c.Request().Context(), task, composerStop, "CancelTask")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if result != nil && !result.Accepted {
		applog.Infof("[handler] CancelTask not cancellable task=%s status=%s category=%s", taskID, task.Status, task.Category)
		return echo.NewHTTPError(http.StatusBadRequest, result.Message)
	}
	applog.Infof("[handler] CancelTask cancelled task=%s", taskID)

	// Return the full kanban board for HTMX requests
	if isHTMX(c) {
		if composerStop {
			return render(c, http.StatusOK, components.ChatComposerActionButtonOOB("task-thread-form-primary-action", fmt.Sprintf("/tasks/%s/cancel?composer_stop=1", taskID), false, ""))
		}
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks/"+taskID)
}

func (h *Handler) UpdateTaskCategory(c echo.Context) error {
	taskID := c.Param("taskId")
	category := models.TaskCategory(c.FormValue("category"))
	applog.Infof("[handler] UpdateTaskCategory task=%s newCategory=%s", taskID, category)

	// Validate: cannot move to scheduled category unless the task has a schedule
	if category == models.CategoryScheduled {
		schedules, err := h.scheduleRepo.ListByTask(c.Request().Context(), taskID)
		if err != nil {
			applog.Infof("[handler] UpdateTaskCategory error checking schedules: %v", err)
			return err
		}
		if len(schedules) == 0 {
			applog.Infof("[handler] UpdateTaskCategory rejected: task %s has no schedule", taskID)
			return echo.NewHTTPError(http.StatusBadRequest, "Cannot move task to Scheduled category: task has no schedule. Create a schedule first.")
		}
	}
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			applog.Infof("[handler] UpdateTaskCategory model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			applog.Infof("[handler] UpdateTaskCategory blocked: no models configured task=%s", taskID)
			return noModelsConfiguredResponse(c)
		}
	}

	if err := h.taskSvc.UpdateCategory(c.Request().Context(), taskID, category); err != nil {
		applog.Infof("[handler] UpdateTaskCategory error: %v", err)
		return err
	}
	applog.Infof("[handler] UpdateTaskCategory success task=%s -> %s", taskID, category)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) MoveCompletedActiveToCompleted(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	applog.Infof("[handler] MoveCompletedActiveToCompleted project=%s", projectID)

	count, err := h.taskSvc.MoveCompletedActiveToCompleted(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] MoveCompletedActiveToCompleted error: %v", err)
		return err
	}
	applog.Infof("[handler] MoveCompletedActiveToCompleted moved %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) UpdateTaskStatus(c echo.Context) error {
	taskID := c.Param("taskId")
	status := models.TaskStatus(c.FormValue("status"))
	applog.Infof("[handler] UpdateTaskStatus task=%s newStatus=%s", taskID, status)

	if err := h.taskSvc.UpdateStatus(c.Request().Context(), taskID, status); err != nil {
		applog.Infof("[handler] UpdateTaskStatus error: %v", err)
		return err
	}
	applog.Infof("[handler] UpdateTaskStatus success task=%s -> %s", taskID, status)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) BatchUpdateTaskCategory(c echo.Context) error {
	projectID := c.FormValue("project_id")
	taskIDs := c.FormValue("task_ids")
	category := models.TaskCategory(c.FormValue("category"))
	applog.Infof("[handler] BatchUpdateTaskCategory project=%s category=%s task_ids=%s", projectID, category, taskIDs)

	// Validate: if moving to scheduled category, all tasks must have schedules
	if category == models.CategoryScheduled {
		for _, id := range strings.Split(taskIDs, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			schedules, err := h.scheduleRepo.ListByTask(c.Request().Context(), id)
			if err != nil {
				applog.Infof("[handler] BatchUpdateTaskCategory error checking schedules for task=%s: %v", id, err)
				return err
			}
			if len(schedules) == 0 {
				applog.Infof("[handler] BatchUpdateTaskCategory rejected: task %s has no schedule", id)
				return echo.NewHTTPError(http.StatusBadRequest, "Cannot move tasks to Scheduled category: one or more tasks have no schedule")
			}
		}
	}
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			applog.Infof("[handler] BatchUpdateTaskCategory model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			applog.Infof("[handler] BatchUpdateTaskCategory blocked: no models configured project=%s", projectID)
			return noModelsConfiguredResponse(c)
		}
	}

	for _, id := range strings.Split(taskIDs, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := h.taskSvc.UpdateCategory(c.Request().Context(), id, category); err != nil {
			applog.Infof("[handler] BatchUpdateTaskCategory error task=%s: %v", id, err)
			return err
		}
	}
	applog.Infof("[handler] BatchUpdateTaskCategory success")

	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) DeleteAllCompletedTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	applog.Infof("[handler] DeleteAllCompletedTasks project=%s", projectID)

	count, err := h.taskSvc.DeleteAllCompleted(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] DeleteAllCompletedTasks error: %v", err)
		return err
	}
	applog.Infof("[handler] DeleteAllCompletedTasks deleted %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) DeleteAllBacklogTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	applog.Infof("[handler] DeleteAllBacklogTasks project=%s", projectID)

	count, err := h.taskSvc.DeleteAllBacklog(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] DeleteAllBacklogTasks error: %v", err)
		return err
	}
	applog.Infof("[handler] DeleteAllBacklogTasks deleted %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) ReorderTask(c echo.Context) error {
	taskID := c.Param("taskId")
	newPosition, err := strconv.Atoi(c.FormValue("position"))
	if err != nil {
		applog.Infof("[handler] ReorderTask invalid position: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid position")
	}
	applog.Infof("[handler] ReorderTask task=%s newPosition=%d", taskID, newPosition)

	if err := h.taskSvc.ReorderTask(c.Request().Context(), taskID, newPosition); err != nil {
		applog.Infof("[handler] ReorderTask error: %v", err)
		return err
	}
	applog.Infof("[handler] ReorderTask success task=%s -> position %d", taskID, newPosition)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) ExecuteBacklogTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	priorityStr := c.QueryParam("priority")
	priority := 0
	if priorityStr != "" {
		var err error
		priority, err = strconv.Atoi(priorityStr)
		if err != nil || priority < 0 || priority > 4 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid priority")
		}
	}
	applog.Infof("[handler] ExecuteBacklogTasks project=%s priority=%d", projectID, priority)

	tasks, submitted, err := h.taskSvc.ExecuteBacklogTasks(c.Request().Context(), projectID, priority)
	if err != nil {
		applog.Infof("[handler] ExecuteBacklogTasks error: %v", err)
		return err
	}
	applog.Infof("[handler] ExecuteBacklogTasks submitted %d/%d tasks", submitted, len(tasks))

	// Return the full kanban board
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, nil)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) CountBacklogByPriority(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	applog.Infof("[handler] CountBacklogByPriority project=%s", projectID)

	counts, err := h.taskSvc.CountBacklogByPriority(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] CountBacklogByPriority error: %v", err)
		return err
	}

	return c.JSON(http.StatusOK, counts)
}

func (h *Handler) SetBacklogSort(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	sortBy := c.QueryParam("sort")
	applog.Infof("[handler] SetBacklogSort project=%s sort=%s", projectID, sortBy)

	if !isValidBacklogSort(sortBy) {
		applog.Infof("[handler] SetBacklogSort invalid sort: %s", sortBy)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort parameter")
	}

	setTaskSortCookie(c, backlogSortCookieName, sortBy)
	applog.Infof("[handler] SetBacklogSort cookie set: %s", sortBy)

	// Return the full kanban board with the new sort order
	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, func(sortPrefs *taskSortPreferences) {
			sortPrefs.Backlog = sortBy
		})
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) SetCompletedSort(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	sortBy := c.QueryParam("sort")
	applog.Infof("[handler] SetCompletedSort project=%s sort=%s", projectID, sortBy)

	if !isValidCompletedSort(sortBy) {
		applog.Infof("[handler] SetCompletedSort invalid sort: %s", sortBy)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort parameter")
	}

	setTaskSortCookie(c, completedSortCookieName, sortBy)
	applog.Infof("[handler] SetCompletedSort cookie set: %s", sortBy)

	if isHTMX(c) {
		return h.renderTaskBoardRefresh(c, projectID, func(sortPrefs *taskSortPreferences) {
			sortPrefs.Completed = sortBy
		})
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) UpdateTaskChainConfig(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] UpdateTaskChainConfig id=%s", taskID)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] UpdateTaskChainConfig fetch error: %v", err)
		return err
	}
	if task == nil {
		applog.Infof("[handler] UpdateTaskChainConfig not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Parse chain configuration from form
	enabled := c.FormValue("chain_enabled") == "true"
	trigger := c.FormValue("chain_trigger")
	childAgentID := c.FormValue("chain_child_agent_id")
	childModel := c.FormValue("chain_child_model")
	childCategory := c.FormValue("chain_child_category")

	config := &models.ChainConfiguration{
		Enabled:       enabled,
		Trigger:       trigger,
		ChildAgentID:  childAgentID,
		ChildModel:    childModel,
		ChildCategory: childCategory,
	}

	applog.Infof("[handler] UpdateTaskChainConfig id=%s enabled=%v trigger=%s child_agent=%s child_model=%s child_category=%s",
		taskID, enabled, trigger, childAgentID, childModel, childCategory)

	// Update task with new chain config
	if err := task.SetChainConfig(config); err != nil {
		applog.Infof("[handler] UpdateTaskChainConfig error serializing config: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chain configuration")
	}

	if err := h.taskSvc.Update(c.Request().Context(), task); err != nil {
		applog.Infof("[handler] UpdateTaskChainConfig error updating task: %v", err)
		return err
	}

	// Manage blocked child task for visibility:
	// - Chain enabled: pre-create blocked child so it's visible on the board
	// - Chain disabled: remove any existing blocked child
	if enabled {
		existing, _ := h.taskRepo.FindBlockedChildByParent(c.Request().Context(), taskID)
		if existing == nil {
			blockedChild := llmworkflow.BuildBlockedChild(*task, config)
			if createErr := h.taskSvc.Create(c.Request().Context(), blockedChild); createErr != nil {
				applog.Infof("[handler] UpdateTaskChainConfig error creating blocked child: %v", createErr)
			} else {
				applog.Infof("[handler] UpdateTaskChainConfig pre-created blocked child id=%s for parent=%s", blockedChild.ID, taskID)
			}
		} else {
			applog.Infof("[handler] UpdateTaskChainConfig blocked child already exists id=%s for parent=%s", existing.ID, taskID)
		}
	} else {
		if delErr := h.taskRepo.DeleteBlockedChildrenByParent(c.Request().Context(), taskID); delErr != nil {
			applog.Infof("[handler] UpdateTaskChainConfig error deleting blocked children: %v", delErr)
		} else {
			applog.Infof("[handler] UpdateTaskChainConfig removed blocked children for parent=%s (chain disabled)", taskID)
		}
	}

	applog.Infof("[handler] UpdateTaskChainConfig success id=%s", taskID)

	// Return updated task detail content
	if isHTMX(c) {
		return h.renderTaskDetailContent(c, taskID, "chaining")
	}

	return c.NoContent(http.StatusOK)
}

func (h *Handler) TaskThreadComposerAction(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	executions, err := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	activeTurnID := ""
	for _, exec := range executions {
		if exec.Status == models.ExecRunning {
			activeTurnID = exec.ID
		}
	}
	return render(c, http.StatusOK, components.ChatComposerActionButtonOOB("task-thread-form-primary-action", fmt.Sprintf("/tasks/%s/cancel?composer_stop=1", taskID), components.TaskThreadHasActiveComposerStopState(task, executions), activeTurnID))
}

// TaskThreadSelectModel persists a task-thread composer model selection
// immediately, independent of sending a message. Without this, changing the
// model dropdown only mutates client-side DOM/hidden-input state; since task
// threads intentionally do not use the Chat page's localStorage persistence
// (which is keyed by ProjectID), navigating away and back would otherwise
// re-render the composer from the task's unchanged Task.AgentID and silently
// revert the visible selection. "auto" and "" are one-off routing choices and
// are not persisted here (matches TaskThreadSend's persistence semantics).
func (h *Handler) TaskThreadSelectModel(c echo.Context) error {
	taskID := c.Param("taskId")
	agentID := c.FormValue("agent_id")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Swarm parent tasks resolve their assigned agent through swarm-specific
	// semantics (see SwarmService.resolveAssignedAgentID and child creation),
	// not through direct Task.AgentID composer persistence. TaskThreadSend
	// skips AgentID persistence for swarm parents by routing through
	// HandleParentFollowup first; mirror that here so this endpoint cannot
	// mutate parent.AgentID and unintentionally affect swarm child creation.
	if task.SwarmRole == models.SwarmRoleParent {
		return c.NoContent(http.StatusNoContent)
	}

	if agentID != "" && agentID != "auto" && h.taskRepo != nil {
		agent, selErr := h.selectAgent(c.Request().Context(), agentID, "", false)
		if selErr != nil || agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model selection")
		}
		if task.AgentID == nil || *task.AgentID != agent.ID {
			if updErr := h.taskRepo.UpdateAgentID(c.Request().Context(), taskID, agent.ID); updErr != nil {
				applog.Infof("[handler] TaskThreadSelectModel error persisting selected model task=%s agent=%s: %v", taskID, agent.ID, updErr)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to persist model selection")
			}
		}
	}

	return c.NoContent(http.StatusNoContent)
}

// TaskThreadSend handles sending a follow-up message in the task thread.
// Uses shared agent selection and streaming response processing from chat_processing.go.
func (h *Handler) TaskThreadSend(c echo.Context) error {
	taskID := c.Param("taskId")
	message := c.FormValue("message")
	agentID := c.FormValue("agent_id")
	sessionID := c.FormValue("attachment_session_id")

	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}

	applog.Infof("[handler] TaskThreadSend task=%s message=%q agent_id=%s", taskID, message, agentID)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
			applog.Infof("[handler] TaskThreadSend error cleaning unpublished attachment session %s for missing task: %v", sessionID, cleanupErr)
		}
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if h.swarmSvc != nil && task.SwarmRole == models.SwarmRoleParent {
		if err := h.swarmSvc.HandleParentFollowup(c.Request().Context(), task.ID, message); err != nil {
			applog.Infof("[handler] TaskThreadSend swarm parent follow-up routing failed task=%s: %v", taskID, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to route swarm follow-up")
		}
		return render(c, http.StatusOK, templ.Join(
			components.TaskThreadQueuedFollowupResponse(message, nil, task.ProjectID),
			components.ChatComposerActionButtonOOB("task-thread-form-primary-action", fmt.Sprintf("/tasks/%s/cancel?composer_stop=1", taskID), false, ""),
		))
	}

	// Check for pending image attachments (for vision-aware agent selection)
	hasImages := hasPendingImages(sessionID)

	// Select agent: prefer form value, fall back to task's assigned agent, then auto-select.
	// "auto" routes through complexity-based auto-selection; explicit IDs route directly.
	agent, err := h.selectAgent(c.Request().Context(), agentID, message, hasImages)
	if err != nil {
		applog.Infof("[handler] TaskThreadSend agent selection error: %v, trying task fallback", err)
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(c.Request().Context(), *task.AgentID)
		}
		if agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no agent available")
		}
	}

	// An explicit composer model selection (a concrete model ID, or an explicit
	// "default" choice) becomes the task's ongoing assigned model, so the composer
	// selector continues to reflect it after this send and on future follow-ups
	// instead of silently reverting to the task's previous default. This matches
	// task assignment semantics where explicit Task.AgentID drives task model
	// selection. "auto" and unset selections are one-off routing for this send
	// only and do not change the task's assignment. Persisting here (rather than
	// only after execution admission) also protects against races with concurrent
	// or queued follow-ups: each queued/direct execution still carries its own
	// explicitly resolved agent.ID independent of this assignment update.
	if agentID != "" && agentID != "auto" && h.taskRepo != nil {
		newAgentID := agent.ID
		if task.AgentID == nil || *task.AgentID != newAgentID {
			if updErr := h.taskRepo.UpdateAgentID(c.Request().Context(), taskID, newAgentID); updErr != nil {
				applog.Infof("[handler] TaskThreadSend error persisting selected model task=%s agent=%s: %v", taskID, newAgentID, updErr)
			} else {
				task.AgentID = &newAgentID
			}
		}
	}

	admission, err := h.admitTaskFollowup(c.Request().Context(), taskFollowupAdmissionRequest{
		Task:                task,
		Agent:               agent,
		Message:             message,
		Source:              models.TaskOriginWeb,
		AttachmentSessionID: sessionID,
		LogPrefix:           "TaskThreadSend",
	})
	if err != nil {
		if admissionErr, ok := err.(*taskFollowupAdmissionError); ok {
			switch admissionErr.Op {
			case taskFollowupAdmissionOpActiveCheck:
				applog.Infof("[handler] TaskThreadSend active execution check failed task=%s: %v", taskID, admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active task turn")
			case taskFollowupAdmissionOpFirstTurnCheck:
				applog.Infof("[handler] TaskThreadSend first-turn state check failed task=%s: %v", taskID, admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check task queue")
			case taskFollowupAdmissionOpQueueUnavailable:
				return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
			case taskFollowupAdmissionOpQueueCreate:
				applog.Infof("[handler] TaskThreadSend error creating queued input: %v", admissionErr.Err)
				if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
					applog.Infof("[handler] TaskThreadSend error cleaning unpublished attachment session %s: %v", sessionID, cleanupErr)
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue follow-up")
			case taskFollowupAdmissionOpDirectAdmission:
				applog.Infof("[handler] TaskThreadSend error admitting execution: %v", admissionErr.Err)
				if cleanupErr := h.cleanupUnpublishedPendingAttachmentSession(c.Request().Context(), sessionID); cleanupErr != nil {
					applog.Infof("[handler] TaskThreadSend error cleaning unpublished attachment session %s after admission failure: %v", sessionID, cleanupErr)
				}
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
			case taskFollowupAdmissionOpSwarmChildRouting:
				applog.Infof("[handler] TaskThreadSend swarm child follow-up routing failed task=%s: %v", taskID, admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to route swarm follow-up")
			}
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
	}
	if admission.Queued != nil {
		queued := admission.Queued
		return render(c, http.StatusOK, components.ChatQueuedInputRowOOBForTask(queued.ID, message, fmt.Sprintf("/tasks/%s/thread/queued/%s/steer", taskID, queued.ID), queued.AttachmentSessionID != "", taskID))
	}
	exec := admission.Execution
	task = admission.Task

	applog.Infof("[handler] TaskThreadSend created followup exec=%s for task=%s agent=%s status=%s", exec.ID, taskID, agent.Name, exec.Status)
	// Handle file attachments if present (same as ChatSend)
	var attachmentContext string
	var imageAttachments []models.Attachment
	var chatAttachments []models.ChatAttachment
	if sessionID != "" {
		applog.Infof("[handler] TaskThreadSend processing attachments for session=%s", sessionID)
		var attErr error
		attachmentContext, imageAttachments, chatAttachments, attErr = h.processAttachmentsWithReturn(c.Request().Context(), sessionID, exec.ID)
		if attErr != nil {
			applog.Infof("[handler] TaskThreadSend error processing attachments: %v", attErr)
			message = message + fmt.Sprintf("\n\n⚠️ Attachment processing error: %v", attErr)
		}
	}

	h.resumeUserStoppedGoalForManualStart(c.Request().Context(), taskID, models.TaskOriginWeb, "")
	h.reactivateAchievedGoalForManualFollowup(c.Request().Context(), taskID, models.TaskOriginWeb, "")

	// Spawn LLM processing goroutine (acquires per-model worker slot in processStreamingResponse).
	// DeferHistoryLoad=true moves the full ListByTaskChronological scan, agent-definition
	// loading, system/goal/personality context building, and worktree resolution out of the
	// HTTP handler and into the background goroutine, eliminating the per-execution O(N) block
	// that caused visible UI hangs on tasks with many prior executions.
	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:            exec.ID,
		TaskID:            taskID,
		Message:           message,
		Agent:             *agent,
		ProjectID:         task.ProjectID,
		ImageAttachments:  imageAttachments,
		IsTaskFollowup:    true,
		InputOrigin:       models.TaskOriginWeb,
		DeferHistoryLoad:  true,
		AttachmentContext: attachmentContext,
		Task:              task,
	}); err != nil {
		c.Response().Header().Set("Retry-After", "30")
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}

	return render(c, http.StatusOK, templ.Join(
		components.TaskThreadFollowupResponse(message, exec.ID, chatAttachments, task.ProjectID),
		components.ChatComposerActionButtonOOB("task-thread-form-primary-action", fmt.Sprintf("/tasks/%s/cancel?composer_stop=1", taskID), true, exec.ID),
	))
}

func (h *Handler) TaskThreadSteer(c echo.Context) error {
	taskID := c.Param("taskId")
	message := strings.TrimSpace(c.FormValue("message"))
	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	active, err := h.execRepo.FindActiveTaskThreadExecution(c.Request().Context(), taskID, "")
	if err != nil {
		applog.Infof("[handler] TaskThreadSteer active execution check failed task=%s: %v", taskID, err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active response")
	}
	if active == nil {
		return echo.NewHTTPError(http.StatusConflict, "no active response to steer; send a normal follow-up instead")
	}
	expectedTurnID := c.FormValue("expected_turn_id")
	if expectedTurnID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "expected turn id is required")
	}
	if expectedTurnID != active.ID {
		return echo.NewHTTPError(http.StatusConflict, "active turn changed; queue the message instead")
	}
	input := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           task.ProjectID,
		TaskID:              taskID,
		RunExecutionID:      active.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              active.ID,
		ExpectedTurnID:      expectedTurnID,
		Content:             message,
		AttachmentSessionID: c.FormValue("attachment_session_id"),
	}
	if err := h.threadInputRepo.CreateSteeringForActiveExecution(c.Request().Context(), input, active.ID); err != nil {
		applog.Infof("[handler] TaskThreadSteer error creating steering input: %v", err)
		if errors.Is(err, repository.ErrExpectedTurnEmpty) {
			return echo.NewHTTPError(http.StatusBadRequest, "expected turn id is required")
		}
		if errors.Is(err, repository.ErrNoActiveTurn) || errors.Is(err, repository.ErrActiveTurnChanged) {
			return echo.NewHTTPError(http.StatusConflict, "active turn changed; queue the message instead")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save steering input")
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputSteered,
			ProjectID:      input.ProjectID,
			TaskID:         input.TaskID,
			ExecID:         input.RunExecutionID,
			Message:        input.Content,
			PendingInputID: input.ID,
			HasAttachments: input.AttachmentSessionID != "",
		})
	}
	return render(c, http.StatusOK, components.ChatSteeringInputRowForTask(input.ID, message, input.AttachmentSessionID != "", taskID))
}

// GetTaskThread returns the task thread view (for polling updates)
func (h *Handler) GetTaskThread(c echo.Context) error {
	taskID := c.Param("taskId")
	ctx := c.Request().Context()
	limit := parseThreadWindowLimit(c.QueryParam("limit"), taskThreadWindowLimitDefault, taskThreadWindowLimitMax)
	beforeExecID := strings.TrimSpace(c.QueryParam("before"))
	isPoll := c.QueryParam("poll") == "1"

	var (
		task *models.Task
		err  error
	)
	if isPoll && beforeExecID == "" {
		task, err = h.taskSvc.GetThreadRenderMetadata(ctx, taskID)
	} else {
		task, err = h.taskSvc.GetByID(ctx, taskID)
	}
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	executions, hasEarlier, err := h.loadTaskThreadExecutionWindow(ctx, taskID, beforeExecID, limit)
	if err != nil {
		applog.Infof("[handler] GetTaskThread error loading executions: %v", err)
		executions = []models.Execution{}
		hasEarlier = false
	}
	agents, _ := h.llmConfigRepo.ListBadgeOptions(ctx)

	chatAttachmentsByExec := h.loadChatAttachmentsForExecutions(ctx, executions, "GetTaskThread")

	pendingInputs := []models.ThreadInput{}
	if h.threadInputRepo != nil {
		if inputs, inputErr := h.threadInputRepo.ListPendingForTask(ctx, taskID); inputErr == nil {
			pendingInputs = inputs
		} else {
			applog.Infof("[handler] GetTaskThread error loading pending inputs: %v", inputErr)
		}
	}

	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && h.agentRepo != nil {
		if ad, adErr := h.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
		}
	}

	if beforeExecID != "" {
		return render(c, http.StatusOK, components.TaskThreadEarlierMessages(task, executions, chatAttachmentsByExec, hasEarlier, limit))
	}

	renderTask := h.taskThreadRenderTaskWithEffectiveAgent(ctx, task, agents)
	if isPoll {
		renderTask = taskThreadPollRenderTaskWithExecutionPrompt(renderTask, executions, hasEarlier)
		preservedExecIDs := make(map[string]bool)
		for index, id := range strings.Split(c.QueryParam("preserved_exec_ids"), ",") {
			if index >= taskThreadWindowLimitMax {
				break
			}
			id = strings.TrimSpace(id)
			if id != "" && len(id) <= 128 {
				preservedExecIDs[id] = true
			}
		}
		return render(c, http.StatusOK, components.TaskThreadPollView(renderTask, executions, agents, agentDef, chatAttachmentsByExec, pendingInputs, hasEarlier, limit, preservedExecIDs))
	}
	return render(c, http.StatusOK, components.TaskThreadView(renderTask, executions, agents, agentDef, chatAttachmentsByExec, pendingInputs, hasEarlier, limit))
}

func (h *Handler) taskThreadRenderTaskWithEffectiveAgent(ctx context.Context, task *models.Task, agents []models.LLMConfig) *models.Task {
	if task == nil || task.AgentID != nil {
		return task
	}
	resolvedID := ""
	if h.projectRepo != nil && strings.TrimSpace(task.ProjectID) != "" {
		defaultAgentConfigID, err := h.projectRepo.GetDefaultAgentConfigID(ctx, task.ProjectID)
		if err == nil && defaultAgentConfigID != nil {
			candidateID := strings.TrimSpace(*defaultAgentConfigID)
			if taskThreadAgentIDInBadgeOptions(agents, candidateID) {
				resolvedID = candidateID
			}
		}
	}
	if resolvedID == "" {
		for _, agent := range agents {
			if agent.IsDefault && strings.TrimSpace(agent.ID) != "" {
				resolvedID = agent.ID
				break
			}
		}
	}
	if resolvedID == "" {
		return task
	}
	renderTask := *task
	renderTask.AgentID = &resolvedID
	return &renderTask
}

func taskThreadAgentIDInBadgeOptions(agents []models.LLMConfig, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, agent := range agents {
		if agent.ID == id {
			return true
		}
	}
	return false
}

func taskThreadPollRenderTaskWithExecutionPrompt(task *models.Task, executions []models.Execution, hasEarlier bool) *models.Task {
	if task == nil || task.Prompt != "" || hasEarlier {
		return task
	}
	for _, exec := range executions {
		if !exec.IsFollowup && exec.PromptSent != "" {
			renderTask := *task
			renderTask.Prompt = exec.PromptSent
			return &renderTask
		}
	}
	return task
}

// TaskThreadPendingInputs returns the current pending-inputs composer fragment for a task.
// Called by the task thread page on SSE reconnect to reconcile any steering/queued rows
// missed while the tab was hidden. The server-side query excludes prepared/in-flight steering
// rows (expected_turn_id=NULL) so a stale "Steering pending" row is cleanly replaced.
func (h *Handler) TaskThreadPendingInputs(c echo.Context) error {
	taskID := c.Param("taskId")
	if taskID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task id required")
	}
	pendingInputs := []models.ThreadInput{}
	if h.threadInputRepo != nil {
		if inputs, inputErr := h.threadInputRepo.ListPendingForTask(c.Request().Context(), taskID); inputErr == nil {
			pendingInputs = inputs
		}
	}
	return render(c, http.StatusOK, components.ChatComposerQueuedInputRowsForTask(pendingInputs, func(input models.ThreadInput) string {
		return fmt.Sprintf("/tasks/%s/thread/queued/%s/steer", taskID, input.ID)
	}, taskID))
}

func (h *Handler) loadTaskExecutionWindow(ctx context.Context, taskID, beforeExecID string, limit int) ([]models.Execution, bool, error) {
	queryLimit := limit + 1
	var rows []models.Execution
	var err error
	if beforeExecID != "" {
		rows, err = h.execRepo.ListByTaskChronologicalBefore(ctx, taskID, beforeExecID, queryLimit)
	} else {
		rows, err = h.execRepo.ListByTaskChronologicalLimit(ctx, taskID, queryLimit)
	}
	if err != nil {
		return nil, false, err
	}
	visible, hasEarlier := trimExecutionWindow(rows, limit)
	return visible, hasEarlier, nil
}

func (h *Handler) loadTaskExecutionHistoryWindow(ctx context.Context, taskID, beforeExecID string, limit int) ([]models.Execution, bool, error) {
	visible, hasOlder, err := h.loadTaskExecutionWindow(ctx, taskID, beforeExecID, limit)
	if err != nil {
		return nil, false, err
	}
	for i, j := 0, len(visible)-1; i < j; i, j = i+1, j-1 {
		visible[i], visible[j] = visible[j], visible[i]
	}
	return visible, hasOlder, nil
}

func (h *Handler) loadTaskThreadExecutionWindow(ctx context.Context, taskID, beforeExecID string, limit int) ([]models.Execution, bool, error) {
	return h.loadTaskExecutionWindow(ctx, taskID, beforeExecID, limit)
}

func (h *Handler) GetTaskThreadExecutionFragment(c echo.Context) error {
	taskID := c.Param("taskId")
	execID := c.Param("execId")
	if taskID == "" || execID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task and execution are required")
	}

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	exec, err := h.execRepo.GetByID(c.Request().Context(), execID)
	if err != nil {
		return err
	}
	if exec == nil || exec.TaskID != taskID {
		return echo.NewHTTPError(http.StatusNotFound, "execution not found")
	}

	attachments := []models.ChatAttachment{}
	if h.chatAttachmentRepo != nil {
		byExec, attErr := h.chatAttachmentRepo.ListByExecutionIDs(c.Request().Context(), []string{execID})
		if attErr != nil {
			applog.Infof("[handler] GetTaskThreadExecutionFragment error loading attachments exec=%s: %v", execID, attErr)
		} else {
			attachments = byExec[execID]
		}
	}
	return render(c, http.StatusOK, components.TaskThreadFollowupResponse(exec.PromptSent, exec.ID, attachments, task.ProjectID))
}
