package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// UpdateTaskAutoMerge toggles auto-merge for a task.
func (h *Handler) UpdateTaskAutoMerge(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	autoMerge := c.FormValue("auto_merge") == "on" || c.FormValue("auto_merge") == "true"
	targetBranch := c.FormValue("merge_target_branch")
	if targetBranch == "" {
		targetBranch = task.MergeTargetBranch
	}

	if err := h.taskRepo.UpdateAutoMerge(c.Request().Context(), taskID, autoMerge, targetBranch); err != nil {
		log.Printf("[handler] UpdateTaskAutoMerge error: %v", err)
		return err
	}

	task.AutoMerge = autoMerge
	task.MergeTargetBranch = targetBranch

	// Re-fetch and return the worktree info fragment
	return h.renderWorktreeInfo(c, task)
}

// MergeTaskBranch manually merges a task's worktree branch to target.
func (h *Handler) MergeTaskBranch(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	if task.WorktreeBranch == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task has no worktree branch")
	}
	if c.FormValue("merge_source") == "changes_tab" && !h.isTaskChangesMergeOptionsEnabled() {
		return echo.NewHTTPError(http.StatusForbidden, "task changes merge options are disabled")
	}

	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	// Get the repo path from the project
	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}

	mergeType := c.FormValue("merge_type")
	if mergeType == "" {
		mergeType = "merge"
	}

	result, mergeErr := h.worktreeSvc.MergeBranch(c.Request().Context(), task, project.RepoPath, mergeType)
	if mergeErr != nil {
		log.Printf("[handler] MergeTaskBranch error: %v", mergeErr)
		errMessage := "Local merge failed"
		if result != nil && result.ErrorMessage != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", result.ErrorMessage)
		} else if mergeErr.Error() != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", mergeErr.Error())
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		return c.String(http.StatusBadRequest, errMessage)
	}

	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		if isHTMX(c) {
			setHTMXToast(c, "Local merge has conflicts. Resolve conflicts or abort merge.", "failed")
		}
		// Conflicts detected - refresh the view to show conflict status
		task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
		return h.renderWorktreeInfo(c, task)
	}

	// Success - refresh task data and trigger changes tab refresh
	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)

	// Set response headers to trigger changes tab refresh and show success message
	targetBranch := task.MergeTargetBranch
	if targetBranch == "" && project != nil && project.RepoPath != "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		targetBranch = "main"
	}
	c.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"refreshChanges": true, "showToast": {"message": "Merged locally into %s", "type": "success", "taskId": "%s"}}`, targetBranch, task.ID))

	return h.renderWorktreeInfo(c, task)
}

// CreateTaskPullRequest creates or reuses a pull request for a task worktree branch.
func (h *Handler) CreateTaskPullRequest(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		setHTMXToast(c, "Task not found", "failed")
		return c.NoContent(http.StatusNoContent)
	}
	if task.WorktreeBranch == "" {
		setHTMXToast(c, "Task has no worktree branch", "failed")
		return c.NoContent(http.StatusNoContent)
	}
	if h.githubSvc == nil {
		setHTMXToastWithLink(c, "GitHub integration is not configured", "failed", "/channels", "Open Channels")
		return c.NoContent(http.StatusNoContent)
	}
	if h.taskPullRequestRepo == nil {
		setHTMXToast(c, "Task pull request repository not available", "failed")
		return c.NoContent(http.StatusNoContent)
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		setHTMXToast(c, "Project has no repository path configured", "failed")
		return c.NoContent(http.StatusNoContent)
	}

	existingPR, err := h.taskPullRequestRepo.GetByTaskID(c.Request().Context(), taskID)
	if err != nil {
		setHTMXToast(c, "Failed to check existing pull request", "failed")
		return c.NoContent(http.StatusNoContent)
	}
	if existingPR != nil {
		setHTMXToast(c, fmt.Sprintf("GitHub PR already exists (#%d)", existingPR.PRNumber), "success")
		return h.GetTaskChanges(c)
	}

	repoRef, err := h.githubSvc.ResolveRepo(c.Request().Context(), project.RepoURL, project.RepoPath)
	if err != nil {
		setHTMXToast(c, fmt.Sprintf("Failed to resolve repository: %v", err), "failed")
		return c.NoContent(http.StatusNoContent)
	}

	if task.WorktreePath != "" {
		if err := service.CommitWorktreeChanges(task.WorktreePath, fmt.Sprintf("Task updates: %s", task.Title)); err != nil {
			setHTMXToast(c, fmt.Sprintf("Failed to commit changes: %v", err), "failed")
			return c.NoContent(http.StatusNoContent)
		}
	}

	if err := h.githubSvc.PushBranch(c.Request().Context(), project.RepoPath, task.WorktreePath, task.WorktreeBranch, repoRef); err != nil {
		setHTMXToast(c, fmt.Sprintf("Failed to push branch: %v", err), "failed")
		return c.NoContent(http.StatusNoContent)
	}

	foundPR, err := h.githubSvc.FindPullRequestByBranch(c.Request().Context(), repoRef, task.WorktreeBranch)
	if err != nil {
		setHTMXToast(c, fmt.Sprintf("Failed to find pull request: %v", err), "failed")
		return c.NoContent(http.StatusNoContent)
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		targetBranch = "main"
	}

	pr := foundPR
	if pr == nil {
		body := fmt.Sprintf("Automated pull request for task `%s`.\n\nTask title: %s\nTask ID: %s\n\nGenerated by OpenVibely.", task.Title, task.Title, task.ID)
		pr, err = h.githubSvc.CreatePullRequest(c.Request().Context(), repoRef, service.GitHubCreatePullRequestRequest{
			Title: task.Title,
			Body:  body,
			Head:  task.WorktreeBranch,
			Base:  targetBranch,
			Draft: false,
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "pull request already exists") {
				retryPR, findErr := h.githubSvc.FindPullRequestByBranch(c.Request().Context(), repoRef, task.WorktreeBranch)
				if findErr == nil && retryPR != nil {
					pr = retryPR
				} else {
					setHTMXToast(c, fmt.Sprintf("Failed to create pull request: %v", err), "failed")
					return c.NoContent(http.StatusNoContent)
				}
			} else {
				setHTMXToast(c, fmt.Sprintf("Failed to create pull request: %v", err), "failed")
				return c.NoContent(http.StatusNoContent)
			}
		}
	}

	record := &models.TaskPullRequest{
		TaskID:   task.ID,
		PRNumber: pr.Number,
		PRURL:    pr.URL,
		PRState:  pr.State,
	}
	if err := h.taskPullRequestRepo.Upsert(c.Request().Context(), record); err != nil {
		setHTMXToast(c, "Failed to save pull request record", "failed")
		return c.NoContent(http.StatusNoContent)
	}

	setHTMXToast(c, fmt.Sprintf("GitHub PR created (#%d)", pr.Number), "success")
	return h.GetTaskChanges(c)
}

// ResolveTaskConflicts triggers AI-assisted conflict resolution.
func (h *Handler) ResolveTaskConflicts(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}

	result, resolveErr := h.worktreeSvc.ResolveConflictsWithAI(c.Request().Context(), task, project.RepoPath)
	if resolveErr != nil {
		log.Printf("[handler] ResolveTaskConflicts error: %v", resolveErr)
	}

	if result != nil && !result.Success {
		// Abort the merge
		service.AbortMerge(project.RepoPath)
	}

	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
	return h.renderWorktreeInfo(c, task)
}

// AbortTaskMerge aborts an in-progress merge for a task.
func (h *Handler) AbortTaskMerge(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}

	service.AbortMerge(project.RepoPath)
	_ = h.taskRepo.UpdateMergeStatus(c.Request().Context(), taskID, models.MergeStatusPending)

	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
	return h.renderWorktreeInfo(c, task)
}

// CleanupTaskWorktree removes the worktree for a task.
func (h *Handler) CleanupTaskWorktree(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}

	deleteBranch := c.FormValue("delete_branch") == "on" || c.FormValue("delete_branch") == "true"
	if cleanErr := h.worktreeSvc.CleanupWorktree(c.Request().Context(), task, project.RepoPath, deleteBranch); cleanErr != nil {
		log.Printf("[handler] CleanupTaskWorktree error: %v", cleanErr)
		return echo.NewHTTPError(http.StatusInternalServerError, cleanErr.Error())
	}

	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
	return h.renderWorktreeInfo(c, task)
}

// GetTaskWorktreeInfo returns the worktree info panel for a task (HTMX partial).
func (h *Handler) GetTaskWorktreeInfo(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	return h.renderWorktreeInfo(c, task)
}

func (h *Handler) renderWorktreeInfo(c echo.Context, task *models.Task) error {
	// Resolve project repo path for file stats
	var fileStats []service.WorktreeFileStat
	if task.WorktreeBranch != "" {
		project, _ := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
		if project != nil && project.RepoPath != "" {
			targetBranch := task.MergeTargetBranch
			if targetBranch == "" {
				targetBranch = service.GetDefaultBranch(project.RepoPath)
			}
			fileStats = service.GetWorktreeFileStats(project.RepoPath, task.WorktreeBranch, targetBranch)
		}
	}
	return render(c, http.StatusOK, pages.WorktreeInfoPanel(task, fileStats))
}

// GetTaskChangesWorktree returns changes tab showing worktree-specific diff.
func (h *Handler) GetTaskChangesWorktree(c echo.Context) error {
	taskID := c.Param("taskId")

	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// If task has a worktree branch, show worktree diff instead of execution diff
	if task.WorktreeBranch != "" {
		project, _ := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
		if project != nil && project.RepoPath != "" {
			targetBranch := task.MergeTargetBranch
			if targetBranch == "" {
				targetBranch = service.GetDefaultBranch(project.RepoPath)
			}
			diffOutput := service.GetWorktreeDiff(project.RepoPath, task.WorktreeBranch, targetBranch)
			fileStats := service.GetWorktreeFileStats(project.RepoPath, task.WorktreeBranch, targetBranch)

			var reviewComments []models.ReviewComment
			if h.reviewCommentRepo != nil {
				reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
			}
			var taskPR *models.TaskPullRequest
			if h.taskPullRequestRepo != nil {
				taskPR, _ = h.taskPullRequestRepo.GetByTaskID(c.Request().Context(), taskID)
			}

			return render(c, http.StatusOK, pages.TaskChangesWorktreeContent(
				diffOutput, task, fileStats, reviewComments, taskPR, h.isTaskChangesMergeOptionsEnabled(),
			))
		}
	}

	// Fallback to execution-based diff
	executions, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	var reviewComments []models.ReviewComment
	if h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	}
	return render(c, http.StatusOK, pages.TaskChangesContent(executions, task.ID, reviewComments))
}

// UpdateWorktreeSettings updates global worktree settings.
func (h *Handler) UpdateWorktreeSettings(c echo.Context) error {
	ctx := c.Request().Context()

	autoMerge := c.FormValue("worktree_auto_merge")
	if autoMerge != "" {
		h.settingsRepo.Set(ctx, "worktree_auto_merge", autoMerge)
	}

	mergeTarget := c.FormValue("worktree_merge_target")
	if mergeTarget != "" {
		h.settingsRepo.Set(ctx, "worktree_merge_target", mergeTarget)
	}

	cleanup := c.FormValue("worktree_cleanup")
	if cleanup != "" {
		h.settingsRepo.Set(ctx, "worktree_cleanup", cleanup)
	}

	c.Response().Header().Set("HX-Trigger", fmt.Sprintf(`{"showToast": {"message": "Worktree settings saved", "type": "success"}}`))
	return c.NoContent(http.StatusOK)
}
