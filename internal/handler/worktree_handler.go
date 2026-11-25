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
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

var errTaskMutationEligibilityChanged = errors.New("task mutation eligibility changed")

func (h *Handler) renderTaskCardRefresh(c echo.Context, taskID string) error {
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if task.SwarmRole == models.SwarmRoleParent {
		if children, childErr := h.taskRepo.ListSwarmChildren(c.Request().Context(), task.ID); childErr == nil {
			task.SwarmChildren = children
		}
	}
	agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
	agentDefs := h.listAgentDefinitions(c.Request().Context())
	return render(c, http.StatusOK, components.TaskCard(*task, task.ProjectID, string(task.Category), agents, agentDefs))
}

func (h *Handler) taskCardMergeEligibility(ctx context.Context, task *models.Task, mergeType string) (taskMergeActionState, bool, string) {
	if task == nil {
		return taskMergeActionState{}, false, "Task not found."
	}
	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	return h.taskCardMergeEligibilityForProject(ctx, task, project, mergeType)
}

func (h *Handler) taskCardMergeEligibilityForProject(ctx context.Context, task *models.Task, project *models.Project, mergeType string) (taskMergeActionState, bool, string) {
	return h.taskCardMergeEligibilityForProjectWithLocks(ctx, task, project, mergeType, nil)
}

func (h *Handler) taskCardMergeEligibilityForProjectWithLocks(ctx context.Context, task *models.Task, project *models.Project, mergeType string, worktreeLocks map[string]bool) (taskMergeActionState, bool, string) {
	var state taskMergeActionState
	if task == nil {
		return state, false, "Task not found."
	}
	if project == nil || project.RepoPath == "" {
		return state, false, "The project has no repository path."
	}
	state = h.resolveTaskMergeActionStateForProjectWithLocks(ctx, task, project, worktreeLocks)
	if taskCardWorktreeLocked(project, task, worktreeLocks) {
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
	eligible, reason := taskCardMergeModeEligibility(ctx, task, state, mergeType)
	return state, eligible, reason
}

func taskCardMergeModeEligibility(ctx context.Context, task *models.Task, state taskMergeActionState, mergeType string) (bool, string) {
	if mergeType == "rebase" && !state.RebaseAvailable {
		return false, "Rebase is not available for the current branch state."
	}
	if mergeType == "ff" && task.WorktreePath != "" {
		status, err := service.GitStatusPorcelain(ctx, task.WorktreePath)
		if err != nil || strings.TrimSpace(status) != "" {
			return false, "Fast-forward requires a clean task worktree."
		}
	}
	return true, ""
}

func cardMutationSource(c echo.Context) bool {
	return c.FormValue("merge_source") == "task_card"
}

func (h *Handler) taskCardPullRequestEligibility(task *models.Task, project *models.Project) (bool, string) {
	return h.taskCardPullRequestEligibilityWithLocks(task, project, nil)
}

func (h *Handler) taskCardPullRequestEligibilityWithLocks(task *models.Task, project *models.Project, worktreeLocks map[string]bool) (bool, string) {
	if task == nil || strings.TrimSpace(task.WorktreeBranch) == "" {
		return false, "A task worktree branch is required."
	}
	if project == nil || strings.TrimSpace(project.RepoPath) == "" {
		return false, "The project has no repository path."
	}
	if task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return false, "The task worktree is currently in use."
	}
	if taskCardWorktreeLocked(project, task, worktreeLocks) {
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

	menu, err := h.taskCardMergeMenu(c.Request().Context(), task.ID)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, components.TaskCardMergeSubmenus(task, projectID, menu))
}

func (h *Handler) taskCardMergeMenu(ctx context.Context, taskID string) (components.TaskCardMergeMenuState, error) {
	var menu components.TaskCardMergeMenuState
	if remote, err := h.remoteAct(ctx, taskID, "task.merge_menu", taskID, &menu); remote {
		return menu, err
	}
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return menu, err
	}
	state, eligible, _ := h.taskCardMergeEligibility(ctx, task, "merge")
	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	if task.MergeTargetBranch == "" && project != nil && project.RepoPath != "" {
		task.MergeTargetBranch = service.GetDefaultBranch(project.RepoPath)
	}
	if h.taskPullRequestRepo != nil {
		menu.PullRequest, _ = h.taskPullRequestRepo.GetByTaskID(ctx, task.ID)
	}
	if eligible {
		menu.FastForwardEligible, _ = taskCardMergeModeEligibility(ctx, task, state, "ff")
	}
	menu.LocalEligible = eligible
	menu.RebaseEligible = eligible && state.RebaseAvailable
	menu.PullEligible, _ = h.taskCardPullRequestEligibility(task, project)
	menu.TargetBranch = task.MergeTargetBranch
	return menu, nil
}

// UpdateTaskAutoMerge toggles auto-merge for a task.
func (h *Handler) UpdateTaskAutoMerge(c echo.Context) error {
	args := autoMergeArgs{TaskID: c.Param("taskId"), TargetBranch: c.FormValue("merge_target_branch")}
	args.AutoMerge = formBoolEnabled(c, "auto_merge", false)
	if c.FormValue("auto_merge_on_goal_achieved") != "" || c.FormValue("auto_merge_on_goal_achieved_present") != "" {
		enabled := formBoolEnabled(c, "auto_merge_on_goal_achieved", false)
		args.OnGoalAchieved = &enabled
	}
	task, err := actAutoMerge(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	return h.renderWorktreeInfo(c, task)
}

type autoMergeArgs struct {
	TaskID         string
	AutoMerge      bool
	OnGoalAchieved *bool
	TargetBranch   string
}

var actAutoMerge = defineAction("task.automerge", func(h *Handler, ctx context.Context, a autoMergeArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}, func(h *Handler, ctx context.Context, a autoMergeArgs) (*models.Task, error) {
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return nil, err
	}
	if a.TargetBranch == "" {
		a.TargetBranch = task.MergeTargetBranch
	}
	if err := h.taskRepo.UpdateAutoMergeSettings(ctx, a.TaskID, a.AutoMerge, a.OnGoalAchieved, a.TargetBranch); err != nil {
		applog.Infof("[handler] UpdateTaskAutoMerge error: %v", err)
		return nil, err
	}
	task.AutoMerge = a.AutoMerge
	if a.OnGoalAchieved != nil {
		task.AutoMergeOnGoalAchieved = *a.OnGoalAchieved
	}
	task.MergeTargetBranch = a.TargetBranch
	return task, nil
})

// mutationOutcome is how a worktree action's result is shown: a toast, then
// either a refreshed View or a plain Code/Message response.
type mutationOutcome struct {
	Code    int
	Message string
	// Toast is the toast status; without ToastAlways it shows only to HTMX.
	Toast          string
	ToastAlways    bool
	ToastTask      bool
	RefreshChanges bool
	LinkURL        string
	LinkText       string
	View           string
}

const (
	viewCard     = "card"
	viewChanges  = "changes"
	viewWorktree = "worktree"
	viewBoard    = "board"
	viewNone     = "none"
)

func failText(code int, message string) mutationOutcome {
	return mutationOutcome{Code: code, Message: message, Toast: "failed"}
}

func failView(view, message string) mutationOutcome {
	return mutationOutcome{View: view, Message: message, Toast: "failed"}
}

func (h *Handler) respondMutation(c echo.Context, taskID string, o mutationOutcome) error {
	if o.Toast != "" && (o.ToastAlways || isHTMX(c)) {
		var triggers map[string]any
		if o.RefreshChanges {
			triggers = map[string]any{"refreshChanges": true}
		}
		toastTask := ""
		if o.ToastTask {
			toastTask = taskID
		}
		setHTMXToastWithOptionsAndTriggers(c, o.Message, o.Toast, o.LinkURL, o.LinkText, toastTask, "", "", triggers)
	}
	switch o.View {
	case viewCard:
		return h.renderTaskCardRefresh(c, taskID)
	case viewChanges:
		return h.GetTaskChanges(c)
	case viewWorktree, viewBoard:
		task, err := h.loadTask(c.Request().Context(), taskID)
		if err != nil {
			return actionHTTPError(err)
		}
		if o.View == viewBoard {
			return h.renderTaskBoardRefresh(c, task.ProjectID, nil)
		}
		return h.renderWorktreeInfo(c, task)
	case viewNone:
		return c.NoContent(http.StatusNoContent)
	}
	return c.String(o.Code, o.Message)
}

type taskBranchMutationPreflight struct {
	task                *models.Task
	project             *models.Project
	branchAlreadyMerged bool
	eligibility         taskMergeEligibility
	cardReason          string
	operationEligible   bool
	operationReason     string
}

type taskBranchMutationOptions struct {
	selectedTarget string // Immutable destination supplied by this action request.
	taskID         string
	projectID      string
	fromTaskCard   bool
	operationCheck func(context.Context, *models.Task, *models.Project, bool) (bool, string)
	leaseReason    func(*taskBranchMutationPreflight) string
}

func taskBranchMutationOwnershipMatches(task *models.Task, projectID string, fromTaskCard bool) bool {
	return !fromTaskCard || (task != nil && strings.TrimSpace(projectID) != "" && task.ProjectID == projectID)
}

func (h *Handler) loadTaskBranchMutation(ctx context.Context, taskID, projectID string, fromTaskCard bool) (*taskBranchMutationPreflight, error) {
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if !taskBranchMutationOwnershipMatches(task, projectID, fromTaskCard) {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	task, err = h.taskSvc.GetByID(ctx, taskID)
	if err != nil || task == nil || !taskBranchMutationOwnershipMatches(task, projectID, fromTaskCard) {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	return &taskBranchMutationPreflight{task: task, project: project, operationEligible: true}, nil
}

// refreshTaskBranchMutation performs the common live recovery, ancestry, and
// merge-eligibility checks. Operation-specific policy is supplied by the
// callback so Merge and Rebase retain their distinct gates.
func (h *Handler) refreshTaskBranchMutation(ctx context.Context, preflight *taskBranchMutationPreflight, options taskBranchMutationOptions) {
	h.recoverTaskWorktreeState(ctx, preflight.task, preflight.project)
	preflight.branchAlreadyMerged = taskBranchAlreadyMergedForProject(ctx, preflight.task, preflight.project)
	preflight.eligibility = taskMergeEligibility{}
	preflight.cardReason = ""
	preflight.operationEligible = true
	preflight.operationReason = ""

	if options.fromTaskCard && taskCardWorktreeLocked(preflight.project, preflight.task, nil) {
		preflight.cardReason = "The task worktree is locked."
		preflight.operationEligible = false
		return
	}

	preflight.eligibility = h.resolveTaskMergeEligibility(ctx, preflight.task, preflight.project, preflight.branchAlreadyMerged)
	if options.fromTaskCard {
		if preflight.eligibility.ConflictRecovery {
			preflight.cardReason = "Resolve or abort the active merge conflict first."
			preflight.operationEligible = false
			return
		}
		if !preflight.eligibility.MergeAvailable {
			reason := strings.TrimSpace(preflight.eligibility.Reason)
			if reason == "" {
				reason = "No mergeable task branch is available."
			}
			preflight.cardReason = reason
			preflight.operationEligible = false
			return
		}
	}
	if !preflight.eligibility.MergeAvailable {
		preflight.operationEligible = false
		return
	}
	if options.operationCheck != nil {
		preflight.operationEligible, preflight.operationReason = options.operationCheck(ctx, preflight.task, preflight.project, preflight.branchAlreadyMerged)
	}
}

// revalidateTaskBranchMutation reloads task/project identity and reruns the
// shared policy checks while the repository mutation lease is held.
func (h *Handler) revalidateTaskBranchMutation(ctx context.Context, preflight *taskBranchMutationPreflight, options taskBranchMutationOptions) error {
	freshTask, err := h.taskSvc.GetByID(ctx, options.taskID)
	if err != nil || freshTask == nil {
		return fmt.Errorf("%w: task is no longer available", service.ErrMergeEligibilityChanged)
	}
	if freshTask.ProjectID != preflight.project.ID || !taskBranchMutationOwnershipMatches(freshTask, options.projectID, options.fromTaskCard) {
		return fmt.Errorf("%w: task project ownership changed", service.ErrMergeEligibilityChanged)
	}

	freshProject, err := h.projectRepo.GetByID(ctx, freshTask.ProjectID)
	if err != nil || freshProject == nil || freshProject.RepoPath == "" {
		return fmt.Errorf("%w: project repository is no longer available", service.ErrMergeEligibilityChanged)
	}
	if freshProject.ID != preflight.project.ID || freshProject.RepoPath != preflight.project.RepoPath {
		return fmt.Errorf("%w: project repository context changed", service.ErrMergeEligibilityChanged)
	}

	if err := h.selectChangesLocalTarget(ctx, freshTask, freshProject, options.selectedTarget); err != nil {
		return fmt.Errorf("%w: %v", service.ErrMergeEligibilityChanged, err)
	}
	preflight.task = freshTask
	preflight.project = freshProject
	h.refreshTaskBranchMutation(ctx, preflight, options)
	if preflight.cardReason != "" {
		return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, preflight.cardReason)
	}
	if !preflight.eligibility.MergeAvailable || !preflight.operationEligible {
		reason := ""
		if options.leaseReason != nil {
			reason = options.leaseReason(preflight)
		}
		if reason == "" {
			reason = "task branch is no longer eligible for this operation"
		}
		return fmt.Errorf("%w: %s", service.ErrMergeEligibilityChanged, reason)
	}
	if options.selectedTarget != "" {
		if err := h.taskRepo.UpdateMergeTargetBranch(ctx, freshTask.ID, options.selectedTarget); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) mergeTaskBranchOptions(taskID, projectID string, fromTaskCard bool, mergeType string) taskBranchMutationOptions {
	return taskBranchMutationOptions{
		taskID:       taskID,
		projectID:    projectID,
		fromTaskCard: fromTaskCard,
		operationCheck: func(ctx context.Context, task *models.Task, project *models.Project, branchAlreadyMerged bool) (bool, string) {
			if !fromTaskCard {
				return true, ""
			}
			state := taskMergeActionState{
				BranchAlreadyMerged: branchAlreadyMerged,
				RebaseAvailable:     h.taskRebaseAvailable(ctx, task, project, branchAlreadyMerged),
			}
			return taskCardMergeModeEligibility(ctx, task, state, mergeType)
		},
		leaseReason: func(preflight *taskBranchMutationPreflight) string {
			if !preflight.eligibility.MergeAvailable {
				reason := preflight.eligibility.Reason
				if preflight.eligibility.ConflictRecovery {
					reason = "a merge conflict is now active"
				}
				if reason == "" {
					reason = "task branch is no longer eligible to merge"
				}
				return reason
			}
			if !preflight.operationEligible && preflight.operationReason != "" {
				return preflight.operationReason
			}
			return "task branch is no longer eligible to merge"
		},
	}
}

func (h *Handler) rebaseTaskBranchOptions(taskID, projectID string, fromTaskCard bool) taskBranchMutationOptions {
	return taskBranchMutationOptions{
		taskID:       taskID,
		projectID:    projectID,
		fromTaskCard: fromTaskCard,
		operationCheck: func(ctx context.Context, task *models.Task, project *models.Project, branchAlreadyMerged bool) (bool, string) {
			if h.taskRebaseAvailable(ctx, task, project, branchAlreadyMerged) {
				return true, ""
			}
			if fromTaskCard {
				return false, "Rebase is not available for the current branch state."
			}
			return false, "Task branch is not currently eligible to rebase onto its target"
		},
		leaseReason: func(preflight *taskBranchMutationPreflight) string {
			if !preflight.eligibility.MergeAvailable {
				reason := preflight.eligibility.Reason
				if preflight.eligibility.ConflictRecovery {
					reason = "a merge conflict is now active"
				}
				if reason == "" {
					reason = "task branch is no longer eligible to rebase"
				}
				return reason
			}
			if !preflight.operationEligible && fromTaskCard && preflight.operationReason != "" {
				return preflight.operationReason
			}
			return "task branch is no longer eligible to rebase onto its target"
		},
	}
}

// branchMutationArgs requests a merge or rebase. CardProjectID is the board
// a task card action came from.
type branchMutationArgs struct {
	TaskID         string
	CardProjectID  string
	FromTaskCard   bool
	FromChangesTab bool
	MergeType      string
	Target         string
}

func ownBranchMutation(h *Handler, ctx context.Context, a branchMutationArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}

func (h *Handler) parseBranchMutation(c echo.Context) (branchMutationArgs, error) {
	a := branchMutationArgs{
		TaskID:         c.Param("taskId"),
		CardProjectID:  strings.TrimSpace(c.FormValue("project_id")),
		FromTaskCard:   c.FormValue("merge_source") == "task_card",
		FromChangesTab: c.FormValue("merge_source") == "changes_tab",
		MergeType:      c.FormValue("merge_type"),
		Target:         strings.TrimSpace(c.FormValue("changes_target")),
	}
	if a.Target != "" {
		if _, _, err := h.changesActionContext(c); err != nil {
			return a, err
		}
	}
	return a, nil
}

// MergeTaskBranch manually merges a task's worktree branch to target.
func (h *Handler) MergeTaskBranch(c echo.Context) error {
	args, err := h.parseBranchMutation(c)
	if err != nil {
		return err
	}
	if args.MergeType == "" {
		args.MergeType = "merge"
	}
	out, err := actMergeTaskBranch(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	return h.respondMutation(c, args.TaskID, out)
}

func (h *Handler) applyChangesTarget(ctx context.Context, preflight *taskBranchMutationPreflight, target string) (string, error) {
	if target == "" {
		return "", nil
	}
	if err := h.selectChangesLocalTarget(ctx, preflight.task, preflight.project, target); err != nil {
		return "", statusErr(http.StatusBadRequest, err.Error())
	}
	return target, nil
}

func mutationView(fromTaskCard, fromChangesTab bool) string {
	if fromTaskCard {
		return viewCard
	}
	if fromChangesTab {
		return viewChanges
	}
	return viewWorktree
}

var actMergeTaskBranch = defineAction("task.merge", ownBranchMutation, func(h *Handler, ctx context.Context, a branchMutationArgs) (mutationOutcome, error) {
	preflight, err := h.loadTaskBranchMutation(ctx, a.TaskID, a.CardProjectID, a.FromTaskCard)
	if err != nil {
		return mutationOutcome{}, err
	}
	task := preflight.task
	project := preflight.project
	if a.MergeType != "merge" && a.MergeType != "ff" && a.MergeType != "squash" {
		if a.FromTaskCard {
			return failText(http.StatusConflict, "Unsupported merge mode."), nil
		}
		return mutationOutcome{Code: http.StatusBadRequest, Message: "unsupported merge type"}, nil
	}

	options := h.mergeTaskBranchOptions(a.TaskID, a.CardProjectID, a.FromTaskCard, a.MergeType)
	if options.selectedTarget, err = h.applyChangesTarget(ctx, preflight, a.Target); err != nil {
		return mutationOutcome{}, err
	}
	h.refreshTaskBranchMutation(ctx, preflight, options)
	if a.FromTaskCard {
		if preflight.cardReason != "" {
			return failText(http.StatusConflict, preflight.cardReason), nil
		}
		if !preflight.operationEligible {
			return failText(http.StatusConflict, preflight.operationReason), nil
		}
	} else if !preflight.eligibility.MergeAvailable {
		msg := preflight.eligibility.Reason
		if preflight.eligibility.ConflictRecovery {
			msg = "A merge conflict is already active. Resolve conflicts or abort the merge before trying another merge."
		}
		if msg == "" {
			msg = "Task branch is not currently eligible to merge"
		}
		return failText(http.StatusConflict, msg), nil
	}

	if h.worktreeSvc == nil {
		return mutationOutcome{}, statusErr(http.StatusInternalServerError, "worktree service not available")
	}

	targetBranch := preflight.eligibility.TargetBranch
	result, mergeErr := h.worktreeSvc.MergeBranchValidated(ctx, task, project.RepoPath, a.MergeType, func() error {
		if err := h.revalidateTaskBranchMutation(ctx, preflight, options); err != nil {
			return err
		}
		*task = *preflight.task
		targetBranch = preflight.eligibility.TargetBranch
		return nil
	})
	if mergeErr != nil {
		applog.Infof("[handler] MergeTaskBranch error: %v", mergeErr)
		if errors.Is(mergeErr, service.ErrMergeEligibilityChanged) {
			if a.FromChangesTab {
				return failView(viewChanges, mergeErr.Error()), nil
			}
			return failText(http.StatusConflict, mergeErr.Error()), nil
		}
		if errors.Is(mergeErr, service.ErrMergeInProgress) {
			return failText(http.StatusConflict, "A local merge is already in progress for this repository. Wait for it to finish before trying another merge."), nil
		}
		errMessage := "Local merge failed"
		if result != nil && result.ErrorMessage != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", result.ErrorMessage)
		} else if mergeErr.Error() != "" {
			errMessage = fmt.Sprintf("Local merge failed: %s", mergeErr.Error())
		}
		// Card and Changes surfaces own authoritative retry fragments. Recoverable
		// merge failures return a swap-safe response so HTMX applies both the
		// refreshed controls and the failure toast. Stale eligibility failures above
		// retain their non-success status.
		if a.FromTaskCard && result != nil {
			return failView(viewCard, errMessage), nil
		}
		if a.FromChangesTab {
			return failView(viewChanges, errMessage), nil
		}
		return failText(http.StatusBadRequest, errMessage), nil
	}

	view := mutationView(a.FromTaskCard, a.FromChangesTab)
	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		return failView(view, "Local merge has conflicts. Resolve conflicts or abort merge."), nil
	}
	if targetBranch == "" {
		targetBranch = "main"
	}
	// Changes actions return the refreshed panel. Emitting refreshChanges
	// as well would fetch and swap that panel a second time, rebuilding its diffs.
	return mutationOutcome{
		View:           view,
		Message:        fmt.Sprintf("Merged locally into %s", targetBranch),
		Toast:          "completed",
		ToastAlways:    true,
		ToastTask:      true,
		RefreshChanges: !a.FromChangesTab,
	}, nil
})

// RebaseTaskBranch rebases a task's worktree branch onto its target branch.
func (h *Handler) RebaseTaskBranch(c echo.Context) error {
	args, err := h.parseBranchMutation(c)
	if err != nil {
		return err
	}
	out, err := actRebaseTaskBranch(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	return h.respondMutation(c, args.TaskID, out)
}

var actRebaseTaskBranch = defineAction("task.rebase", ownBranchMutation, func(h *Handler, ctx context.Context, a branchMutationArgs) (mutationOutcome, error) {
	options := h.rebaseTaskBranchOptions(a.TaskID, a.CardProjectID, a.FromTaskCard)
	preflight, err := h.loadTaskBranchMutation(ctx, a.TaskID, a.CardProjectID, a.FromTaskCard)
	if err != nil {
		return mutationOutcome{}, err
	}
	task := preflight.task
	project := preflight.project
	if options.selectedTarget, err = h.applyChangesTarget(ctx, preflight, a.Target); err != nil {
		return mutationOutcome{}, err
	}
	h.refreshTaskBranchMutation(ctx, preflight, options)
	if a.FromTaskCard {
		if preflight.cardReason != "" {
			return failText(http.StatusConflict, preflight.cardReason), nil
		}
		if !preflight.operationEligible {
			return failText(http.StatusConflict, preflight.operationReason), nil
		}
	} else if !preflight.eligibility.MergeAvailable || !preflight.operationEligible {
		msg := preflight.eligibility.Reason
		if preflight.eligibility.ConflictRecovery {
			msg = "A merge conflict is already active. Resolve conflicts or abort the merge before rebasing."
		} else if preflight.eligibility.MergeAvailable {
			msg = "Task branch is not currently eligible to rebase onto its target"
		}
		if msg == "" {
			msg = "Task branch is not currently eligible to rebase"
		}
		return failText(http.StatusConflict, msg), nil
	}
	if h.worktreeSvc == nil {
		return mutationOutcome{}, statusErr(http.StatusInternalServerError, "worktree service not available")
	}

	targetBranch := preflight.eligibility.TargetBranch
	result, rebaseErr := h.worktreeSvc.RebaseBranchValidated(ctx, task, project.RepoPath, func() error {
		if err := h.revalidateTaskBranchMutation(ctx, preflight, options); err != nil {
			return err
		}
		*task = *preflight.task
		targetBranch = preflight.eligibility.TargetBranch
		return nil
	})
	if rebaseErr != nil {
		applog.Infof("[handler] RebaseTaskBranch error: %v", rebaseErr)
		if errors.Is(rebaseErr, service.ErrMergeEligibilityChanged) {
			if a.FromTaskCard {
				return failText(http.StatusConflict, rebaseErr.Error()), nil
			}
			return failView(viewChanges, rebaseErr.Error()), nil
		}
		if errors.Is(rebaseErr, service.ErrMergeInProgress) {
			return failText(http.StatusConflict, "A local merge or rebase is already in progress for this repository. Wait for it to finish before rebasing."), nil
		}
		errMessage := "Rebase failed"
		if result != nil && result.ErrorMessage != "" {
			errMessage = fmt.Sprintf("Rebase failed: %s", result.ErrorMessage)
		} else if rebaseErr.Error() != "" {
			errMessage = fmt.Sprintf("Rebase failed: %s", rebaseErr.Error())
		}
		if a.FromTaskCard && result != nil {
			return failView(viewCard, errMessage), nil
		}
		return failText(http.StatusBadRequest, errMessage), nil
	}

	view := viewChanges
	if a.FromTaskCard {
		view = viewCard
	}
	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		msg := fmt.Sprintf("Rebase onto %s had conflicts and was aborted. Rebase manually and resolve the conflicts, or ask the agent to reconcile with %s, then try again.", targetBranch, targetBranch)
		if result.ErrorMessage != "" {
			msg = result.ErrorMessage
		}
		return failView(view, msg), nil
	}
	msg := fmt.Sprintf("Rebased task branch onto %s", targetBranch)
	if result != nil && result.UpToDate {
		msg = fmt.Sprintf("Task branch is already up to date with %s", targetBranch)
	}
	return mutationOutcome{View: view, Message: msg, Toast: "completed", ToastAlways: true}, nil
})

func taskPullRequestFailure(fromTaskCard bool, message string) mutationOutcome {
	if fromTaskCard {
		return mutationOutcome{Code: http.StatusBadRequest, Message: message, Toast: "failed"}
	}
	return mutationOutcome{View: viewNone, Message: message, Toast: "failed", ToastAlways: true}
}

var taskCardPullRequestNotFound = mutationOutcome{Code: http.StatusNotFound, Message: "Task not found", Toast: "failed"}

func (h *Handler) taskPullRequestProjectContext(c echo.Context) (string, bool, error) {
	ctx := c.Request().Context()
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" && projectID != "default" {
		project, err := h.projectSvc.GetByID(ctx, projectID)
		if err != nil {
			return "", true, err
		}
		if project == nil {
			return "", true, nil
		}
		return projectID, true, nil
	}
	if h.settingsRepo == nil {
		return "", false, nil
	}
	selectedProjectID, err := h.settingsRepo.Get(ctx, uiPreferenceSelectedProjectIDKey)
	if err != nil {
		applog.Debugf("[handler] failed to load selected project preference for task pull request: %v", err)
		return "", false, nil
	}
	selectedProjectID = strings.TrimSpace(selectedProjectID)
	if selectedProjectID == "" {
		return "", false, nil
	}
	project, err := h.projectSvc.GetByID(ctx, selectedProjectID)
	if err != nil {
		return "", true, err
	}
	if project == nil {
		return "", true, nil
	}
	return selectedProjectID, true, nil
}

// pullRequestArgs opens a PR. ProjectID is the project the request was made
// in; when set, the task must belong to it.
type pullRequestArgs struct {
	TaskID       string
	ProjectID    string
	FromTaskCard bool
	TargetRepo   string
	Base         string
}

// CreateTaskPullRequest creates or reuses a pull request for a task worktree branch.
func (h *Handler) CreateTaskPullRequest(c echo.Context) error {
	args := pullRequestArgs{
		TaskID:       c.Param("taskId"),
		FromTaskCard: cardMutationSource(c),
		TargetRepo:   strings.TrimSpace(c.FormValue("target_repo")),
		Base:         strings.TrimSpace(c.FormValue("base")),
	}
	if args.FromTaskCard {
		args.ProjectID = strings.TrimSpace(c.FormValue("project_id"))
		if args.ProjectID == "" {
			return h.respondMutation(c, args.TaskID, taskCardPullRequestNotFound)
		}
	} else {
		projectID, hasProject, err := h.taskPullRequestProjectContext(c)
		if err != nil || (hasProject && projectID == "") {
			return h.respondMutation(c, args.TaskID, taskCardPullRequestNotFound)
		}
		args.ProjectID = projectID
	}
	out, err := actPullRequest(h, c.Request().Context(), args)
	var status *echo.HTTPError
	if errors.As(err, &status) && status.Code == http.StatusNotFound {
		if args.FromTaskCard {
			return h.respondMutation(c, args.TaskID, taskCardPullRequestNotFound)
		}
		return h.respondMutation(c, args.TaskID, taskPullRequestFailure(false, "Task not found"))
	}
	if err != nil {
		return err
	}
	return h.respondMutation(c, args.TaskID, out)
}

var actPullRequest = defineAction("task.pullrequest", func(h *Handler, ctx context.Context, a pullRequestArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}, func(h *Handler, ctx context.Context, a pullRequestArgs) (mutationOutcome, error) {
	task, err := h.taskSvc.GetByID(ctx, a.TaskID)
	if err != nil || task == nil {
		if a.FromTaskCard {
			return taskCardPullRequestNotFound, nil
		}
		return taskPullRequestFailure(false, "Task not found"), nil
	}
	if a.ProjectID != "" && task.ProjectID != a.ProjectID {
		return taskCardPullRequestNotFound, nil
	}
	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return taskPullRequestFailure(a.FromTaskCard, "Project has no repository path configured"), nil
	}
	h.recoverTaskWorktreeState(ctx, task, project)
	if task.WorktreeBranch == "" {
		return taskPullRequestFailure(a.FromTaskCard, "Task has no worktree branch"), nil
	}
	if h.githubSvc == nil {
		out := taskPullRequestFailure(a.FromTaskCard, "GitHub integration is not configured")
		if !a.FromTaskCard {
			out.LinkURL, out.LinkText = "/channels", "Open Channels"
		}
		return out, nil
	}
	if h.taskPullRequestRepo == nil {
		return taskPullRequestFailure(a.FromTaskCard, "Task pull request repository not available"), nil
	}

	var eligibilityReason string
	result, mutationErr := h.newTaskPullRequestService().OpenForTaskValidated(ctx, project, task, service.OpenTaskPullRequestOptions{
		CommitMessage: h.buildPullRequestPrepCommitMessage(ctx, task),
		TargetRepoURL: a.TargetRepo,
		Base:          a.Base,
	}, func() (*models.Project, *models.Task, error) {
		currentTask, loadErr := h.taskSvc.GetByID(ctx, a.TaskID)
		if loadErr != nil || currentTask == nil {
			eligibilityReason = "Task not found."
			return nil, nil, errTaskMutationEligibilityChanged
		}
		if currentTask.ProjectID != project.ID {
			eligibilityReason = "Task project eligibility changed."
			return nil, nil, errTaskMutationEligibilityChanged
		}
		currentProject, loadErr := h.projectRepo.GetByID(ctx, currentTask.ProjectID)
		if loadErr != nil || currentProject == nil || strings.TrimSpace(currentProject.RepoPath) == "" {
			eligibilityReason = "Task project repository eligibility changed."
			return nil, nil, errTaskMutationEligibilityChanged
		}
		if !service.SameRepositoryMutationScope(project.RepoPath, currentProject.RepoPath) {
			eligibilityReason = "Task project repository eligibility changed."
			return nil, nil, errTaskMutationEligibilityChanged
		}
		if currentTask.Status == models.StatusRunning {
			eligibilityReason = "Wait for the task to finish before creating a PR."
			return nil, nil, errTaskMutationEligibilityChanged
		}
		h.recoverTaskWorktreeState(ctx, currentTask, currentProject)
		if a.FromTaskCard {
			if eligible, reason := h.taskCardPullRequestEligibility(currentTask, currentProject); !eligible {
				eligibilityReason = reason
				return nil, nil, errTaskMutationEligibilityChanged
			}
		}
		return currentProject, currentTask, nil
	})
	if errors.Is(mutationErr, errTaskMutationEligibilityChanged) {
		return failText(http.StatusConflict, eligibilityReason), nil
	}
	if mutationErr != nil {
		return taskPullRequestFailure(a.FromTaskCard, formatTaskPullRequestError(mutationErr)), nil
	}

	msg := fmt.Sprintf("GitHub PR reused (#%d)", result.PullRequest.Number)
	if result.ReusedExistingRecord {
		msg = fmt.Sprintf("GitHub PR already exists (#%d)", result.PullRequest.Number)
	} else if result.Created {
		msg = fmt.Sprintf("GitHub PR created (#%d)", result.PullRequest.Number)
	}
	view := viewChanges
	if a.FromTaskCard {
		view = viewBoard
	}
	return mutationOutcome{View: view, Message: msg, Toast: "success", ToastAlways: true}, nil
})

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
		commitCtx.DiffSummary = h.llmSvc.SummarizeWorktreeCommitDiffForAgentID(service.WithDirectUsageProject(ctx, task.ProjectID), task.WorktreePath, *task.AgentID, commitCtx)
	}
	return service.BuildWorktreeCommitMessage(task.WorktreePath, commitCtx)
}

type taskConflictRecoveryPreflight struct {
	task           *models.Task
	project        *models.Project
	eligibility    taskMergeEligibility
	fromChangesTab bool
}

// preflightTaskConflictRecovery loads the shared initial context for task-owned
// conflict recovery before Resolve and Abort perform their distinct mutations.
// afterTaskLoad preserves any operation-specific checks that must run after a
// task is found but before repository recovery begins.
func (h *Handler) preflightTaskConflictRecovery(ctx context.Context, taskID string, fromChangesTab bool, afterTaskLoad func(*models.Task) error) (*taskConflictRecoveryPreflight, error) {
	task, err := h.taskSvc.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if afterTaskLoad != nil {
		if err := afterTaskLoad(task); err != nil {
			return nil, err
		}
	}

	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "project has no repo path")
	}
	h.recoverTaskWorktreeState(ctx, task, project)
	eligibility := h.resolveTaskMergeEligibility(ctx, task, project, h.reconcileAlreadyMergedBranch(ctx, task))

	return &taskConflictRecoveryPreflight{
		task:           task,
		project:        project,
		eligibility:    eligibility,
		fromChangesTab: fromChangesTab,
	}, nil
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

type conflictArgs struct {
	TaskID         string
	FromChangesTab bool
}

func ownConflict(h *Handler, ctx context.Context, a conflictArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}

func parseConflictArgs(c echo.Context) conflictArgs {
	return conflictArgs{TaskID: c.Param("taskId"), FromChangesTab: c.FormValue("merge_source") == "changes_tab"}
}

func conflictFailure(fromChangesTab bool, status int, message string) mutationOutcome {
	if fromChangesTab {
		return failView(viewChanges, message)
	}
	return failText(status, message)
}

// ResolveTaskConflicts triggers AI-assisted conflict resolution.
func (h *Handler) ResolveTaskConflicts(c echo.Context) error {
	args := parseConflictArgs(c)
	out, err := actResolveConflicts(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	return h.respondMutation(c, args.TaskID, out)
}

var actResolveConflicts = defineAction("task.conflicts.resolve", ownConflict, func(h *Handler, ctx context.Context, a conflictArgs) (mutationOutcome, error) {
	preflight, err := h.preflightTaskConflictRecovery(ctx, a.TaskID, a.FromChangesTab, func(*models.Task) error {
		if h.worktreeSvc == nil {
			return statusErr(http.StatusInternalServerError, "worktree service not available")
		}
		return nil
	})
	if err != nil {
		return mutationOutcome{}, err
	}
	task := preflight.task
	if !preflight.eligibility.ConflictRecovery {
		out := conflictFailure(a.FromChangesTab, http.StatusConflict, "No active merge conflicts remain. Merge actions have been refreshed.")
		out.Toast = "info"
		return out, nil
	}

	result, resolveErr := h.worktreeSvc.ResolveConflictsWithAIValidated(ctx, task, preflight.project.RepoPath, func() error {
		_, err := h.revalidateTaskConflictRecovery(ctx, a.TaskID, task, preflight.project)
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
		return conflictFailure(a.FromChangesTab, status, errMessage), nil
	}

	out := mutationOutcome{View: mutationView(false, a.FromChangesTab)}
	if result != nil && !result.Success {
		out.Toast = "failed"
		out.Message = "AI could not resolve all conflicts. Resolve conflicts manually or abort the merge."
		if result.ErrorMessage != "" {
			out.Message = result.ErrorMessage
		}
	}
	return out, nil
})

// AbortTaskMerge aborts an in-progress merge for a task.
func (h *Handler) AbortTaskMerge(c echo.Context) error {
	args := parseConflictArgs(c)
	out, err := actAbortMerge(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	return h.respondMutation(c, args.TaskID, out)
}

var actAbortMerge = defineAction("task.conflicts.abort", ownConflict, func(h *Handler, ctx context.Context, a conflictArgs) (mutationOutcome, error) {
	preflight, err := h.preflightTaskConflictRecovery(ctx, a.TaskID, a.FromChangesTab, nil)
	if err != nil {
		return mutationOutcome{}, err
	}
	task := preflight.task
	project := preflight.project
	eligibility := preflight.eligibility
	if !eligibility.ConflictRecovery {
		out := conflictFailure(a.FromChangesTab, http.StatusConflict, "No active merge remains. Merge actions have been refreshed.")
		out.Toast = "info"
		return out, nil
	}

	abortBranch := task.WorktreeBranch
	abortTarget := eligibility.TargetBranch
	abortErr := h.worktreeSvc.AbortMergeForTaskValidated(ctx, a.TaskID, project.RepoPath, abortBranch, abortTarget, models.MergeStatusPending, func() error {
		freshEligibility, err := h.revalidateTaskConflictRecovery(ctx, a.TaskID, task, project)
		if err != nil {
			return err
		}
		if task.WorktreeBranch != abortBranch || freshEligibility.TargetBranch != abortTarget {
			return fmt.Errorf("%w: task conflict metadata changed before abort", service.ErrMergeEligibilityChanged)
		}
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
		return conflictFailure(a.FromChangesTab, status, errMessage), nil
	}
	return mutationOutcome{View: mutationView(false, a.FromChangesTab)}, nil
})

// CleanupTaskWorktree removes the worktree for a task.
func (h *Handler) CleanupTaskWorktree(c echo.Context) error {
	taskID := c.Param("taskId")
	deleteBranch := c.FormValue("delete_branch") == "on" || c.FormValue("delete_branch") == "true"
	if _, err := actCleanupWorktree(h, c.Request().Context(), cleanupWorktreeArgs{TaskID: taskID, DeleteBranch: deleteBranch}); err != nil {
		return err
	}
	return h.respondMutation(c, taskID, mutationOutcome{View: viewWorktree})
}

type cleanupWorktreeArgs struct {
	TaskID       string
	DeleteBranch bool
}

var actCleanupWorktree = defineAction("task.worktree.cleanup", func(h *Handler, ctx context.Context, a cleanupWorktreeArgs) (string, authz.Permission, error) {
	return editTask(h, ctx, a.TaskID)
}, func(h *Handler, ctx context.Context, a cleanupWorktreeArgs) (struct{}, error) {
	task, err := h.loadTask(ctx, a.TaskID)
	if err != nil {
		return struct{}{}, err
	}
	if h.worktreeSvc == nil {
		return struct{}{}, statusErr(http.StatusInternalServerError, "worktree service not available")
	}
	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return struct{}{}, statusErr(http.StatusBadRequest, "project has no repo path")
	}
	if err := h.worktreeSvc.CleanupWorktree(ctx, task, project.RepoPath, a.DeleteBranch); err != nil {
		applog.Infof("[handler] CleanupTaskWorktree error: %v", err)
		return struct{}{}, statusErr(http.StatusInternalServerError, err.Error())
	}
	return struct{}{}, nil
})

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
	ctx := c.Request().Context()
	var info worktreeInfo
	if remote, err := h.remoteAct(ctx, task.ID, "worktree.info", task.ID, &info); remote {
		if err != nil {
			return err
		}
		return render(c, http.StatusOK, pages.WorktreeInfoPanel(info.Task, info.FileStats))
	}
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
