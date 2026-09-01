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
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

var errTaskMutationEligibilityChanged = errors.New("task mutation eligibility changed")

func (h *Handler) taskCardMergeEligibility(ctx context.Context, task *models.Task, mergeType string) (taskMergeActionState, bool, string) {
	var state taskMergeActionState
	if task == nil {
		return state, false, "Task not found."
	}
	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	if project == nil || project.RepoPath == "" {
		return state, false, "The project has no repository path."
	}
	state = h.resolveTaskMergeActionState(ctx, task)
	if service.IsGitWorktreeLocked(project.RepoPath, task.WorktreePath) {
		return state, false, "The task worktree is locked."
	}
	eligibility := h.resolveTaskMergeEligibility(ctx, task, project, state.BranchAlreadyMerged)
	if eligibility.ConflictRecovery {
		return state, false, "Resolve or abort the active merge conflict first."
	}
	if !eligibility.MergeAvailable {
		reason := strings.TrimSpace(eligibility.Reason)
		if reason == "" {
			reason = "No mergeable task branch is available."
		}
		return state, false, reason
	}
	if mergeType == "rebase" && !state.RebaseAvailable {
		return state, false, "Rebase is not available for the current branch state."
	}
	if mergeType == "ff" && task.WorktreePath != "" {
		status, err := service.GitStatusPorcelain(task.WorktreePath)
		if err != nil || strings.TrimSpace(status) != "" {
			return state, false, "Fast-forward requires a clean task worktree."
		}
	}
	return state, true, ""
}

func cardMutationSource(c echo.Context) bool {
	return c.FormValue("merge_source") == "task_card"
}

func rejectTaskCardMutation(c echo.Context, message string) error {
	if isHTMX(c) {
		setHTMXToast(c, message, "failed")
	}
	return c.String(http.StatusConflict, message)
}

func (h *Handler) taskCardPullRequestEligibility(task *models.Task, project *models.Project) (bool, string) {
	if task == nil || strings.TrimSpace(task.WorktreeBranch) == "" {
		return false, "A task worktree branch is required."
	}
	if project == nil || strings.TrimSpace(project.RepoPath) == "" {
		return false, "The project has no repository path."
	}
	if task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return false, "The task worktree is currently in use."
	}
	if service.IsGitWorktreeLocked(project.RepoPath, task.WorktreePath) {
		return false, "The task worktree is locked."
	}
	if h.githubSvc == nil {
		return false, "GitHub integration is not configured."
	}
	if h.taskPullRequestRepo == nil {
		return false, "Pull request storage is unavailable."
	}
	return true, ""
}

// GetTaskCardMergeOptions returns freshly validated merge actions for one Kanban card.
func (h *Handler) GetTaskCardMergeOptions(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}
	task, err := h.taskSvc.GetByID(c.Request().Context(), c.Param("taskId"))
	if err != nil {
		return err
	}
	if task == nil || task.ProjectID != projectID {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	state, eligible, reason := h.taskCardMergeEligibility(c.Request().Context(), task, "merge")
	project, _ := h.projectRepo.GetByID(c.Request().Context(), projectID)

	if task.MergeTargetBranch == "" && project != nil && project.RepoPath != "" {
		task.MergeTargetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	var taskPR *models.TaskPullRequest
	if h.taskPullRequestRepo != nil {
		taskPR, _ = h.taskPullRequestRepo.GetByTaskID(c.Request().Context(), task.ID)
	}
	prEligible, prUnavailableReason := h.taskCardPullRequestEligibility(task, project)
	return render(c, http.StatusOK, components.TaskCardMergeOptions(task, projectID, eligible, state.RebaseAvailable, reason, taskPR, prEligible, prUnavailableReason))
}

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
	fromTaskCard := c.FormValue("merge_source") == "task_card"
	if fromTaskCard {
		projectID := strings.TrimSpace(c.FormValue("project_id"))
		if projectID == "" || task.ProjectID != projectID {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
	}

	// Get the repo path from the project
	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	task, err = h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	mergeType := c.FormValue("merge_type")
	if mergeType == "" {
		mergeType = "merge"
	}
	if mergeType != "merge" && mergeType != "ff" && mergeType != "squash" {
		if fromTaskCard {
			return rejectTaskCardMutation(c, "Unsupported merge mode.")
		}
		return c.String(http.StatusBadRequest, "unsupported merge type")
	}
	if fromTaskCard {
		if _, eligible, reason := h.taskCardMergeEligibility(c.Request().Context(), task, mergeType); !eligible {
			return rejectTaskCardMutation(c, reason)
		}
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

	fromChangesTab := c.FormValue("merge_source") == "changes_tab"

	result, mergeErr := h.worktreeSvc.MergeBranchValidated(c.Request().Context(), task, project.RepoPath, mergeType, func() error {
		freshTask, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
		if err != nil || freshTask == nil {
			return fmt.Errorf("%w: task is no longer available", service.ErrMergeEligibilityChanged)
		}
		h.recoverTaskWorktreeState(c.Request().Context(), freshTask, project)
		if fromTaskCard {
			if _, eligible, reason := h.taskCardMergeEligibility(c.Request().Context(), freshTask, mergeType); !eligible {
				return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
			}
		}
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
		if fromTaskCard {
			return c.String(http.StatusConflict, "Local merge has conflicts. Resolve conflicts or abort merge.")
		}
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

	if fromTaskCard {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
	}
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
	fromTaskCard := c.FormValue("merge_source") == "task_card"
	if fromTaskCard {
		projectID := strings.TrimSpace(c.FormValue("project_id"))
		if projectID == "" || task.ProjectID != projectID {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	task, err = h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if fromTaskCard {
		if _, eligible, reason := h.taskCardMergeEligibility(c.Request().Context(), task, "rebase"); !eligible {
			return rejectTaskCardMutation(c, reason)
		}
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
		if fromTaskCard {
			if _, eligible, reason := h.taskCardMergeEligibility(c.Request().Context(), freshTask, "rebase"); !eligible {
				return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
			}
		}
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
			if fromTaskCard {
				return rejectTaskCardMutation(c, rebaseErr.Error())
			}
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
		if fromTaskCard {
			return c.String(http.StatusConflict, msg)
		}
		return h.GetTaskChanges(c)
	}

	if result != nil && result.UpToDate {
		setHTMXToast(c, fmt.Sprintf("Task branch is already up to date with %s", targetBranch), "completed")
	} else {
		setHTMXToast(c, fmt.Sprintf("Rebased task branch onto %s", targetBranch), "completed")
	}
	if fromTaskCard {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
	}
	return h.GetTaskChanges(c)
}

func taskPullRequestFailure(c echo.Context, fromTaskCard bool, message string) error {
	if fromTaskCard {
		if isHTMX(c) {
			setHTMXToast(c, message, "failed")
		}
		return c.String(http.StatusBadRequest, message)
	}
	setHTMXToast(c, message, "failed")
	return c.NoContent(http.StatusNoContent)
}

func taskCardPullRequestNotFound(c echo.Context) error {
	const message = "Task not found"
	if isHTMX(c) {
		setHTMXToast(c, message, "failed")
	}
	return c.String(http.StatusNotFound, message)
}

// CreateTaskPullRequest creates or reuses a pull request for a task worktree branch.
func (h *Handler) CreateTaskPullRequest(c echo.Context) error {
	taskID := c.Param("taskId")
	fromTaskCard := cardMutationSource(c)
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		if fromTaskCard {
			return taskCardPullRequestNotFound(c)
		}
		return taskPullRequestFailure(c, false, "Task not found")
	}
	if fromTaskCard {
		projectID := strings.TrimSpace(c.FormValue("project_id"))
		if projectID == "" || task.ProjectID != projectID {
			return taskCardPullRequestNotFound(c)
		}
	}
	if task.WorktreeBranch == "" {
		return taskPullRequestFailure(c, fromTaskCard, "Task has no worktree branch")
	}
	if h.githubSvc == nil {
		if fromTaskCard {
			return taskPullRequestFailure(c, true, "GitHub integration is not configured")
		}
		setHTMXToastWithLink(c, "GitHub integration is not configured", "failed", "/channels", "Open Channels")
		return c.NoContent(http.StatusNoContent)
	}
	if h.taskPullRequestRepo == nil {
		return taskPullRequestFailure(c, fromTaskCard, "Task pull request repository not available")
	}

	project, err := h.projectRepo.GetByID(c.Request().Context(), task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return taskPullRequestFailure(c, fromTaskCard, "Project has no repository path configured")
	}
	var eligibilityReason string
	result, mutationErr := h.newTaskPullRequestService().OpenForTaskValidated(c.Request().Context(), project, task, service.OpenTaskPullRequestOptions{
		CommitMessage: h.buildPullRequestPrepCommitMessage(c.Request().Context(), task),
	}, func() (*models.Task, error) {
		currentTask, loadErr := h.taskSvc.GetByID(c.Request().Context(), taskID)
		if loadErr != nil || currentTask == nil {
			eligibilityReason = "Task not found."
			return nil, errTaskMutationEligibilityChanged
		}
		if fromTaskCard {
			if eligible, reason := h.taskCardPullRequestEligibility(currentTask, project); !eligible {
				eligibilityReason = reason
				return nil, errTaskMutationEligibilityChanged
			}
		}
		task = currentTask
		return currentTask, nil
	})
	if errors.Is(mutationErr, errTaskMutationEligibilityChanged) {
		return rejectTaskCardMutation(c, eligibilityReason)
	}
	if mutationErr != nil {
		return taskPullRequestFailure(c, fromTaskCard, formatTaskPullRequestError(mutationErr))
	}

	if result.ReusedExistingRecord {
		setHTMXToast(c, fmt.Sprintf("GitHub PR already exists (#%d)", result.PullRequest.Number), "success")
	} else if result.Created {
		setHTMXToast(c, fmt.Sprintf("GitHub PR created (#%d)", result.PullRequest.Number), "success")
	} else {
		setHTMXToast(c, fmt.Sprintf("GitHub PR reused (#%d)", result.PullRequest.Number), "success")
	}
	if fromTaskCard {
		return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
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
	abortErr := h.worktreeSvc.AbortMergeForTaskValidated(c.Request().Context(), taskID, project.RepoPath, abortBranch, abortTarget, models.MergeStatusPending, func() error {
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
