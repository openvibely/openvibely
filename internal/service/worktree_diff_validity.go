package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

// WorktreeDiffIsEmpty verifies emptiness without generating a whole diff. Errors,
// missing worktrees, and cancellation must never be mistaken for an empty diff.
func WorktreeDiffIsEmpty(ctx context.Context, repoDir, targetBranch, worktreePath string) (bool, error) {
	key := canonicalRepositoryMutationKey(ctx, repoDir)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	value, _ := worktreeLifecycleLocks.LoadOrStore(key, &sync.RWMutex{})
	if !diffReadLock(ctx, value.(*sync.RWMutex)) {
		return false, ctx.Err()
	}
	defer value.(*sync.RWMutex).RUnlock()
	if worktreePath == "" || targetBranch == "" {
		return false, fmt.Errorf("worktree and target required")
	}
	if !isGitRepoDir(ctx, worktreePath) {
		return false, fmt.Errorf("worktree unavailable")
	}
	base, err := diffGitOutput(ctx, worktreePath, "merge-base", targetBranch, "HEAD")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(base)) == "" {
		return false, fmt.Errorf("merge base unavailable")
	}
	cmd := diffGitCommand(ctx, "diff", "--quiet", strings.TrimSpace(string(base)), "--")
	cmd.Dir = worktreePath
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	untracked, err := diffGitOutput(ctx, worktreePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	return len(untracked) == 0, nil
}

type diffInvalidator interface {
	InvalidateDiffOutput(context.Context, string) error
}

// InvalidateEmptyTaskDiff is for successful execution finalization only, never
// periodic snapshots. A recorded merge keeps integrated work reviewable; equal
// branch tips alone do not prove that a merge happened.
func InvalidateEmptyTaskDiff(ctx context.Context, store diffInvalidator, task *models.Task, execID, repoDir string) error {
	if task == nil || task.WorktreePath == "" || task.WorktreeBranch == "" || repoDir == "" || task.MergeStatus == models.MergeStatusMerged {
		return nil
	}
	target := task.MergeTargetBranch
	if target == "" {
		target = GetDefaultBranchContext(ctx, repoDir)
	}
	empty, err := WorktreeDiffIsEmpty(ctx, repoDir, target, task.WorktreePath)
	if err != nil || !empty {
		return err
	}
	if store == nil {
		return fmt.Errorf("diff store unavailable")
	}
	return store.InvalidateDiffOutput(ctx, execID)
}
