package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type mergeTaskToolInput struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	Action string `json:"action"`
}

type mergeTaskToolResult struct {
	OK            bool     `json:"ok"`
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	Action        string   `json:"action"`
	Result        string   `json:"result"`
	TargetBranch  string   `json:"target_branch,omitempty"`
	MergeCommit   string   `json:"merge_commit,omitempty"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
	Message       string   `json:"message,omitempty"`
}

var mergeTaskToolMergeTypes = map[string]string{
	"merge":        "merge",
	"squash":       "squash",
	"fast_forward": "ff",
}

func (h *Handler) executeMergeTaskTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	var req mergeTaskToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", fmt.Errorf("merge_task: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "merge"
	}
	if _, ok := mergeTaskToolMergeTypes[action]; !ok && action != "rebase" {
		return "", fmt.Errorf("merge_task: unsupported action %q", req.Action)
	}
	if h.worktreeSvc == nil {
		return "", fmt.Errorf("merge_task: worktree service unavailable")
	}

	taskIDInput := strings.TrimSpace(req.TaskID)
	if taskIDInput == "" && strings.TrimSpace(req.Title) == "" && params.IsTaskFollowup && params.TaskID != "" {
		taskIDInput = "current"
	}
	taskID, err := h.resolveTaskIDForTool(ctx, params, taskIDInput, req.Title)
	if err != nil {
		return "", fmt.Errorf("merge_task: %w", err)
	}

	var result mergeTaskToolResult
	if action == "rebase" {
		result = h.rebaseTaskFromChat(ctx, taskID, params.ProjectID)
	} else {
		result = h.mergeTaskFromChat(ctx, taskID, params.ProjectID, mergeTaskToolMergeTypes[action])
	}
	result.Action = action
	b, err := json.Marshal(result)
	return string(b), err
}

// Chat uses the task-card policy (project ownership, worktree lock, and per-mode
// checks) because it acts on the user's behalf without seeing the live board.
func (h *Handler) mergeTaskFromChat(ctx context.Context, taskID, projectID, mergeType string) mergeTaskToolResult {
	preflight, err := h.loadTaskBranchMutation(ctx, taskID, projectID, true)
	if err != nil {
		return mergeTaskToolResult{TaskID: taskID, Result: "not_eligible", Message: "task or project repository not found"}
	}
	options := h.mergeTaskBranchOptions(taskID, projectID, true, mergeType)
	h.refreshTaskBranchMutation(ctx, preflight, options)
	task := preflight.task
	out := mergeTaskToolResult{TaskID: task.ID, Title: task.Title, TargetBranch: preflight.eligibility.TargetBranch}
	if reason := chatBranchMutationRejection(preflight); reason != "" {
		out.Result = "not_eligible"
		if preflight.branchAlreadyMerged || task.MergeStatus == models.MergeStatusMerged {
			out.Result = "already_merged"
		}
		out.Message = reason
		return out
	}

	result, mergeErr := h.worktreeSvc.MergeBranchValidated(ctx, task, preflight.project.RepoPath, mergeType, func() error {
		if err := h.revalidateTaskBranchMutation(ctx, preflight, options); err != nil {
			return err
		}
		*task = *preflight.task
		out.TargetBranch = preflight.eligibility.TargetBranch
		return nil
	})
	if mergeErr != nil {
		return chatBranchMutationFailure(out, result, mergeErr)
	}

	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		out.Result = "conflict"
		out.ConflictFiles = result.ConflictFiles
		out.Message = "Merge had conflicts and was aborted; the target branch is unchanged. Ask the task to rebase onto the target branch and resolve the conflicts."
		if abortErr := h.abortChatMergeConflict(ctx, task, preflight.project, out.TargetBranch); abortErr != nil {
			out.Message = fmt.Sprintf("Merge had conflicts and the automatic abort failed (%v); resolve or abort the merge from the task board before merging anything else.", abortErr)
		}
		return out
	}

	out.OK = true
	out.Result = "merged"
	if result != nil {
		out.MergeCommit = result.MergeCommit
	}
	return out
}

func (h *Handler) rebaseTaskFromChat(ctx context.Context, taskID, projectID string) mergeTaskToolResult {
	preflight, err := h.loadTaskBranchMutation(ctx, taskID, projectID, true)
	if err != nil {
		return mergeTaskToolResult{TaskID: taskID, Result: "not_eligible", Message: "task or project repository not found"}
	}
	options := h.rebaseTaskBranchOptions(taskID, projectID, true)
	h.refreshTaskBranchMutation(ctx, preflight, options)
	task := preflight.task
	out := mergeTaskToolResult{TaskID: task.ID, Title: task.Title, TargetBranch: preflight.eligibility.TargetBranch}
	if reason := chatBranchMutationRejection(preflight); reason != "" {
		out.Result = "not_eligible"
		if preflight.branchAlreadyMerged || task.MergeStatus == models.MergeStatusMerged {
			out.Result = "already_merged"
		}
		out.Message = reason
		return out
	}

	result, rebaseErr := h.worktreeSvc.RebaseBranchValidated(ctx, task, preflight.project.RepoPath, func() error {
		if err := h.revalidateTaskBranchMutation(ctx, preflight, options); err != nil {
			return err
		}
		*task = *preflight.task
		out.TargetBranch = preflight.eligibility.TargetBranch
		return nil
	})
	if rebaseErr != nil {
		return chatBranchMutationFailure(out, nil, rebaseErr)
	}
	if result != nil && !result.Success && len(result.ConflictFiles) > 0 {
		out.Result = "conflict"
		out.ConflictFiles = result.ConflictFiles
		out.Message = "Rebase had conflicts and was aborted. Ask the task to rebase onto the target branch and resolve the conflicts."
		return out
	}

	out.OK = true
	out.Result = "rebased"
	if result != nil && result.UpToDate {
		out.Result = "up_to_date"
	}
	return out
}

func chatBranchMutationRejection(preflight *taskBranchMutationPreflight) string {
	if preflight.cardReason != "" {
		return preflight.cardReason
	}
	if !preflight.operationEligible {
		if preflight.operationReason != "" {
			return preflight.operationReason
		}
		if preflight.eligibility.Reason != "" {
			return preflight.eligibility.Reason
		}
		return "task branch is not currently eligible"
	}
	return ""
}

func chatBranchMutationFailure(out mergeTaskToolResult, result *service.MergeResult, err error) mergeTaskToolResult {
	switch {
	case errors.Is(err, service.ErrMergeInProgress):
		out.Result = "busy"
		out.Message = "Another merge or rebase is in progress for this repository; retry after it finishes."
	case errors.Is(err, service.ErrMergeEligibilityChanged):
		out.Result = "not_eligible"
		out.Message = err.Error()
	default:
		out.Result = "failed"
		out.Message = err.Error()
		if result != nil && strings.TrimSpace(result.ErrorMessage) != "" {
			out.Message = strings.TrimSpace(result.ErrorMessage)
		}
	}
	return out
}

// abortChatMergeConflict backs out a conflicted chat merge so the shared repo is
// not left mid-merge, which would block every other merge until a human intervenes.
func (h *Handler) abortChatMergeConflict(ctx context.Context, task *models.Task, project *models.Project, targetBranch string) error {
	branch := task.WorktreeBranch
	return h.worktreeSvc.AbortMergeForTaskValidated(ctx, task.ID, project.RepoPath, branch, targetBranch, models.MergeStatusConflict, func() error {
		fresh, err := h.revalidateTaskConflictRecovery(ctx, task.ID, task, project)
		if err != nil {
			return err
		}
		if task.WorktreeBranch != branch || fresh.TargetBranch != targetBranch {
			return fmt.Errorf("%w: task conflict metadata changed before abort", service.ErrMergeEligibilityChanged)
		}
		return nil
	})
}
