package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
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
		applog.Infof("[handler] UpdateTaskAutoMerge error: %v", err)
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

	// Get the repo path from the project
	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	h.recoverTaskWorktreeState(c.Request().Context(), task, project)
	if task.WorktreeBranch == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task has no worktree branch")
	}

	// Defense-in-depth: if the branch is already merged into its target,
	// back-fill merge_status and reject the redundant merge request instead
	// of attempting it.
	// Performed before the worktreeSvc check so a stale UI cannot drive
	// duplicate merges even on installs where the worktree service is missing.
	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if len(service.ActiveConflictFiles(project.RepoPath)) > 0 {
		msg := "A merge conflict is already active. Resolve conflicts or abort the merge before trying another merge."
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
		return c.String(http.StatusConflict, msg)
	}
	if targetBranch != "" && service.IsBranchTipMergedInto(project.RepoPath, task.WorktreeBranch, targetBranch) {
		if task.MergeStatus != models.MergeStatusMerged {
			_ = h.taskRepo.UpdateMergeStatus(c.Request().Context(), task.ID, models.MergeStatusMerged)
			task.MergeStatus = models.MergeStatusMerged
		}
		msg := fmt.Sprintf("Branch %s is already merged into %s", task.WorktreeBranch, targetBranch)
		if isHTMX(c) {
			setHTMXToast(c, msg, "info")
		}
		return c.String(http.StatusConflict, msg)
	}

	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	mergeType := c.FormValue("merge_type")
	if mergeType == "" {
		mergeType = "merge"
	}

	fromChangesTab := c.FormValue("merge_source") == "changes_tab"

	result, mergeErr := h.worktreeSvc.MergeBranch(c.Request().Context(), task, project.RepoPath, mergeType)
	if mergeErr != nil {
		applog.Infof("[handler] MergeTaskBranch error: %v", mergeErr)
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
		if fromChangesTab {
			return h.GetTaskChanges(c)
		}
		return h.renderWorktreeInfo(c, task)
	}

	// Success - refresh task data and trigger changes tab refresh
	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)

	// Set response headers to trigger changes tab refresh and show success message
	if targetBranch == "" {
		targetBranch = "main"
	}
	setHTMXToastWithOptionsAndTriggers(c, fmt.Sprintf("Merged locally into %s", targetBranch), "completed", "", "", task.ID, "", "", map[string]any{
		"refreshChanges": true,
	})

	if fromChangesTab {
		return h.GetTaskChanges(c)
	}
	return h.renderWorktreeInfo(c, task)
}

// RebaseTaskBranch rebases a task's worktree branch onto its target branch.
func (h *Handler) RebaseTaskBranch(c echo.Context) error {
	taskID := c.Param("taskId")
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	h.recoverTaskWorktreeState(c.Request().Context(), task, project)
	if task.WorktreeBranch == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "task has no worktree branch")
	}
	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if targetBranch == "" {
		targetBranch = "main"
	}

	if len(service.ActiveConflictFiles(project.RepoPath)) > 0 || task.MergeStatus == models.MergeStatusConflict {
		msg := "A merge conflict is already active. Resolve conflicts or abort the merge before rebasing."
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
		return c.String(http.StatusConflict, msg)
	}

	result, rebaseErr := h.worktreeSvc.RebaseBranch(c.Request().Context(), task, project.RepoPath)
	if rebaseErr != nil {
		applog.Infof("[handler] RebaseTaskBranch error: %v", rebaseErr)
		errMessage := "Rebase failed"
		if result != nil && result.ErrorMessage != "" {
			errMessage = fmt.Sprintf("Rebase failed: %s", result.ErrorMessage)
		} else if rebaseErr.Error() != "" {
			errMessage = fmt.Sprintf("Rebase failed: %s", rebaseErr.Error())
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		return c.String(http.StatusBadRequest, errMessage)
	}

	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		msg := fmt.Sprintf("Rebase onto %s had conflicts and was aborted. Resolve the conflicting files in the task worktree, then try rebase again.", targetBranch)
		if result.ErrorMessage != "" {
			msg = result.ErrorMessage
		}
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
		return h.GetTaskChanges(c)
	}

	if result != nil && result.UpToDate {
		setHTMXToast(c, fmt.Sprintf("Task branch is already up to date with %s", targetBranch), "completed")
	} else {
		setHTMXToast(c, fmt.Sprintf("Rebased task branch onto %s", targetBranch), "completed")
	}
	return h.GetTaskChanges(c)
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
	repoRef, err := h.githubSvc.ResolveRepo(c.Request().Context(), project.RepoURL, project.RepoPath)
	if err != nil {
		setHTMXToast(c, formatTaskPullRequestError(fmt.Errorf("resolving repository: %w", err)), "failed")
		return c.NoContent(http.StatusNoContent)
	}
	if err := service.ConfigureGitHubRepoEndpoint(repoRef, h.githubSvc.GlobalAPIEndpoint(c.Request().Context())); err != nil {
		setHTMXToast(c, err.Error(), "failed")
		return c.NoContent(http.StatusNoContent)
	}

	result, err := service.NewTaskPullRequestService(h.githubSvc, h.taskPullRequestRepo).OpenForTask(c.Request().Context(), project, task, service.OpenTaskPullRequestOptions{
		CommitMessage: h.buildPullRequestPrepCommitMessage(c.Request().Context(), task),
	})
	if err != nil {
		setHTMXToast(c, formatTaskPullRequestError(err), "failed")
		return c.NoContent(http.StatusNoContent)
	}

	if result.ReusedExistingRecord {
		setHTMXToast(c, fmt.Sprintf("GitHub PR already exists (#%d)", result.PullRequest.Number), "success")
	} else if result.Created {
		setHTMXToast(c, fmt.Sprintf("GitHub PR created (#%d)", result.PullRequest.Number), "success")
	} else {
		setHTMXToast(c, fmt.Sprintf("GitHub PR reused (#%d)", result.PullRequest.Number), "success")
	}
	return h.GetTaskChanges(c)
}

func formatTaskPullRequestError(err error) string {
	if err == nil {
		return "Failed to create pull request"
	}
	msg := err.Error()
	replacements := []struct {
		prefix string
		label  string
	}{
		{prefix: "publishing branch:", label: "Failed to publish branch:"},
		{prefix: "finding pull request:", label: "Failed to find pull request:"},
		{prefix: "creating pull request:", label: "Failed to create pull request:"},
		{prefix: "saving pull request record:", label: "Failed to save pull request record:"},
		{prefix: "resolving repository:", label: "Failed to resolve repository:"},
	}
	for _, replacement := range replacements {
		if strings.HasPrefix(msg, replacement.prefix) {
			return strings.TrimSpace(replacement.label + " " + strings.TrimSpace(strings.TrimPrefix(msg, replacement.prefix)))
		}
	}
	return msg
}

func (h *Handler) buildPullRequestPrepCommitMessage(ctx context.Context, task *models.Task) string {
	commitCtx := service.WorktreeCommitMessageContext{
		Phase:     service.WorktreeCommitPhaseMerge,
		TaskTitle: task.Title,
	}
	if h.llmSvc != nil && task.AgentID != nil {
		commitCtx.DiffSummary = h.llmSvc.SummarizeWorktreeCommitDiffForAgentID(ctx, task.WorktreePath, *task.AgentID, commitCtx)
	}
	return service.BuildWorktreeCommitMessage(task.WorktreePath, commitCtx)
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
		applog.Infof("[handler] ResolveTaskConflicts error: %v", resolveErr)
		errMessage := "Failed to resolve merge conflicts"
		if result != nil && result.ErrorMessage != "" {
			errMessage = result.ErrorMessage
		} else if resolveErr.Error() != "" {
			errMessage = resolveErr.Error()
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		return c.String(http.StatusBadRequest, errMessage)
	}

	if result != nil && !result.Success {
		msg := "AI could not resolve all conflicts. Resolve conflicts manually or abort the merge."
		if result.ErrorMessage != "" {
			msg = result.ErrorMessage
		}
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
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

	if abortErr := service.AbortMerge(project.RepoPath); abortErr != nil {
		errMessage := fmt.Sprintf("Failed to abort merge: %v", abortErr)
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		return c.String(http.StatusBadRequest, errMessage)
	}
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
		applog.Infof("[handler] CleanupTaskWorktree error: %v", cleanErr)
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
	ctx := c.Request().Context()
	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	h.recoverTaskWorktreeState(ctx, task, project)
	var fileStats []service.WorktreeFileStat
	if task.WorktreeBranch != "" {
		// Detect already-merged branches so the worktree panel does not keep
		// rendering a "Merge to <target>" button for an already-merged branch.
		h.reconcileAlreadyMergedBranch(ctx, task)

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

	ctx := c.Request().Context()
	state := h.resolveTaskChangesWorktreeState(ctx, task)

	var reviewComments []models.ReviewComment
	if h.reviewCommentRepo != nil {
		reviewComments, _ = h.reviewCommentRepo.ListByTask(ctx, taskID)
	}

	diffView := h.uiDiffViewPreference(ctx)

	if state.UseWorktreeContent {
		var taskPR *models.TaskPullRequest
		if h.taskPullRequestRepo != nil {
			taskPR, _ = h.taskPullRequestRepo.GetByTaskID(ctx, taskID)
		}
		return render(c, http.StatusOK, pages.TaskChangesWorktreeContentWithView(
			state.DiffOutput, task, state.FileStats, reviewComments, taskPR, state.BranchAlreadyMerged, state.RebaseAvailable, diffView,
		))
	}

	return render(c, http.StatusOK, pages.TaskChangesContentWithView(state.DiffOutput, task.ID, reviewComments, diffView))
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

	setHTMXToast(c, "Worktree settings saved", "completed")
	return c.NoContent(http.StatusOK)
}
