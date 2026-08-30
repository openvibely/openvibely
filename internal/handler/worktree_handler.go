package handler

import (
	"context"
	"errors"
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
	branchAlreadyMerged := h.reconcileAlreadyMergedBranch(c.Request().Context(), task)
	eligibility := h.resolveTaskMergeEligibility(c.Request().Context(), task, project, branchAlreadyMerged)
	if !eligibility.MergeAvailable {
		msg := eligibility.Reason
		if eligibility.ConflictRecovery {
			msg = "A merge conflict is already active. Resolve conflicts or abort the merge before trying another merge."
		}
		if msg == "" {
			msg = "Task branch is not currently eligible to merge"
		}
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
		return c.String(http.StatusConflict, msg)
	}
	targetBranch := eligibility.TargetBranch

	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	mergeType := c.FormValue("merge_type")
	if mergeType == "" {
		mergeType = "merge"
	}
	if mergeType != "merge" && mergeType != "ff" && mergeType != "squash" {
		return c.String(http.StatusBadRequest, "unsupported merge type")
	}

	fromChangesTab := c.FormValue("merge_source") == "changes_tab"

	result, mergeErr := h.worktreeSvc.MergeBranchValidated(c.Request().Context(), task, project.RepoPath, mergeType, func() error {
		freshTask, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
		if err != nil || freshTask == nil {
			return fmt.Errorf("%w: task is no longer available", service.ErrMergeEligibilityChanged)
		}
		h.recoverTaskWorktreeState(c.Request().Context(), freshTask, project)
		freshEligibility := h.resolveTaskMergeEligibility(c.Request().Context(), freshTask, project, h.reconcileAlreadyMergedBranch(c.Request().Context(), freshTask))
		if !freshEligibility.MergeAvailable {
			reason := freshEligibility.Reason
			if freshEligibility.ConflictRecovery {
				reason = "a merge conflict is now active"
			}
			if reason == "" {
				reason = "task branch is no longer eligible to merge"
			}
			return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
		}
		*task = *freshTask
		targetBranch = freshEligibility.TargetBranch
		return nil
	})
	if mergeErr != nil {
		applog.Infof("[handler] MergeTaskBranch error: %v", mergeErr)
		errMessage := "Local merge failed"
		if errors.Is(mergeErr, service.ErrMergeEligibilityChanged) {
			errMessage = mergeErr.Error()
			if isHTMX(c) {
				setHTMXToast(c, errMessage, "failed")
			}
			if fromChangesTab {
				return h.GetTaskChanges(c)
			}
			return c.String(http.StatusConflict, errMessage)
		}
		if errors.Is(mergeErr, service.ErrMergeInProgress) {
			errMessage = "A local merge is already in progress for this repository. Wait for it to finish before trying another merge."
			if isHTMX(c) {
				setHTMXToast(c, errMessage, "failed")
			}
			return c.String(http.StatusConflict, errMessage)
		}
		if result != nil && result.ErrorMessage != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", result.ErrorMessage)
		} else if mergeErr.Error() != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", mergeErr.Error())
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		// The Changes tab owns an authoritative fragment. A recoverable merge
		// refusal persists merge_status=failed, so re-render from fresh task/Git
		// state instead of leaving the menu in its in-flight or stale state. The
		// toast carries the failure while a 200 response lets HTMX apply the
		// refreshed retry/recovery actions. Other callers retain the error status.
		if fromChangesTab {
			task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
			return h.GetTaskChanges(c)
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
	branchAlreadyMerged := h.reconcileAlreadyMergedBranch(c.Request().Context(), task)
	eligibility := h.resolveTaskMergeEligibility(c.Request().Context(), task, project, branchAlreadyMerged)
	if !eligibility.MergeAvailable || !h.taskRebaseAvailable(task, project, branchAlreadyMerged) {
		msg := eligibility.Reason
		if eligibility.ConflictRecovery {
			msg = "A merge conflict is already active. Resolve conflicts or abort the merge before rebasing."
		} else if eligibility.MergeAvailable {
			msg = "Task branch is not currently eligible to rebase onto its target"
		}
		if msg == "" {
			msg = "Task branch is not currently eligible to rebase"
		}
		if isHTMX(c) {
			setHTMXToast(c, msg, "failed")
		}
		return c.String(http.StatusConflict, msg)
	}
	if h.worktreeSvc == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "worktree service not available")
	}

	targetBranch := eligibility.TargetBranch

	result, rebaseErr := h.worktreeSvc.RebaseBranchValidated(c.Request().Context(), task, project.RepoPath, func() error {
		freshTask, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
		if err != nil || freshTask == nil {
			return fmt.Errorf("%w: task is no longer available", service.ErrMergeEligibilityChanged)
		}
		h.recoverTaskWorktreeState(c.Request().Context(), freshTask, project)
		freshAlreadyMerged := h.reconcileAlreadyMergedBranch(c.Request().Context(), freshTask)
		freshEligibility := h.resolveTaskMergeEligibility(c.Request().Context(), freshTask, project, freshAlreadyMerged)
		if !freshEligibility.MergeAvailable || !h.taskRebaseAvailable(freshTask, project, freshAlreadyMerged) {
			reason := freshEligibility.Reason
			if freshEligibility.ConflictRecovery {
				reason = "a merge conflict is now active"
			} else if freshEligibility.MergeAvailable {
				reason = "task branch is no longer eligible to rebase onto its target"
			}
			if reason == "" {
				reason = "task branch is no longer eligible to rebase"
			}
			return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
		}
		*task = *freshTask
		targetBranch = freshEligibility.TargetBranch
		return nil
	})
	if rebaseErr != nil {
		applog.Infof("[handler] RebaseTaskBranch error: %v", rebaseErr)
		if errors.Is(rebaseErr, service.ErrMergeEligibilityChanged) {
			if isHTMX(c) {
				setHTMXToast(c, rebaseErr.Error(), "failed")
			}
			return h.GetTaskChanges(c)
		}
		if errors.Is(rebaseErr, service.ErrMergeInProgress) {
			errMessage := "A local merge or rebase is already in progress for this repository. Wait for it to finish before rebasing."
			if isHTMX(c) {
				setHTMXToast(c, errMessage, "failed")
			}
			return c.String(http.StatusConflict, errMessage)
		}
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

	result, err := h.newTaskPullRequestService().OpenForTask(c.Request().Context(), project, task, service.OpenTaskPullRequestOptions{
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

// revalidateTaskConflictRecovery reloads task and Git state while the canonical
// repository mutation lease is held. Recovery must only mutate a conflict that
// still belongs to this task branch.
func (h *Handler) revalidateTaskConflictRecovery(ctx context.Context, taskID string, task *models.Task, project *models.Project) (taskMergeEligibility, error) {
	freshTask, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil || freshTask == nil {
		return taskMergeEligibility{}, fmt.Errorf("%w: task is no longer available", service.ErrMergeEligibilityChanged)
	}
	h.recoverTaskWorktreeState(ctx, freshTask, project)
	freshEligibility := h.resolveTaskMergeEligibility(ctx, freshTask, project, h.reconcileAlreadyMergedBranch(ctx, freshTask))
	if !freshEligibility.ConflictRecovery {
		reason := freshEligibility.Reason
		if reason == "" {
			reason = "the active conflict no longer belongs to this task"
		}
		return freshEligibility, fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
	}
	*task = *freshTask
	return freshEligibility, nil
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
	h.recoverTaskWorktreeState(c.Request().Context(), task, project)
	eligibility := h.resolveTaskMergeEligibility(c.Request().Context(), task, project, h.reconcileAlreadyMergedBranch(c.Request().Context(), task))
	fromChangesTab := c.FormValue("merge_source") == "changes_tab"
	if !eligibility.ConflictRecovery {
		msg := "No active merge conflicts remain. Merge actions have been refreshed."
		if isHTMX(c) {
			setHTMXToast(c, msg, "info")
		}
		if fromChangesTab {
			return h.GetTaskChanges(c)
		}
		return c.String(http.StatusConflict, msg)
	}

	result, resolveErr := h.worktreeSvc.ResolveConflictsWithAIValidated(c.Request().Context(), task, project.RepoPath, func() error {
		_, err := h.revalidateTaskConflictRecovery(c.Request().Context(), taskID, task, project)
		return err
	})
	if resolveErr != nil {
		applog.Infof("[handler] ResolveTaskConflicts error: %v", resolveErr)
		errMessage := "Failed to resolve merge conflicts"
		status := http.StatusBadRequest
		if errors.Is(resolveErr, service.ErrMergeInProgress) {
			errMessage = "Another merge, rebase, or conflict recovery is already in progress for this repository. Wait for it to finish before resolving conflicts."
			status = http.StatusConflict
		} else if errors.Is(resolveErr, service.ErrMergeEligibilityChanged) {
			errMessage = resolveErr.Error()
			status = http.StatusConflict
		} else if result != nil && result.ErrorMessage != "" {
			errMessage = result.ErrorMessage
		} else if resolveErr.Error() != "" {
			errMessage = resolveErr.Error()
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		if fromChangesTab {
			task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
			return h.GetTaskChanges(c)
		}
		return c.String(status, errMessage)
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
	if c.FormValue("merge_source") == "changes_tab" {
		return h.GetTaskChanges(c)
	}
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
	h.recoverTaskWorktreeState(c.Request().Context(), task, project)
	eligibility := h.resolveTaskMergeEligibility(c.Request().Context(), task, project, h.reconcileAlreadyMergedBranch(c.Request().Context(), task))
	fromChangesTab := c.FormValue("merge_source") == "changes_tab"
	if !eligibility.ConflictRecovery {
		msg := "No active merge remains. Merge actions have been refreshed."
		if isHTMX(c) {
			setHTMXToast(c, msg, "info")
		}
		if fromChangesTab {
			return h.GetTaskChanges(c)
		}
		return c.String(http.StatusConflict, msg)
	}

	abortBranch := task.WorktreeBranch
	abortTarget := eligibility.TargetBranch
	abortErr := service.AbortMergeForTaskValidated(project.RepoPath, abortBranch, abortTarget, func() error {
		freshEligibility, err := h.revalidateTaskConflictRecovery(c.Request().Context(), taskID, task, project)
		if err != nil {
			return err
		}
		if task.WorktreeBranch != abortBranch || freshEligibility.TargetBranch != abortTarget {
			return fmt.Errorf("%w: task conflict metadata changed before abort", service.ErrMergeEligibilityChanged)
		}
		eligibility = freshEligibility
		return nil
	})
	if abortErr != nil {
		errMessage := fmt.Sprintf("Failed to abort merge: %v", abortErr)
		status := http.StatusBadRequest
		if errors.Is(abortErr, service.ErrMergeInProgress) {
			errMessage = "Another merge, rebase, or conflict recovery is already in progress for this repository. Wait for it to finish before aborting."
			status = http.StatusConflict
		} else if errors.Is(abortErr, service.ErrMergeEligibilityChanged) {
			errMessage = abortErr.Error()
			status = http.StatusConflict
		}
		if isHTMX(c) {
			setHTMXToast(c, errMessage, "failed")
		}
		if fromChangesTab {
			task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
			return h.GetTaskChanges(c)
		}
		return c.String(status, errMessage)
	}
	_ = h.taskRepo.UpdateMergeStatus(c.Request().Context(), taskID, models.MergeStatusPending)

	task, _ = h.taskSvc.GetByID(c.Request().Context(), taskID)
	if c.FormValue("merge_source") == "changes_tab" {
		return h.GetTaskChanges(c)
	}
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
			state.DiffOutput, task, state.FileStats, reviewComments, taskPR, state.LocalMergeUnavailable, state.ConflictRecovery, state.RebaseAvailable, diffView,
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
