package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	llmworkflow "github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	backlogSortCookieName   = "backlog_sort"
	completedSortCookieName = "completed_sort"
)

type taskSortPreferences struct {
	Backlog   string
	Completed string
}

func getSortPreference(c echo.Context, cookieName string) string {
	if cookie, err := c.Cookie(cookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func getSortPreferences(c echo.Context) taskSortPreferences {
	return taskSortPreferences{
		Backlog:   getSortPreference(c, backlogSortCookieName),
		Completed: getSortPreference(c, completedSortCookieName),
	}
}

func isValidTaskSort(sortBy string) bool {
	switch sortBy {
	case "title_asc", "title_desc", "created_asc", "created_desc", "priority_asc", "priority_desc":
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
		log.Printf("[handler] listAgentDefinitions error: %v", err)
		return nil
	}
	return agentDefs
}

func (h *Handler) renderKanbanBoard(c echo.Context, tasks []models.Task, projectID string, sortPrefs taskSortPreferences, llmModels []models.LLMConfig) error {
	agentDefs := h.listAgentDefinitions(c.Request().Context())
	return render(c, http.StatusOK, components.KanbanBoard(tasks, projectID, sortPrefs.Backlog, sortPrefs.Completed, llmModels, agentDefs))
}

func (h *Handler) ListTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	isHTMX := isHTMX(c)
	htmxTarget := c.Request().Header.Get("HX-Target")
	log.Printf("[handler] ListTasks project=%s htmx=%v target=%s", projectID, isHTMX, htmxTarget)

	// Read sort preferences from cookies
	sortPrefs := getSortPreferences(c)
	if sortPrefs.Backlog != "" || sortPrefs.Completed != "" {
		log.Printf("[handler] ListTasks using sort preferences: backlog=%s completed=%s", sortPrefs.Backlog, sortPrefs.Completed)
	}

	// For kanban-board-only refreshes (SSE, etc.), project_id must be provided
	if isHTMX && htmxTarget != "main-content" {
		if projectID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
		}
		tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			log.Printf("[handler] ListTasks error: %v", err)
			return err
		}
		log.Printf("[handler] ListTasks found %d tasks", len(tasks))
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	// For full page and main-content swaps, default to first project
	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
	if err != nil {
		log.Printf("[handler] ListTasks error: %v", err)
		return err
	}
	log.Printf("[handler] ListTasks found %d tasks", len(tasks))

	project, _ := h.projectSvc.GetByID(c.Request().Context(), projectID)
	agents, _ := h.llmConfigRepo.List(c.Request().Context())
	var agentDefs []models.Agent
	if h.agentRepo != nil {
		agentDefs, _ = h.agentRepo.List(c.Request().Context())
	}

	if isHTMX {
		return render(c, http.StatusOK, pages.TasksContent(project, tasks, agents, agentDefs, sortPrefs.Backlog, sortPrefs.Completed))
	}

	return render(c, http.StatusOK, pages.Tasks(projects, project, tasks, agents, agentDefs, sortPrefs.Backlog, sortPrefs.Completed))
}

func (h *Handler) CreateTask(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	priority, _ := strconv.Atoi(c.FormValue("priority"))
	category := models.TaskCategory(c.FormValue("category"))
	if category == "" {
		category = models.CategoryActive
	}

	// Creating an active task immediately submits it to the worker pool.
	// Block this when no models are configured so tasks do not get stuck queued.
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			log.Printf("[handler] CreateTask model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			log.Printf("[handler] CreateTask blocked: no models configured project=%s title=%q", projectID, c.FormValue("title"))
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
	// Handle optional agent definition selection
	if agentDefID := c.FormValue("agent_definition_id"); agentDefID != "" {
		t.AgentDefinitionID = &agentDefID
	}
	log.Printf("[handler] CreateTask project=%s title=%q category=%s priority=%d tag=%s prompt_len=%d",
		projectID, t.Title, t.Category, t.Priority, t.Tag, len(t.Prompt))

	if err := h.taskSvc.Create(c.Request().Context(), t); err != nil {
		if errors.Is(err, service.ErrDuplicateTask) {
			log.Printf("[handler] CreateTask duplicate title=%q", t.Title)
			return echo.NewHTTPError(http.StatusConflict, "A task with this name already exists in this project")
		}
		log.Printf("[handler] CreateTask error: %v", err)
		return err
	}
	log.Printf("[handler] CreateTask success id=%s", t.ID)

	// If category is scheduled and run_at is provided, create a schedule
	if t.Category == models.CategoryScheduled {
		runAtStr := c.FormValue("run_at")
		if runAtStr != "" {
			// Parse the time in local timezone since the browser sends datetime-local values,
			// then convert to UTC for consistent storage
			runAt, err := time.ParseInLocation("2006-01-02T15:04", runAtStr, time.Local)
			if err != nil {
				log.Printf("[handler] CreateTask schedule parse error: %v", err)
			} else {
				runAt = runAt.UTC()
				repeatInterval, _ := strconv.Atoi(c.FormValue("repeat_interval"))
				if repeatInterval < 1 {
					repeatInterval = 1
				}
				sched := &models.Schedule{
					TaskID:         t.ID,
					RunAt:          runAt,
					RepeatType:     models.RepeatType(c.FormValue("repeat_type")),
					RepeatInterval: repeatInterval,
					Enabled:        true,
				}
					if sched.RepeatType == "" {
						sched.RepeatType = models.RepeatDaily
					}
				// For recurring schedules with a past RunAt, compute the next future occurrence immediately
				if sched.RepeatType != models.RepeatOnce && !runAt.After(time.Now().UTC()) {
					nextRun := sched.ComputeNextRun(time.Now().UTC())
					if nextRun != nil {
						sched.NextRun = nextRun
					}
				}
				if err := h.scheduleRepo.Create(c.Request().Context(), sched); err != nil {
					log.Printf("[handler] CreateTask schedule create error: %v", err)
				} else {
					log.Printf("[handler] CreateTask schedule created id=%s next_run=%v", sched.ID, sched.NextRun)
				}
			}
		}
	}

	// Handle optional file attachments (multiple files supported)
	form, err := c.MultipartForm()
	if err == nil && form != nil {
		files := form.File["files"]
		if len(files) > 0 {
			// Create task-specific directory
			taskDir := filepath.Join(uploadsDir, t.ID)
			if err := os.MkdirAll(taskDir, 0755); err != nil {
				log.Printf("[handler] CreateTask error creating directory: %v", err)
			} else {
				// Process each file
				uploadedCount := 0
				for _, file := range files {
					// Check file size
					if file.Size > maxUploadSize {
						log.Printf("[handler] CreateTask file %s too large (%d bytes)", file.Filename, file.Size)
						continue // Skip this file but continue with others
					}

					// Open the uploaded file
					src, err := file.Open()
					if err != nil {
						log.Printf("[handler] CreateTask error opening file %s: %v", file.Filename, err)
						continue
					}

					// Save file
					filename := filepath.Base(file.Filename)
					destPath := filepath.Join(taskDir, filename)
					dest, err := os.Create(destPath)
					if err != nil {
						log.Printf("[handler] CreateTask error creating file %s: %v", filename, err)
						src.Close()
						continue
					}

					if _, err := io.Copy(dest, src); err != nil {
						log.Printf("[handler] CreateTask error copying file %s: %v", filename, err)
						src.Close()
						dest.Close()
						os.Remove(destPath)
						continue
					}
					src.Close()
					dest.Close()

					// Detect media type from file header
					mediaType := file.Header.Get("Content-Type")
					if mediaType == "" {
						mediaType = "application/octet-stream"
					}

					// Create attachment record
					attachment := &models.Attachment{
						TaskID:    t.ID,
						FileName:  filename,
						FilePath:  destPath,
						MediaType: mediaType,
						FileSize:  file.Size,
					}

					if err := h.attachmentRepo.Create(c.Request().Context(), attachment); err != nil {
						log.Printf("[handler] CreateTask error creating attachment for %s: %v", filename, err)
						os.Remove(destPath)
						continue
					}

					log.Printf("[handler] CreateTask attachment created id=%s file=%s size=%d", attachment.ID, filename, file.Size)
					uploadedCount++
				}

				if uploadedCount > 0 {
					log.Printf("[handler] CreateTask completed: %d/%d attachments uploaded", uploadedCount, len(files))
				}
			}
		}
	}

	// If created from the schedule page, return the updated schedule content
	if c.QueryParam("from") == "schedule" {
		project, _ := h.projectSvc.GetByID(c.Request().Context(), projectID)
		scheduledTasks, _ := h.taskSvc.GetTasksWithSchedulesByProject(c.Request().Context(), projectID)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		weekOffset := 0
		if weekParam := c.QueryParam("week"); weekParam != "" {
			if w, err := strconv.Atoi(weekParam); err == nil {
				weekOffset = w
			}
		}
		return render(c, http.StatusOK, pages.ScheduleContent(project, scheduledTasks, weekOffset, agents))
	}

	// Return the full kanban board
	sortPrefs := getSortPreferences(c)
	tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
	agents, _ := h.llmConfigRepo.List(c.Request().Context())
	return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
}

func (h *Handler) GetTask(c echo.Context) error {
	taskID := c.Param("taskId")
	isHTMX := isHTMX(c)
	log.Printf("[handler] GetTask id=%s htmx=%v", taskID, isHTMX)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		log.Printf("[handler] GetTask error: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[handler] GetTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	schedules, _ := h.scheduleRepo.ListByTask(c.Request().Context(), taskID)
	agents, _ := h.llmConfigRepo.List(c.Request().Context())
	attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
	var agentDefs []models.Agent
	if h.agentRepo != nil {
		agentDefs, _ = h.agentRepo.List(c.Request().Context())
	}
	var reviewComments []models.ReviewComment
	if h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	}
	log.Printf("[handler] GetTask id=%s executions=%d schedules=%d attachments=%d", taskID, len(executions), len(schedules), len(attachments))

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
	// Migrate old "history" tab param to "chat"
	if defaultTab == "history" {
		defaultTab = "chat"
	}
	log.Printf("[handler] GetTask id=%s defaultTab=%s", taskID, defaultTab)

	// HTMX request: return just the task detail content partial
	if isHTMX {
		return render(c, http.StatusOK, pages.TaskDetailContent(task, executions, schedules, agents, agentDefs, attachments, defaultTab, reviewComments))
	}

	// Full page load: wrap in layout
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.TaskDetailPage(projects, task, executions, schedules, agents, agentDefs, attachments, defaultTab, reviewComments))
}

// GetTaskExecutions returns just the execution history for a task (used for polling updates)
func (h *Handler) GetTaskExecutions(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	executions, _ := h.execRepo.ListByTask(c.Request().Context(), taskID)

	return render(c, http.StatusOK, components.TaskExecutionHistory(task, executions))
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

	executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)

	return render(c, http.StatusOK, pages.TaskDetailMetrics(task, executions))
}

func latestNonEmptyDiff(executions []models.Execution) string {
	for i := len(executions) - 1; i >= 0; i-- {
		if executions[i].DiffOutput != "" {
			return executions[i].DiffOutput
		}
	}
	return ""
}

// resolveTaskChangesDiffOutput resolves the diff payload used by the Changes UI.
// It mirrors GetTaskChanges behavior so per-file lazy loads match full-page output.
func (h *Handler) resolveTaskChangesDiffOutput(ctx context.Context, task *models.Task) string {
	if task == nil {
		return ""
	}

	// Worktree tasks can use live git diff or preserved execution diff.
	if task.WorktreeBranch != "" {
		executions, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)

		// For merged tasks, only preserved execution diff is available.
		if task.MergeStatus == models.MergeStatusMerged {
			return latestNonEmptyDiff(executions)
		}

		// For unmerged tasks with an existing worktree, prefer live diff.
		if task.WorktreePath != "" {
			if _, err := os.Stat(task.WorktreePath); err == nil {
				project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
				if project != nil && project.RepoPath != "" {
					targetBranch := task.MergeTargetBranch
					if targetBranch == "" {
						targetBranch = service.GetDefaultBranch(project.RepoPath)
					}
					var diffOutput string
					if task.Status == models.StatusRunning || task.Status == models.StatusQueued {
						diffOutput = service.GetWorktreeDiffWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, task.WorktreePath)
					} else {
						diffOutput = service.GetWorktreeDiff(project.RepoPath, task.WorktreeBranch, targetBranch)
					}
					if strings.TrimSpace(diffOutput) == "" &&
						task.Status != models.StatusRunning &&
						task.Status != models.StatusQueued &&
						service.IsBranchMerged(project.RepoPath, task.WorktreeBranch, targetBranch) {
						return latestNonEmptyDiff(executions)
					}
					return diffOutput
				}
			}
		}

		// Worktree is gone/unavailable, fall back to preserved diff.
		return latestNonEmptyDiff(executions)
	}

	// Non-worktree tasks use execution-based diff.
	executions, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
	return latestNonEmptyDiff(executions)
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

	// If task has a worktree branch, show worktree-specific diff
	// For merged tasks, show the preserved diff from execution (live diff would be empty)
	// For pending/conflict tasks, show live diff if worktree still exists
	if task.WorktreeBranch != "" {
		executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
		var reviewComments []models.ReviewComment
		if h.reviewCommentRepo != nil {
			reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
		}
		var taskPR *models.TaskPullRequest
		if h.taskPullRequestRepo != nil {
			taskPR, _ = h.taskPullRequestRepo.GetByTaskID(c.Request().Context(), taskID)
		}

		// If merged, always show preserved diff from execution (live diff would be empty)
		if task.MergeStatus == models.MergeStatusMerged {
			diffOutput := latestNonEmptyDiff(executions)
			return render(c, http.StatusOK, pages.TaskChangesWorktreeContent(
				diffOutput, task, nil, reviewComments, taskPR, h.isTaskChangesMergeOptionsEnabled(),
			))
		}

		// For unmerged tasks, show live diff if worktree still exists
		if task.WorktreePath != "" {
			if _, err := os.Stat(task.WorktreePath); err == nil {
				project, _ := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
				if project != nil && project.RepoPath != "" {
					targetBranch := task.MergeTargetBranch
					if targetBranch == "" {
						targetBranch = service.GetDefaultBranch(project.RepoPath)
					}
					// For running/queued tasks, include uncommitted changes for real-time visibility
					var diffOutput string
					if task.Status == models.StatusRunning || task.Status == models.StatusQueued {
						diffOutput = service.GetWorktreeDiffWithUncommitted(project.RepoPath, task.WorktreeBranch, targetBranch, task.WorktreePath)
					} else {
						diffOutput = service.GetWorktreeDiff(project.RepoPath, task.WorktreeBranch, targetBranch)
					}
					fileStats := service.GetWorktreeFileStats(project.RepoPath, task.WorktreeBranch, targetBranch)
					if strings.TrimSpace(diffOutput) == "" &&
						task.Status != models.StatusRunning &&
						task.Status != models.StatusQueued &&
						service.IsBranchMerged(project.RepoPath, task.WorktreeBranch, targetBranch) {
						if preservedDiff := latestNonEmptyDiff(executions); preservedDiff != "" {
							diffOutput = preservedDiff
							fileStats = nil
						}
					}

					return render(c, http.StatusOK, pages.TaskChangesWorktreeContent(
						diffOutput, task, fileStats, reviewComments, taskPR, h.isTaskChangesMergeOptionsEnabled(),
					))
				}
			}
		}

		// Fallback: worktree existed but is gone, show preserved diff
		for i := len(executions) - 1; i >= 0; i-- {
			if executions[i].DiffOutput != "" {
				return render(c, http.StatusOK, pages.TaskChangesWorktreeContent(
					executions[i].DiffOutput, task, nil, reviewComments, taskPR, h.isTaskChangesMergeOptionsEnabled(),
				))
			}
		}
	}

	// Fallback to execution-based diff (non-worktree tasks)
	executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	var reviewComments []models.ReviewComment
	if h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	}

	return render(c, http.StatusOK, pages.TaskChangesContent(executions, task.ID, reviewComments))
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
	if reviewMode && h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	}

	diffOutput := h.resolveTaskChangesDiffOutput(c.Request().Context(), task)
	return render(c, http.StatusOK, components.LoadDiffFileCard(diffOutput, fileIndex, view, taskID, reviewComments, reviewMode))
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

	var reviewComments []models.ReviewComment
	if h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	}

	component := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, `<div id="diff-viewer-container">`); err != nil {
			return err
		}
		if err := components.DiffViewerWithReview(diffOutput, task.ID, reviewComments).Render(ctx, w); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</div>`)
		return err
	})

	return render(c, http.StatusOK, component)
}

func (h *Handler) UpdateTask(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] UpdateTask id=%s", taskID)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		log.Printf("[handler] UpdateTask fetch error: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[handler] UpdateTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	oldCategory := task.Category
	newCategory := models.TaskCategory(c.FormValue("category"))
	if oldCategory != newCategory && newCategory == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			log.Printf("[handler] UpdateTask model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			log.Printf("[handler] UpdateTask blocked: no models configured task=%s", taskID)
			return noModelsConfiguredResponse(c)
		}
	}

	task.Title = c.FormValue("title")
	task.Category = newCategory
	task.Prompt = c.FormValue("prompt")
	task.Tag = models.TaskTag(c.FormValue("tag"))
	if p, err := strconv.Atoi(c.FormValue("priority")); err == nil {
		task.Priority = p
	}

	// Handle optional agent (LLM config) selection
	if agentID := c.FormValue("agent_id"); agentID != "" {
		task.AgentID = &agentID
	} else {
		task.AgentID = nil
	}
	// Handle optional agent definition selection
	if agentDefID := c.FormValue("agent_definition_id"); agentDefID != "" {
		task.AgentDefinitionID = &agentDefID
	} else {
		task.AgentDefinitionID = nil
	}

	// Handle auto-merge settings — if the hidden sentinel is present, the edit form
	// was submitted and we always update (unchecked checkbox sends no value).
	if c.FormValue("auto_merge_present") != "" {
		task.AutoMerge = c.FormValue("auto_merge") == "on" || c.FormValue("auto_merge") == "true"
	}
	if targetBranch := c.FormValue("merge_target_branch"); targetBranch != "" {
		task.MergeTargetBranch = targetBranch
	}

	log.Printf("[handler] UpdateTask id=%s title=%q category=%s->%s tag=%s", taskID, task.Title, oldCategory, newCategory, task.Tag)
	if err := h.taskSvc.Update(c.Request().Context(), task); err != nil {
		if errors.Is(err, service.ErrDuplicateTask) {
			log.Printf("[handler] UpdateTask duplicate title=%q", task.Title)
			return echo.NewHTTPError(http.StatusConflict, "A task with this name already exists in this project")
		}
		log.Printf("[handler] UpdateTask error: %v", err)
		return err
	}

	// Handle file uploads if present (multipart form)
	if form, err := c.MultipartForm(); err == nil && form != nil {
		if files := form.File["files"]; len(files) > 0 {
			h.processTaskFileUploads(c.Request().Context(), taskID, files)
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
				log.Printf("[handler] UpdateTask error deleting attachment %s: %v", attID, err)
				continue
			}
			os.Remove(att.FilePath)
			log.Printf("[handler] UpdateTask removed attachment %s from task %s", attID, taskID)
		}
	}

	// If category changed to Active, reset status and auto-submit (same as drag & drop behavior)
	if oldCategory != newCategory && newCategory == models.CategoryActive {
		log.Printf("[handler] UpdateTask category changed to Active, resetting status and auto-submitting id=%s", taskID)
		if err := h.taskSvc.UpdateStatus(c.Request().Context(), taskID, models.StatusPending); err != nil {
			log.Printf("[handler] UpdateTask error resetting status: %v", err)
			return err
		}
	}

	log.Printf("[handler] UpdateTask success id=%s", taskID)

	// Re-fetch updated task data for rendering
	if isHTMX(c) {
		task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
		executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
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
		return render(c, http.StatusOK, pages.TaskDetailContent(task, executions, schedules, agents, adefs, attachments, "details", rc))
	}

	return c.Redirect(http.StatusSeeOther, "/tasks/"+task.ID)
}

// processTaskFileUploads handles file uploads during task update.
// Saves files to uploads/{taskID}/ and creates attachment records.
func (h *Handler) processTaskFileUploads(ctx context.Context, taskID string, files []*multipart.FileHeader) {
	taskDir := filepath.Join(uploadsDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		log.Printf("[handler] processTaskFileUploads error creating directory: %v", err)
		return
	}

	for _, file := range files {
		if file.Size > maxUploadSize {
			log.Printf("[handler] processTaskFileUploads file %s too large (%d bytes)", file.Filename, file.Size)
			continue
		}

		src, err := file.Open()
		if err != nil {
			log.Printf("[handler] processTaskFileUploads error opening %s: %v", file.Filename, err)
			continue
		}

		filename := filepath.Base(file.Filename)
		destPath := filepath.Join(taskDir, filename)
		dest, err := os.Create(destPath)
		if err != nil {
			log.Printf("[handler] processTaskFileUploads error creating %s: %v", filename, err)
			src.Close()
			continue
		}

		if _, err := io.Copy(dest, src); err != nil {
			log.Printf("[handler] processTaskFileUploads error copying %s: %v", filename, err)
			src.Close()
			dest.Close()
			os.Remove(destPath)
			continue
		}
		src.Close()
		dest.Close()

		mediaType := file.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		att := &models.Attachment{
			TaskID:    taskID,
			FileName:  filename,
			FilePath:  destPath,
			MediaType: mediaType,
			FileSize:  file.Size,
		}
		if err := h.attachmentRepo.Create(ctx, att); err != nil {
			log.Printf("[handler] processTaskFileUploads error creating record for %s: %v", filename, err)
			os.Remove(destPath)
			continue
		}

		log.Printf("[handler] processTaskFileUploads uploaded %s to task %s", filename, taskID)
	}
}

func (h *Handler) DeleteTask(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] DeleteTask task=%s", taskID)

	// Fetch task before deleting to get projectID for kanban board response
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		log.Printf("[handler] DeleteTask fetch error: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[handler] DeleteTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	projectID := task.ProjectID

	if err := h.taskSvc.Delete(c.Request().Context(), taskID); err != nil {
		log.Printf("[handler] DeleteTask error: %v", err)
		return err
	}
	log.Printf("[handler] DeleteTask success id=%s", taskID)

	// If redirect=list (from task detail page), redirect to task list
	if isHTMX(c) && c.QueryParam("redirect") == "list" {
		c.Response().Header().Set("HX-Redirect", "/tasks?project_id="+projectID)
		return c.NoContent(http.StatusOK)
	}

	// Return the full kanban board for HTMX requests (consistent with other task operations)
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			log.Printf("[handler] DeleteTask error listing tasks: %v", err)
			return err
		}
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) RunTask(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] RunTask task=%s", taskID)

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		log.Printf("[handler] RunTask model availability check error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
	}
	if !hasModels {
		log.Printf("[handler] RunTask blocked: no models configured task=%s", taskID)
		return noModelsConfiguredResponse(c)
	}

	if err := h.taskSvc.RunTask(c.Request().Context(), taskID); err != nil {
		log.Printf("[handler] RunTask error: %v", err)
		return err
	}
	log.Printf("[handler] RunTask submitted task=%s to worker pool", taskID)

	// Return no content for HTMX requests — the dialog close handler on each page
	// will refresh relevant content (e.g., kanban board on tasks page)
	if isHTMX(c) {
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks/"+taskID)
}

func (h *Handler) CancelTask(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] CancelTask task=%s", taskID)

	// Fetch task to get projectID for kanban board response
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		log.Printf("[handler] CancelTask fetch error: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[handler] CancelTask not found id=%s", taskID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	projectID := task.ProjectID

	if err := h.taskSvc.CancelTask(c.Request().Context(), taskID); err != nil {
		log.Printf("[handler] CancelTask error: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	log.Printf("[handler] CancelTask cancelled task=%s", taskID)

	// Return the full kanban board for HTMX requests
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			log.Printf("[handler] CancelTask error listing tasks: %v", err)
			return err
		}
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks/"+taskID)
}

func (h *Handler) UpdateTaskCategory(c echo.Context) error {
	taskID := c.Param("taskId")
	category := models.TaskCategory(c.FormValue("category"))
	log.Printf("[handler] UpdateTaskCategory task=%s newCategory=%s", taskID, category)

	// Validate: cannot move to scheduled category unless the task has a schedule
	if category == models.CategoryScheduled {
		schedules, err := h.scheduleRepo.ListByTask(c.Request().Context(), taskID)
		if err != nil {
			log.Printf("[handler] UpdateTaskCategory error checking schedules: %v", err)
			return err
		}
		if len(schedules) == 0 {
			log.Printf("[handler] UpdateTaskCategory rejected: task %s has no schedule", taskID)
			return echo.NewHTTPError(http.StatusBadRequest, "Cannot move task to Scheduled category: task has no schedule. Create a schedule first.")
		}
	}
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			log.Printf("[handler] UpdateTaskCategory model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			log.Printf("[handler] UpdateTaskCategory blocked: no models configured task=%s", taskID)
			return noModelsConfiguredResponse(c)
		}
	}

	if err := h.taskSvc.UpdateCategory(c.Request().Context(), taskID, category); err != nil {
		log.Printf("[handler] UpdateTaskCategory error: %v", err)
		return err
	}
	log.Printf("[handler] UpdateTaskCategory success task=%s -> %s", taskID, category)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), task.ProjectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, task.ProjectID, sortPrefs, agents)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) MoveCompletedActiveToCompleted(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] MoveCompletedActiveToCompleted project=%s", projectID)

	count, err := h.taskSvc.MoveCompletedActiveToCompleted(c.Request().Context())
	if err != nil {
		log.Printf("[handler] MoveCompletedActiveToCompleted error: %v", err)
		return err
	}
	log.Printf("[handler] MoveCompletedActiveToCompleted moved %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) UpdateTaskStatus(c echo.Context) error {
	taskID := c.Param("taskId")
	status := models.TaskStatus(c.FormValue("status"))
	log.Printf("[handler] UpdateTaskStatus task=%s newStatus=%s", taskID, status)

	if err := h.taskSvc.UpdateStatus(c.Request().Context(), taskID, status); err != nil {
		log.Printf("[handler] UpdateTaskStatus error: %v", err)
		return err
	}
	log.Printf("[handler] UpdateTaskStatus success task=%s -> %s", taskID, status)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), task.ProjectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, task.ProjectID, sortPrefs, agents)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) BatchUpdateTaskCategory(c echo.Context) error {
	projectID := c.FormValue("project_id")
	taskIDs := c.FormValue("task_ids")
	category := models.TaskCategory(c.FormValue("category"))
	log.Printf("[handler] BatchUpdateTaskCategory project=%s category=%s task_ids=%s", projectID, category, taskIDs)

	// Validate: if moving to scheduled category, all tasks must have schedules
	if category == models.CategoryScheduled {
		for _, id := range strings.Split(taskIDs, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			schedules, err := h.scheduleRepo.ListByTask(c.Request().Context(), id)
			if err != nil {
				log.Printf("[handler] BatchUpdateTaskCategory error checking schedules for task=%s: %v", id, err)
				return err
			}
			if len(schedules) == 0 {
				log.Printf("[handler] BatchUpdateTaskCategory rejected: task %s has no schedule", id)
				return echo.NewHTTPError(http.StatusBadRequest, "Cannot move tasks to Scheduled category: one or more tasks have no schedule")
			}
		}
	}
	if category == models.CategoryActive {
		hasModels, err := h.hasConfiguredModels(c)
		if err != nil {
			log.Printf("[handler] BatchUpdateTaskCategory model availability check error: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to check model availability")
		}
		if !hasModels {
			log.Printf("[handler] BatchUpdateTaskCategory blocked: no models configured project=%s", projectID)
			return noModelsConfiguredResponse(c)
		}
	}

	for _, id := range strings.Split(taskIDs, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := h.taskSvc.UpdateCategory(c.Request().Context(), id, category); err != nil {
			log.Printf("[handler] BatchUpdateTaskCategory error task=%s: %v", id, err)
			return err
		}
	}
	log.Printf("[handler] BatchUpdateTaskCategory success")

	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handler) DeleteAllCompletedTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] DeleteAllCompletedTasks project=%s", projectID)

	count, err := h.taskSvc.DeleteAllCompleted(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] DeleteAllCompletedTasks error: %v", err)
		return err
	}
	log.Printf("[handler] DeleteAllCompletedTasks deleted %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) DeleteAllBacklogTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] DeleteAllBacklogTasks project=%s", projectID)

	count, err := h.taskSvc.DeleteAllBacklog(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] DeleteAllBacklogTasks error: %v", err)
		return err
	}
	log.Printf("[handler] DeleteAllBacklogTasks deleted %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) ActivateAllBacklogTasks(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] ActivateAllBacklogTasks project=%s", projectID)

	count, err := h.taskSvc.ActivateAllBacklog(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] ActivateAllBacklogTasks error: %v", err)
		return err
	}
	log.Printf("[handler] ActivateAllBacklogTasks activated %d tasks", count)

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) ReorderTask(c echo.Context) error {
	taskID := c.Param("taskId")
	newPosition, err := strconv.Atoi(c.FormValue("position"))
	if err != nil {
		log.Printf("[handler] ReorderTask invalid position: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid position")
	}
	log.Printf("[handler] ReorderTask task=%s newPosition=%d", taskID, newPosition)

	if err := h.taskSvc.ReorderTask(c.Request().Context(), taskID, newPosition); err != nil {
		log.Printf("[handler] ReorderTask error: %v", err)
		return err
	}
	log.Printf("[handler] ReorderTask success task=%s -> position %d", taskID, newPosition)

	// Fetch task to get projectID for kanban board response
	task, _ := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		tasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), task.ProjectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, task.ProjectID, sortPrefs, agents)
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
	log.Printf("[handler] ExecuteBacklogTasks project=%s priority=%d", projectID, priority)

	tasks, submitted, err := h.taskSvc.ExecuteBacklogTasks(c.Request().Context(), projectID, priority)
	if err != nil {
		log.Printf("[handler] ExecuteBacklogTasks error: %v", err)
		return err
	}
	log.Printf("[handler] ExecuteBacklogTasks submitted %d/%d tasks", submitted, len(tasks))

	// Return the full kanban board
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		allTasks, _ := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, allTasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) CountBacklogByPriority(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	log.Printf("[handler] CountBacklogByPriority project=%s", projectID)

	counts, err := h.taskSvc.CountBacklogByPriority(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] CountBacklogByPriority error: %v", err)
		return err
	}

	return c.JSON(http.StatusOK, counts)
}

func (h *Handler) SetBacklogSort(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	sortBy := c.QueryParam("sort")
	log.Printf("[handler] SetBacklogSort project=%s sort=%s", projectID, sortBy)

	if !isValidTaskSort(sortBy) {
		log.Printf("[handler] SetBacklogSort invalid sort: %s", sortBy)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort parameter")
	}

	setTaskSortCookie(c, backlogSortCookieName, sortBy)
	log.Printf("[handler] SetBacklogSort cookie set: %s", sortBy)

	// Return the full kanban board with the new sort order
	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		sortPrefs.Backlog = sortBy
		tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			log.Printf("[handler] SetBacklogSort error: %v", err)
			return err
		}
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) SetCompletedSort(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	sortBy := c.QueryParam("sort")
	log.Printf("[handler] SetCompletedSort project=%s sort=%s", projectID, sortBy)

	if !isValidTaskSort(sortBy) {
		log.Printf("[handler] SetCompletedSort invalid sort: %s", sortBy)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sort parameter")
	}

	setTaskSortCookie(c, completedSortCookieName, sortBy)
	log.Printf("[handler] SetCompletedSort cookie set: %s", sortBy)

	if isHTMX(c) {
		sortPrefs := getSortPreferences(c)
		sortPrefs.Completed = sortBy
		tasks, err := h.taskSvc.ListByProjectWithCategorySorts(c.Request().Context(), projectID, "", sortPrefs.Backlog, sortPrefs.Completed)
		if err != nil {
			log.Printf("[handler] SetCompletedSort error: %v", err)
			return err
		}
		agents, _ := h.llmConfigRepo.List(c.Request().Context())
		return h.renderKanbanBoard(c, tasks, projectID, sortPrefs, agents)
	}

	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) UpdateTaskChainConfig(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] UpdateTaskChainConfig id=%s", taskID)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		log.Printf("[handler] UpdateTaskChainConfig fetch error: %v", err)
		return err
	}
	if task == nil {
		log.Printf("[handler] UpdateTaskChainConfig not found id=%s", taskID)
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

	log.Printf("[handler] UpdateTaskChainConfig id=%s enabled=%v trigger=%s child_agent=%s child_model=%s child_category=%s",
		taskID, enabled, trigger, childAgentID, childModel, childCategory)

	// Update task with new chain config
	if err := task.SetChainConfig(config); err != nil {
		log.Printf("[handler] UpdateTaskChainConfig error serializing config: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chain configuration")
	}

	if err := h.taskSvc.Update(c.Request().Context(), task); err != nil {
		log.Printf("[handler] UpdateTaskChainConfig error updating task: %v", err)
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
				log.Printf("[handler] UpdateTaskChainConfig error creating blocked child: %v", createErr)
			} else {
				log.Printf("[handler] UpdateTaskChainConfig pre-created blocked child id=%s for parent=%s", blockedChild.ID, taskID)
			}
		} else {
			log.Printf("[handler] UpdateTaskChainConfig blocked child already exists id=%s for parent=%s", existing.ID, taskID)
		}
	} else {
		if delErr := h.taskRepo.DeleteBlockedChildrenByParent(c.Request().Context(), taskID); delErr != nil {
			log.Printf("[handler] UpdateTaskChainConfig error deleting blocked children: %v", delErr)
		} else {
			log.Printf("[handler] UpdateTaskChainConfig removed blocked children for parent=%s (chain disabled)", taskID)
		}
	}

	log.Printf("[handler] UpdateTaskChainConfig success id=%s", taskID)

	// Return updated task detail content
	if isHTMX(c) {
		executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
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
		return render(c, http.StatusOK, pages.TaskDetailContent(task, executions, schedules, agents, adefs, attachments, "chaining", rc))
	}

	return c.NoContent(http.StatusOK)
}

// TaskThreadSend handles sending a follow-up message in the task thread.
// Uses shared agent selection and streaming response processing from chat_processing.go.
func (h *Handler) TaskThreadSend(c echo.Context) error {
	taskID := c.Param("taskId")
	message := c.FormValue("message")
	agentID := c.FormValue("agent_id")

	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required")
	}

	log.Printf("[handler] TaskThreadSend task=%s message=%q agent_id=%s", taskID, message, agentID)

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Check for pending image attachments (for vision-aware agent selection)
	sessionID := c.FormValue("attachment_session_id")
	hasImages := hasPendingImages(sessionID)

	// Select agent: prefer form value, fall back to task's assigned agent, then auto-select
	// Uses the shared selectAgent helper for explicit/auto selection, with task-specific fallback
	agent, err := h.selectAgent(c.Request().Context(), agentID, message, hasImages)
	if err != nil {
		log.Printf("[handler] TaskThreadSend agent selection error: %v, trying task fallback", err)
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(c.Request().Context(), *task.AgentID)
		}
		if agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no agent available")
		}
	}

	// Set status to "queued" to indicate the task is waiting for worker capacity.
	// Using "pending" would cause the scheduler to also auto-submit the task to the
	// worker pool, resulting in a duplicate execution race. The "queued" status
	// prevents scheduler interference while signaling to the user that the thread
	// message is queued (the goroutine in processStreamingResponse will update to
	// "running" once worker slots are acquired).
	if task.Status != models.StatusRunning && task.Status != models.StatusQueued {
		log.Printf("[handler] TaskThreadSend setting task=%s status=queued (was %s)", taskID, task.Status)
		if err := h.taskRepo.UpdateStatus(c.Request().Context(), taskID, models.StatusQueued); err != nil {
			log.Printf("[handler] TaskThreadSend error setting status: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update task status")
		}
	}
	// Always move to active category so the task appears in the Active column
	if task.Category != models.CategoryActive {
		log.Printf("[handler] TaskThreadSend moving task=%s to active (was %s)", taskID, task.Category)
		if err := h.taskRepo.UpdateCategory(c.Request().Context(), taskID, models.CategoryActive); err != nil {
			log.Printf("[handler] TaskThreadSend error updating category: %v", err)
		}
	}

	// Create execution record for the follow-up
	exec := &models.Execution{
		TaskID:        taskID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    message,
		IsFollowup:    true,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		log.Printf("[handler] TaskThreadSend error creating execution: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create execution")
	}

	log.Printf("[handler] TaskThreadSend created followup exec=%s for task=%s agent=%s", exec.ID, taskID, agent.Name)

	// Handle file attachments if present (same as ChatSend)
	var attachmentContext string
	var imageAttachments []models.Attachment
	var chatAttachments []models.ChatAttachment
	if sessionID != "" {
		log.Printf("[handler] TaskThreadSend processing attachments for session=%s", sessionID)
		var attErr error
		attachmentContext, imageAttachments, chatAttachments, attErr = h.processAttachmentsWithReturn(c.Request().Context(), sessionID, exec.ID)
		if attErr != nil {
			log.Printf("[handler] TaskThreadSend error processing attachments: %v", attErr)
			message = message + fmt.Sprintf("\n\n⚠️ Attachment processing error: %v", attErr)
		}
	}

	// Load conversation history and build system context
	priorExecs, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	systemContext := buildThreadSystemContext(task.Title, len(priorHistory) > 0, attachmentContext)
	personalityContext := h.getPersonalityContext(c.Request().Context(), task.ProjectID)
	workDir := h.resolveWorktreeWorkDir(c.Request().Context(), task)
	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && h.agentRepo != nil {
		if ad, adErr := h.agentRepo.GetByID(c.Request().Context(), *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
		}
	}

	// Spawn LLM processing goroutine (acquires per-model worker slot in processStreamingResponse)
	go h.processStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           taskID,
		Message:          message,
		Agent:            *agent,
		AgentDefinition:  agentDef,
		ChatHistory:      priorHistory,
		ProjectID:        task.ProjectID,
		SystemContext:    combineContexts(systemContext, personalityContext),
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   true,
		ProcessMarkers:   false,
	})

	return render(c, http.StatusOK, components.TaskThreadFollowupResponse(message, exec.ID, chatAttachments))
}

// GetTaskThread returns the task thread view (for polling updates)
func (h *Handler) GetTaskThread(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	agents, _ := h.llmConfigRepo.List(c.Request().Context())

	// Load chat attachments for all executions in a single query
	execIDs := make([]string, len(executions))
	for i, exec := range executions {
		execIDs[i] = exec.ID
	}
	chatAttachmentsByExec, err := h.chatAttachmentRepo.ListByExecutionIDs(c.Request().Context(), execIDs)
	if err != nil {
		log.Printf("[handler] GetTaskThread error loading attachments: %v", err)
		chatAttachmentsByExec = make(map[string][]models.ChatAttachment)
	}

	return render(c, http.StatusOK, components.TaskThreadView(task, executions, agents, chatAttachmentsByExec))
}
