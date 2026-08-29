package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// WorktreeService manages git worktrees for task isolation.
type WorktreeService struct {
	taskRepo     *repository.TaskRepo
	projectRepo  *repository.ProjectRepo
	settingsRepo *repository.SettingsRepo
	llmSvc       *LLMService
}

func NewWorktreeService(taskRepo *repository.TaskRepo, projectRepo *repository.ProjectRepo, settingsRepo *repository.SettingsRepo) *WorktreeService {
	return &WorktreeService{
		taskRepo:     taskRepo,
		projectRepo:  projectRepo,
		settingsRepo: settingsRepo,
	}
}

// SetLLMService sets the LLM service for AI-assisted conflict resolution.
func (ws *WorktreeService) SetLLMService(llmSvc *LLMService) {
	ws.llmSvc = llmSvc
}

// slugify creates a branch-name-safe slug from a string.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(s, "-")
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}

// IsGitRepo checks if the given directory is inside a git repository.
func IsGitRepo(dir string) bool {
	return IsGitRepoContext(context.Background(), dir)
}

// IsGitRepoContext checks if the given directory is inside a git repository.
func IsGitRepoContext(ctx context.Context, dir string) bool {
	if dir == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// GetDefaultBranch returns the name of the default branch (main or master).
func GetDefaultBranch(repoDir string) string {
	return GetDefaultBranchContext(context.Background(), repoDir)
}

// GetDefaultBranchContext returns the name of the default branch (main or master).
func GetDefaultBranchContext(ctx context.Context, repoDir string) string {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err == nil {
		branch := strings.TrimSpace(string(out))
		// Strip "origin/" prefix
		parts := strings.SplitN(branch, "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
		return branch
	}
	// Fallback: check if main or master branch exists
	for _, name := range []string{"main", "master"} {
		checkCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", name)
		checkCmd.Dir = repoDir
		if checkCmd.Run() == nil {
			return name
		}
	}
	return "main"
}

// GetCurrentBranch returns the current branch name.
func GetCurrentBranch(repoDir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func GitStatusPorcelain(repoDir string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SetupWorktree creates a git worktree for a task.
// For chained tasks with lineage metadata (BaseCommitSHA/BaseBranch), the worktree
// is created from the parent's commit SHA so child tasks inherit parent code changes.
// Returns the worktree path and branch name, or error.
func (ws *WorktreeService) SetupWorktree(ctx context.Context, task *models.Task, repoDir string) (worktreePath string, branchName string, err error) {
	return ws.setupWorktree(ctx, task, repoDir, false)
}

// SetupFollowupWorktree resolves the worktree for a task-thread follow-up. Terminal
// merged/stale tasks continue from the current merge target on a fresh follow-up
// branch instead of trying to merge current target back into the historical branch.
// The returned skipStartupSync flag is true when the new branch was just created
// from the current target and therefore does not need startup auto-sync.
func (ws *WorktreeService) SetupFollowupWorktree(ctx context.Context, task *models.Task, repoDir string) (worktreePath string, branchName string, skipStartupSync bool, err error) {
	if repoDir == "" || !IsGitRepo(repoDir) {
		return "", "", false, fmt.Errorf("not a git repository: %s", repoDir)
	}

	if ws.shouldReuseStoredFollowupWorktree(task, repoDir) {
		applog.Infof("[worktree] reusing stored follow-up worktree task=%s path=%s branch=%s", task.ID, task.WorktreePath, task.WorktreeBranch)
		if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, task.WorktreePath, task.WorktreeBranch); updateErr != nil {
			applog.Infof("[worktree] error updating stored follow-up worktree info: %v", updateErr)
		}
		return task.WorktreePath, task.WorktreeBranch, false, nil
	}

	if ws.shouldContinueFollowupFromCurrentTarget(task, repoDir) {
		wtPath, wtBranch, setupErr := ws.setupWorktree(ctx, task, repoDir, true)
		return wtPath, wtBranch, true, setupErr
	}

	wtPath, wtBranch, setupErr := ws.SetupWorktree(ctx, task, repoDir)
	return wtPath, wtBranch, false, setupErr
}

func (ws *WorktreeService) setupWorktree(ctx context.Context, task *models.Task, repoDir string, continueFromCurrentTarget bool) (worktreePath string, branchName string, err error) {
	if repoDir == "" || !IsGitRepo(repoDir) {
		return "", "", fmt.Errorf("not a git repository: %s", repoDir)
	}

	baseRef := ws.resolveWorktreeBaseRef(ctx, task, repoDir, continueFromCurrentTarget)
	if baseRef == "" {
		return "", "", fmt.Errorf("could not resolve base ref for task %s", task.ID)
	}

	// If this is a chained task and we couldn't resolve lineage, log a clear error
	if !continueFromCurrentTarget && task.ParentTaskID != nil && task.BaseCommitSHA != "" && baseRef != task.BaseCommitSHA {
		applog.Infof("[worktree] WARNING: chained task %s could not use parent lineage SHA %s, using fallback base %s", task.ID, task.BaseCommitSHA, baseRef)
	}

	// Create branch name from task
	slug := slugify(task.Title)
	if slug == "" {
		slug = task.ID[:8]
	}
	branchName = fmt.Sprintf("task/%s-%s", task.ID[:8], slug)
	if continueFromCurrentTarget {
		branchName = fmt.Sprintf("task/%s-followup-%d", task.ID[:8], time.Now().UnixNano())
	}

	// Worktree directory
	worktreePath = filepath.Join(repoDir, ".worktrees", fmt.Sprintf("task_%s", task.ID))
	if continueFromCurrentTarget {
		worktreePath = filepath.Join(repoDir, ".worktrees", fmt.Sprintf("task_%s_followup_%d", task.ID, time.Now().UnixNano()))
	}

	// Check if worktree already exists
	if !continueFromCurrentTarget {
		if storedPath, storedBranch, ok := ws.existingStoredWorktree(task); ok {
			ws.clearStaleConflictStatusIfClean(ctx, task)
			applog.Infof("[worktree] stored worktree already exists at %s, reusing", storedPath)
			if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, storedPath, storedBranch); updateErr != nil {
				applog.Infof("[worktree] error updating worktree info: %v", updateErr)
			}
			return storedPath, storedBranch, nil
		}
		if _, err := os.Stat(worktreePath); err == nil {
			applog.Infof("[worktree] worktree already exists at %s, reusing", worktreePath)
			if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); updateErr != nil {
				applog.Infof("[worktree] error updating worktree info: %v", updateErr)
			}
			return worktreePath, branchName, nil
		}
	}

	// Check if branch already exists
	checkBranch := exec.Command("git", "rev-parse", "--verify", branchName)
	checkBranch.Dir = repoDir
	branchExists := checkBranch.Run() == nil

	if branchExists {
		// Branch exists, create worktree pointing to it
		cmd := exec.Command("git", "worktree", "add", worktreePath, branchName)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", "", fmt.Errorf("creating worktree for existing branch: %w: %s", err, string(out))
		}
	} else {
		// Creating a new branch requires a real commit. Existing task branches and
		// worktrees remain reusable even if their original base was renamed later.
		verifyBase := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", baseRef+"^{commit}")
		verifyBase.Dir = repoDir
		out, verifyErr := verifyBase.CombinedOutput()
		if verifyErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", "", fmt.Errorf("verifying worktree base %q: %w", baseRef, ctxErr)
			}
			if exitErr, ok := verifyErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && strings.TrimSpace(string(out)) == "" {
				return "", "", fmt.Errorf("repository has no commit for worktree base %q; create an initial local commit before running coding tasks", baseRef)
			}
			return "", "", fmt.Errorf("verifying worktree base %q: %w: %s", baseRef, verifyErr, strings.TrimSpace(string(out)))
		}

		// Create new branch from the resolved base ref
		cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath, baseRef)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", "", fmt.Errorf("creating worktree from base %s: %w: %s", baseRef, err, string(out))
		}
	}

	applog.Infof("[worktree] created worktree at %s on branch %s (base: %s) for task %s (lineage_depth=%d)", worktreePath, branchName, baseRef, task.ID, task.LineageDepth)

	// Update task record with worktree info
	if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); updateErr != nil {
		applog.Infof("[worktree] error updating worktree info: %v", updateErr)
	}
	if task.MergeTargetBranch == "" {
		mergeTarget := baseRef
		if !continueFromCurrentTarget && task.BaseBranch != "" {
			// Use parent's branch as merge target so changes merge back correctly.
			mergeTarget = task.BaseBranch
		}
		task.MergeTargetBranch = mergeTarget
		if updateErr := ws.taskRepo.UpdateAutoMerge(ctx, task.ID, task.AutoMerge, mergeTarget); updateErr != nil {
			applog.Infof("[worktree] error setting merge target branch: %v", updateErr)
		}
	}

	return worktreePath, branchName, nil
}

func (ws *WorktreeService) resolveWorktreeBaseRef(ctx context.Context, task *models.Task, repoDir string, continueFromCurrentTarget bool) string {
	if continueFromCurrentTarget {
		baseRef := task.MergeTargetBranch
		if baseRef == "" {
			baseRef = ws.getGlobalMergeTarget(ctx)
		}
		if baseRef == "" {
			baseRef = GetDefaultBranch(repoDir)
		}
		return baseRef
	}

	// Determine the base ref to branch from.
	// Priority for chained tasks: BaseCommitSHA > BaseBranch > MergeTargetBranch > global > default
	baseRef := ""
	if task.BaseCommitSHA != "" {
		// Verify the SHA exists in the repo
		checkSHA := exec.Command("git", "cat-file", "-t", task.BaseCommitSHA)
		checkSHA.Dir = repoDir
		if checkSHA.Run() == nil {
			baseRef = task.BaseCommitSHA
			applog.Infof("[worktree] using lineage commit SHA %s as base for task %s (depth=%d)", baseRef, task.ID, task.LineageDepth)
		} else {
			applog.Infof("[worktree] lineage commit SHA %s not found in repo for task %s, falling back", task.BaseCommitSHA, task.ID)
		}
	}
	if baseRef == "" && task.BaseBranch != "" {
		// Verify the branch exists
		checkBr := exec.Command("git", "rev-parse", "--verify", task.BaseBranch)
		checkBr.Dir = repoDir
		if checkBr.Run() == nil {
			baseRef = task.BaseBranch
			applog.Infof("[worktree] using lineage branch %s as base for task %s (depth=%d)", baseRef, task.ID, task.LineageDepth)
		} else {
			applog.Infof("[worktree] lineage branch %s not found in repo for task %s, falling back", task.BaseBranch, task.ID)
		}
	}

	// Standard fallback chain for non-chained tasks or if lineage refs not found
	if baseRef == "" {
		baseRef = task.MergeTargetBranch
		if baseRef == "" {
			baseRef = ws.getGlobalMergeTarget(ctx)
		}
		if baseRef == "" {
			baseRef = GetDefaultBranch(repoDir)
		}
	}
	return baseRef
}

func (ws *WorktreeService) existingStoredWorktree(task *models.Task) (string, string, bool) {
	if task.WorktreePath == "" || task.WorktreeBranch == "" {
		return "", "", false
	}
	if _, err := os.Stat(task.WorktreePath); err != nil {
		return "", "", false
	}
	return task.WorktreePath, task.WorktreeBranch, true
}

func (ws *WorktreeService) shouldContinueFollowupFromCurrentTarget(task *models.Task, repoDir string) bool {
	if task == nil || !models.IsTerminalStatus(task.Status) {
		return false
	}
	if task.MergeStatus == models.MergeStatusMerged || task.MergeStatus == models.MergeStatusConflict {
		return true
	}
	return task.WorktreeBranch != "" && ws.isBranchTipMergedIntoTarget(repoDir, task.WorktreeBranch, task.MergeTargetBranch)
}

func (ws *WorktreeService) shouldReuseStoredFollowupWorktree(task *models.Task, repoDir string) bool {
	if task == nil || task.WorktreePath == "" || task.WorktreeBranch == "" || !strings.Contains(task.WorktreeBranch, "-followup-") {
		return false
	}
	if _, err := os.Stat(task.WorktreePath); err != nil {
		return false
	}
	status, err := GitStatusPorcelain(task.WorktreePath)
	if err != nil {
		return false
	}
	if strings.TrimSpace(status) != "" {
		return true
	}
	if task.MergeStatus == models.MergeStatusMerged || task.MergeStatus == models.MergeStatusConflict {
		return !ws.branchHasCommitsBeyondTarget(repoDir, task.WorktreeBranch, task.MergeTargetBranch)
	}
	return true
}

func (ws *WorktreeService) isBranchTipMergedIntoTarget(repoDir, branchName, targetBranch string) bool {
	if branchName == "" {
		return false
	}
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}
	cmd := exec.Command("git", "merge-base", "--is-ancestor", branchName, targetBranch)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func (ws *WorktreeService) branchHasCommitsBeyondTarget(repoDir, branchName, targetBranch string) bool {
	if branchName == "" {
		return false
	}
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}
	cmd := exec.Command("git", "rev-list", "--count", fmt.Sprintf("%s..%s", targetBranch, branchName))
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != "0"
}

func worktreeHasActiveMerge(worktreePath string) bool {
	if worktreePath == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--git-path", "MERGE_HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	mergeHeadPath := strings.TrimSpace(string(out))
	if !filepath.IsAbs(mergeHeadPath) {
		mergeHeadPath = filepath.Join(worktreePath, mergeHeadPath)
	}
	_, statErr := os.Stat(mergeHeadPath)
	return statErr == nil
}

func worktreeHasConflictFiles(worktreePath string) bool {
	return len(detectConflicts(worktreePath)) > 0
}

func (ws *WorktreeService) clearStaleConflictStatusIfClean(ctx context.Context, task *models.Task) {
	if task == nil || task.MergeStatus != models.MergeStatusConflict || task.WorktreePath == "" || ws.taskRepo == nil {
		return
	}
	status, err := GitStatusPorcelain(task.WorktreePath)
	if err != nil || strings.TrimSpace(status) != "" || worktreeHasActiveMerge(task.WorktreePath) || worktreeHasConflictFiles(task.WorktreePath) {
		return
	}
	if err := ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
		applog.Infof("[worktree] error clearing stale conflict status for task %s: %v", task.ID, err)
		return
	}
	task.MergeStatus = models.MergeStatusPending
	applog.Infof("[worktree] cleared stale conflict status for task %s after clean aborted merge state", task.ID)
}

// StartupSyncConflictError reports a startup merge conflict that Git aborted
// successfully, leaving the task worktree safe for the follow-up agent to use.
type StartupSyncConflictError struct {
	TargetBranch  string
	TaskBranch    string
	WorktreePath  string
	ConflictFiles []string
}

func (e *StartupSyncConflictError) Error() string {
	return fmt.Sprintf("startup auto-merge conflict while merging %s into %s (conflicts: %s); merge was aborted and conflict resolution is required in %s", e.TargetBranch, e.TaskBranch, strings.Join(e.ConflictFiles, ", "), e.WorktreePath)
}

// StartupSyncConflictContext turns an aborted startup merge conflict into
// recovery instructions for an agent that can continue in the clean,
// preserved task worktree.
func StartupSyncConflictContext(conflict *StartupSyncConflictError) string {
	if conflict == nil {
		return ""
	}
	return fmt.Sprintf("# Worktree Sync Warning\n\nStartup sync could not merge %s into %s because Git reported conflicts in: %s. The merge was aborted before this turn started, so the preserved worktree is clean but may be behind or diverged from %s. Before handling the task, run the merge in %s, resolve the conflicts while preserving both the task changes and current target changes, then build, test, and commit the resolution. Sync error: %v", conflict.TargetBranch, conflict.TaskBranch, strings.Join(conflict.ConflictFiles, ", "), conflict.TargetBranch, conflict.WorktreePath, conflict)
}

// SyncWorktreeFromMainAtStart updates a task branch with the latest local
// merge target/default branch before task execution begins. It only runs when
// the worktree is clean and does not implicitly fetch or merge remote branches.
func (ws *WorktreeService) SyncWorktreeFromMainAtStart(ctx context.Context, task *models.Task, repoDir string) error {
	if task == nil || task.WorktreePath == "" {
		return nil
	}

	runGit := func(dir string, args ...string) ([]byte, error) {
		cmdCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cmdCtx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS=true",
			"SSH_ASKPASS=true",
		)
		return cmd.CombinedOutput()
	}

	currentBranch := GetCurrentBranch(task.WorktreePath)
	if currentBranch == "" {
		currentBranch = task.WorktreeBranch
	}
	applog.Infof("[worktree] startup auto-merge check task=%s worktree=%s branch=%s", task.ID, task.WorktreePath, currentBranch)

	statusOut, statusErr := runGit(task.WorktreePath, "status", "--porcelain")
	if statusErr != nil {
		applog.Infof("[worktree] startup auto-merge failed task=%s unable to read git status: %v", task.ID, statusErr)
		if ws.taskRepo != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		}
		return fmt.Errorf("could not check worktree status in %s: %w", task.WorktreePath, statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		applog.Infof("[worktree] startup auto-merge skipped task=%s branch=%s reason=dirty_worktree", task.ID, currentBranch)
		return nil
	}
	ws.clearStaleConflictStatusIfClean(ctx, task)

	syncBranch := task.MergeTargetBranch
	if syncBranch == "" {
		if _, err := runGit(repoDir, "show-ref", "--verify", "--quiet", "refs/heads/main"); err == nil {
			syncBranch = "main"
		} else {
			syncBranch = GetDefaultBranch(repoDir)
		}
	}

	mergeSource := syncBranch
	applog.Infof("[worktree] startup auto-merge task=%s using local %s", task.ID, mergeSource)

	mergeOut, mergeErr := runGit(task.WorktreePath, "merge", "--no-edit", mergeSource)
	mergeMsg := strings.TrimSpace(string(mergeOut))
	if mergeErr != nil {
		conflictFiles := detectConflicts(task.WorktreePath)
		if len(conflictFiles) > 0 {
			abortErr := AbortMerge(task.WorktreePath)
			if ws.taskRepo != nil {
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
			}
			conflictErr := &StartupSyncConflictError{
				TargetBranch:  mergeSource,
				TaskBranch:    currentBranch,
				WorktreePath:  task.WorktreePath,
				ConflictFiles: append([]string(nil), conflictFiles...),
			}
			if abortErr != nil {
				err := fmt.Errorf("%s; additionally, git merge --abort failed: %v", conflictErr, abortErr)
				applog.Infof("[worktree] startup auto-merge failed task=%s reason=conflict details=%s", task.ID, err)
				return err
			}
			applog.Infof("[worktree] startup auto-merge failed task=%s reason=conflict details=%s", task.ID, conflictErr)
			return conflictErr
		}

		if ws.taskRepo != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		}
		if mergeMsg == "" {
			mergeMsg = mergeErr.Error()
		}
		applog.Infof("[worktree] startup auto-merge failed task=%s branch=%s source=%s error=%s", task.ID, currentBranch, mergeSource, mergeMsg)
		return fmt.Errorf("startup auto-merge failed while merging %s into %s: %s", mergeSource, currentBranch, mergeMsg)
	}

	if mergeMsg == "" {
		mergeMsg = "already up to date"
	}
	applog.Infof("[worktree] startup auto-merge ran task=%s branch=%s source=%s result=%s", task.ID, currentBranch, mergeSource, mergeMsg)
	return nil
}

type WorktreeCommitPhase string

const (
	WorktreeCommitPhaseInitial  WorktreeCommitPhase = "initial"
	WorktreeCommitPhaseFollowup WorktreeCommitPhase = "followup"
	WorktreeCommitPhaseMerge    WorktreeCommitPhase = "merge-prep"
)

type WorktreeCommitMessageContext struct {
	Phase       WorktreeCommitPhase
	TaskTitle   string
	TurnIntent  string
	Summary     string
	DiffSummary string
}

// BuildWorktreeCommitMessage builds a descriptive commit message from the
// current uncommitted worktree diff. If an LLM summary produced from the actual
// diff is available, it wins; otherwise the fallback is deterministic from the
// changed paths/statuses and never from stale task text.
func BuildWorktreeCommitMessage(worktreePath string, ctx WorktreeCommitMessageContext) string {
	changes := collectWorktreeCommitChanges(worktreePath)
	diffSummaries := summarizeCommitIntents(ctx.DiffSummary)

	if len(changes) > 0 {
		return summarizeCommitChanges(changes, diffSummaries)
	}
	if len(diffSummaries) > 0 {
		return diffSummaries[0]
	}
	return fallbackCommitSummary(ctx.Phase)
}

type worktreeCommitChange struct {
	Path   string
	Status string
}

func collectWorktreeCommitChanges(worktreePath string) []worktreeCommitChange {
	status := collectWorktreeCommitStatus(worktreePath)
	if status == "" {
		return nil
	}
	changes := make([]worktreeCommitChange, 0)
	for _, line := range strings.Split(status, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		status := strings.TrimSpace(line[:minInt(2, len(line))])
		path := strings.TrimSpace(line[minInt(3, len(line)):])
		if strings.Contains(path, " -> ") {
			parts := strings.Split(path, " -> ")
			path = parts[len(parts)-1]
		}
		path = strings.Trim(path, `"`)
		if path == "" {
			continue
		}
		changes = append(changes, worktreeCommitChange{Path: path, Status: status})
	}
	return changes
}

func collectWorktreeCommitStatus(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}

func BuildWorktreeCommitDiffContext(worktreePath string) string {
	status := collectWorktreeCommitStatus(worktreePath)
	if status == "" {
		return ""
	}
	parts := []string{"Changed files and statuses:\n" + status}
	if diff := collectGitDiff(worktreePath, "diff", "--unified=3", "--", "."); diff != "" {
		parts = append(parts, "Diff hunks:\n"+diff)
	}
	if stagedDiff := collectGitDiff(worktreePath, "diff", "--cached", "--unified=3", "--", "."); stagedDiff != "" {
		parts = append(parts, "Staged diff hunks:\n"+stagedDiff)
	}
	if untracked := collectUntrackedFileSnippets(worktreePath, status); untracked != "" {
		parts = append(parts, "Untracked file snippets:\n"+untracked)
	}
	return truncateCommitDiffContext(strings.Join(parts, "\n\n"))
}

func collectGitDiff(worktreePath string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectUntrackedFileSnippets(worktreePath string, status string) string {
	const maxFiles = 5
	snippets := make([]string, 0)
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "??") {
			continue
		}
		path := strings.TrimSpace(line[minInt(3, len(line)):])
		path = strings.Trim(path, `"`)
		if path == "" || strings.Contains(path, "..") || filepath.IsAbs(path) {
			continue
		}
		fullPath := filepath.Join(worktreePath, path)
		info, err := os.Lstat(fullPath)
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64*1024 {
			continue
		}
		if !isResolvedPathInside(worktreePath, fullPath) {
			continue
		}
		data, err := os.ReadFile(fullPath)
		if err != nil || !looksTextForCommitSummary(data) {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		snippets = append(snippets, fmt.Sprintf("--- %s ---\n%s", path, truncateCommitSnippet(content, 2000)))
		if len(snippets) >= maxFiles {
			break
		}
	}
	return strings.Join(snippets, "\n\n")
}

func isResolvedPathInside(root string, path string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func looksTextForCommitSummary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func truncateCommitSnippet(value string, max int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max])
}

func truncateCommitDiffContext(value string) string {
	const maxLen = 12000
	value = strings.TrimSpace(value)
	if len(value) <= maxLen {
		return value
	}
	return strings.TrimSpace(value[:maxLen]) + "\n[diff truncated]"
}

func summarizeCommitIntents(values ...string) []string {
	summaries := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		for _, summary := range summarizeCommitIntentLines(value) {
			key := strings.ToLower(summary)
			if seen[key] {
				continue
			}
			seen[key] = true
			summaries = append(summaries, summary)
		}
	}
	return summaries
}

func summarizeCommitIntentLines(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	for _, prefix := range []string{"IMPORTANT:", "TASK CREATION:", "---", "RESPONSE FORMAT REQUIREMENT:"} {
		if idx := strings.Index(value, prefix); idx > 0 {
			value = strings.TrimSpace(value[:idx])
		}
	}
	summaries := make([]string, 0, 1)
	for _, line := range strings.Split(value, "\n") {
		line = cleanCommitSubject(line)
		if line == "" || isCommitSubjectBoilerplate(line) {
			continue
		}
		summaries = append(summaries, truncateCommitSubject(line))
		if len(summaries) >= 3 {
			break
		}
	}
	return summaries
}

func cleanCommitSubject(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "`*_#:-. ")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"please ", "can you ", "could you ", "would you ", "task: ", "title: "} {
		if strings.HasPrefix(lower, prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			lower = strings.ToLower(value)
		}
	}
	value = stripConventionalCommitPrefix(value)
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return ""
	}
	return uppercaseInitial(value)
}

func isCommitSubjectBoilerplate(value string) bool {
	trimmed := strings.TrimSpace(value)
	if regexp.MustCompile(`(?i)^\[status:\s*(success|failed|needs_followup)(\s*\|[^\]]*)?\]$`).MatchString(trimmed) {
		return true
	}
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "status:") ||
		strings.HasPrefix(lower, "using tool") ||
		strings.HasPrefix(lower, "tool ") ||
		strings.HasPrefix(lower, "create_task") ||
		strings.HasPrefix(lower, "changed files:") ||
		strings.HasPrefix(lower, "files changed:") ||
		strings.HasPrefix(lower, "modified files:") ||
		strings.HasPrefix(lower, "status failed") ||
		strings.HasPrefix(lower, "status success") ||
		strings.HasPrefix(lower, "status needs_followup")
}

func uppercaseInitial(value string) string {
	if value == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(value)
	if first == utf8.RuneError && size == 0 {
		return value
	}
	return string(unicode.ToUpper(first)) + value[size:]
}

func truncateCommitSubject(value string) string {
	const maxLen = 72
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= maxLen {
		return value
	}
	cut := strings.LastIndex(value[:maxLen], " ")
	if cut < 40 {
		cut = maxLen
	}
	return strings.TrimSpace(value[:cut])
}

func fallbackCommitSummary(phase WorktreeCommitPhase) string {
	switch phase {
	case WorktreeCommitPhaseFollowup:
		return "Refine changes"
	case WorktreeCommitPhaseMerge:
		return "Prepare changes for merge"
	default:
		return "Update changes"
	}
}

func stripConventionalCommitPrefix(value string) string {
	trimmed := strings.TrimSpace(value)
	stripped := regexp.MustCompile(`(?i)^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([^)]+\))?!?:\s+`).ReplaceAllString(trimmed, "")
	if stripped == "" {
		return trimmed
	}
	return strings.TrimSpace(stripped)
}

func summarizeCommitChanges(changes []worktreeCommitChange, diffSummaries []string) string {
	if len(changes) == 0 {
		return ""
	}
	if len(diffSummaries) > 0 {
		return diffSummaries[0]
	}
	verb := commitChangeVerb(changes)
	if len(changes) == 1 {
		return fmt.Sprintf("%s %s", verb, changeLabel(changes[0].Path))
	}
	return fmt.Sprintf("%s %s", verb, commonChangeLabel(changes))
}

func commitChangeVerb(changes []worktreeCommitChange) string {
	verb := "Update"
	for _, change := range changes {
		if strings.HasPrefix(change.Status, "A") || strings.HasPrefix(change.Status, "??") {
			return "Add"
		}
		if strings.HasPrefix(change.Status, "D") {
			verb = "Remove"
		}
	}
	return verb
}

func commonChangeLabel(changes []worktreeCommitChange) string {
	if len(changes) == 0 {
		return "changes"
	}
	if label := commonDirectoryLabel(changes); label != "" {
		return label
	}
	if label := commonBaseWordLabel(changes); label != "" {
		return label
	}
	return fmt.Sprintf("%d files", len(changes))
}

func commonDirectoryLabel(changes []worktreeCommitChange) string {
	firstDir := filepath.Dir(changes[0].Path)
	if firstDir == "." || firstDir == "" {
		return ""
	}
	for _, change := range changes[1:] {
		if filepath.Dir(change.Path) != firstDir {
			return ""
		}
	}
	return fmt.Sprintf("%s files", pathLabel(firstDir))
}

func commonBaseWordLabel(changes []worktreeCommitChange) string {
	counts := make(map[string]int)
	for _, change := range changes {
		seenForPath := make(map[string]bool)
		for _, token := range pathTokens(change.Path) {
			if seenForPath[token] {
				continue
			}
			seenForPath[token] = true
			counts[token]++
		}
	}
	for _, token := range pathTokens(changes[0].Path) {
		if counts[token] == len(changes) {
			return pluralizeCommitLabel(token)
		}
	}
	return ""
}

func changeLabel(path string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "files"
	}
	lowerBase := strings.ToLower(base)
	if lowerBase == "readme.md" {
		return "README.md"
	}
	if lowerBase == "go.mod" || lowerBase == "go.sum" || lowerBase == "makefile" {
		return base
	}
	ext := strings.ToLower(filepath.Ext(base))
	name := strings.TrimSuffix(base, filepath.Ext(base))
	label := pathLabel(name)
	if label == "" {
		return base
	}
	if label == "tests" {
		if parent := pathLabel(filepath.Base(filepath.Dir(path))); parent != "" && parent != "tests" {
			label = parent + " " + label
		}
	}
	if ext == ".templ" && !strings.HasSuffix(label, " template") {
		label += " template"
	}
	return label
}

func pathLabel(value string) string {
	words := pathWords(value)
	if len(words) == 0 {
		return ""
	}
	if len(words) > 1 && words[len(words)-1] == "test" {
		words = append(words[:len(words)-1], "tests")
	}
	return strings.Join(words, " ")
}

func pathWords(value string) []string {
	parts := splitPathTokens(value)
	words := make([]string, 0, len(parts))
	for _, part := range parts {
		if isPathTokenNoise(part) {
			continue
		}
		words = append(words, part)
	}
	return words
}

func pathTokens(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	tokens := make([]string, 0)
	for _, token := range splitPathTokens(path) {
		if !isPathTokenNoise(token) {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func splitPathTokens(value string) []string {
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) >= 3 {
			tokens = append(tokens, field)
		}
	}
	return tokens
}

func isPathTokenNoise(value string) bool {
	switch value {
	case "go", "mod", "sum", "md", "templ", "tmpl", "html", "ts", "tsx", "js", "jsx", "css", "json", "yaml", "yml":
		return true
	default:
		return false
	}
}

func pluralizeCommitLabel(value string) string {
	if value == "" {
		return "files"
	}
	if strings.HasSuffix(value, "s") {
		return value
	}
	return value + "s"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// CommitWorktreeChanges stages and commits all changes in the worktree.
func CommitWorktreeChanges(worktreePath string, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("empty commit message")
	}

	// Check for changes
	out, err := GitStatusPorcelain(worktreePath)
	if err != nil {
		return fmt.Errorf("checking git status: %w", err)
	}
	if len(strings.TrimSpace(out)) == 0 {
		return nil // no changes
	}

	// Ensure git identity is set (required for commits). Check email and name
	// independently because a repo/environment may configure only one of them.
	checkEmailCmd := exec.Command("git", "config", "user.email")
	checkEmailCmd.Dir = worktreePath
	if out, _ := checkEmailCmd.Output(); len(strings.TrimSpace(string(out))) == 0 {
		exec.Command("git", "-C", worktreePath, "config", "user.email", "bot@openvibely.ai").Run()
	}
	checkNameCmd := exec.Command("git", "config", "user.name")
	checkNameCmd.Dir = worktreePath
	if out, _ := checkNameCmd.Output(); len(strings.TrimSpace(string(out))) == 0 {
		exec.Command("git", "-C", worktreePath, "config", "user.name", "OpenVibely Bot").Run()
	}

	// Stage all changes
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = worktreePath
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("staging changes: %w: %s", err, string(out))
	}

	// Commit
	commitCmd := exec.Command("git", "commit", "-m", message)
	commitCmd.Dir = worktreePath
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("committing changes: %w: %s", err, string(out))
	}

	return nil
}

// MergeResult holds the result of a merge operation.
type MergeResult struct {
	Success       bool
	MergeCommit   string
	ConflictFiles []string
	ErrorMessage  string
}

// RebaseResult holds the result of rebasing a task branch onto its target branch.
type RebaseResult struct {
	Success       bool
	UpToDate      bool
	RebasedHead   string
	ConflictFiles []string
	ErrorMessage  string
}

// MergeBranch merges the task branch into the target branch.
// mergeType: "merge" (merge commit), "ff" (fast-forward only), "squash"
func (ws *WorktreeService) MergeBranch(ctx context.Context, task *models.Task, repoDir string, mergeType string) (*MergeResult, error) {
	if task.WorktreeBranch == "" {
		return nil, fmt.Errorf("task has no worktree branch")
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}

	// First, commit any uncommitted changes in the worktree for merge modes
	// that create a merge/squash commit. Fast-forward task-worktree merges must
	// reject dirty worktrees so the rebase and ref update operate on committed
	// task branch state only.
	if task.WorktreePath != "" && mergeType != "ff" {
		commitCtx := WorktreeCommitMessageContext{
			Phase:     WorktreeCommitPhaseMerge,
			TaskTitle: task.Title,
		}
		if ws.llmSvc != nil && task.AgentID != nil {
			commitCtx.DiffSummary = ws.llmSvc.SummarizeWorktreeCommitDiffForAgentID(ctx, task.WorktreePath, *task.AgentID, commitCtx)
		}
		message := BuildWorktreeCommitMessage(task.WorktreePath, commitCtx)
		var err error
		if ws.llmSvc != nil {
			err = ws.llmSvc.CommitTaskWorktreeChanges(ctx, task, nil, task.WorktreePath, message)
		} else {
			err = CommitWorktreeChanges(task.WorktreePath, message)
		}
		if err != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: err.Error()}, fmt.Errorf("auto-commit before merge failed: %w", err)
		}
	}

	// Update merge status to pending
	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)

	if mergeType == "ff" && task.WorktreePath != "" {
		return ws.fastForwardTaskWorktreeToTarget(ctx, task, repoDir, targetBranch)
	}

	var stagedBeforeSquash map[string]bool
	if mergeType == "squash" {
		var err error
		stagedBeforeSquash, err = StagedPaths(repoDir)
		if err != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: fmt.Sprintf("checking staged files before squash: %s", err.Error())}, fmt.Errorf("checking staged files before squash: %w", err)
		}
	}

	// Checkout target branch in the main repo
	checkoutCmd := exec.Command("git", "checkout", targetBranch)
	checkoutCmd.Dir = repoDir
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("checkout target: %s", string(out))}, fmt.Errorf("checkout target branch: %w", err)
	}
	targetBeforeMerge, _ := gitCommitSHA(repoDir, "HEAD")

	// Build merge command based on type
	var mergeArgs []string
	switch mergeType {
	case "ff":
		mergeArgs = []string{"merge", "--ff-only", task.WorktreeBranch}
	case "squash":
		mergeArgs = []string{"merge", "--squash", task.WorktreeBranch}
	default:
		mergeArgs = []string{"merge", "--no-ff", "-m", fmt.Sprintf("Merge task: %s", task.Title), task.WorktreeBranch}
	}

	mergeCmd := exec.Command("git", mergeArgs...)
	mergeCmd.Dir = repoDir
	mergeOut, mergeErr := mergeCmd.CombinedOutput()

	if mergeErr != nil {
		// Check if it's a conflict
		conflictFiles := detectConflicts(repoDir)
		if len(conflictFiles) > 0 {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
			return &MergeResult{
				ConflictFiles: conflictFiles,
				ErrorMessage:  string(mergeOut),
			}, nil
		}
		mergeErrMsg := strings.TrimSpace(string(mergeOut))
		if mergeType == "ff" && strings.Contains(strings.ToLower(mergeErrMsg), "not possible to fast-forward") {
			mergeErrMsg = fmt.Sprintf("fast-forward merge requires branch update from %s. The app attempted to auto-rebase when possible. If this task has no worktree path or still failed, open the task worktree and rebase onto %s, then retry", targetBranch, targetBranch)
		}
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: mergeErrMsg}, fmt.Errorf("merge failed: %w", mergeErr)
	}

	// For squash merge, commit only the paths introduced by the squash result.
	// This allows unrelated user-staged changes to remain staged without being
	// accidentally included in the app-created squash commit.
	if mergeType == "squash" {
		squashPaths, pathErr := SquashMergePaths(repoDir, stagedBeforeSquash)
		if pathErr != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: fmt.Sprintf("checking squash merge paths: %s", pathErr.Error())}, fmt.Errorf("checking squash merge paths: %w", pathErr)
		}
		commitArgs := append([]string{"commit", "-m", fmt.Sprintf("Squash merge task: %s", task.Title), "--only", "--"}, squashPaths...)
		commitCmd := exec.Command("git", commitArgs...)
		commitCmd.Dir = repoDir
		if out, err := commitCmd.CombinedOutput(); err != nil {
			commitErrMsg := strings.TrimSpace(string(out))
			if commitErrMsg == "" {
				commitErrMsg = err.Error()
			}
			if resetErr := ResetSquashMergeChanges(repoDir, squashPaths); resetErr != nil {
				commitErrMsg = fmt.Sprintf("%s; additionally failed to restore squash merge changes: %v", commitErrMsg, resetErr)
			}
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: fmt.Sprintf("squash commit failed: %s", commitErrMsg)}, fmt.Errorf("squash commit failed: %w", err)
		}
	}

	// Get merge commit hash
	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashCmd.Dir = repoDir
	hashOut, _ := hashCmd.Output()
	mergeCommit := strings.TrimSpace(string(hashOut))
	if ws.llmSvc != nil && mergeCommit != "" && mergeCommit != targetBeforeMerge {
		if err := ws.llmSvc.RecordTaskCommitStat(ctx, task, nil, repoDir, mergeCommit); err != nil {
			applog.Infof("[task-commit-stats] error recording app merge commit stat task=%s sha=%s: %v", task.ID, mergeCommit, err)
		}
	}

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)

	return &MergeResult{
		Success:     true,
		MergeCommit: mergeCommit,
	}, nil
}

// RebaseBranch rebases the task worktree branch onto its target branch without merging it.
func (ws *WorktreeService) RebaseBranch(ctx context.Context, task *models.Task, repoDir string) (*RebaseResult, error) {
	if task == nil || task.WorktreeBranch == "" {
		return nil, fmt.Errorf("task has no worktree branch")
	}
	if task.WorktreePath == "" {
		return nil, fmt.Errorf("task has no worktree path")
	}

	targetBranch := task.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}
	if targetBranch == "" {
		targetBranch = "main"
	}

	currentBranchOut, err := gitOutput(task.WorktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		msg := "task worktree must be on the expected task branch before rebasing"
		return &RebaseResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}
	currentBranch := strings.TrimSpace(string(currentBranchOut))
	if currentBranch != task.WorktreeBranch {
		msg := fmt.Sprintf("task worktree is on branch %q, expected %q", currentBranch, task.WorktreeBranch)
		return &RebaseResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}

	statusOut, err := GitStatusPorcelain(task.WorktreePath)
	if err != nil {
		return &RebaseResult{ErrorMessage: fmt.Sprintf("checking task worktree status: %s", err.Error())}, fmt.Errorf("checking task worktree status: %w", err)
	}
	if strings.TrimSpace(statusOut) != "" {
		msg := "task worktree has uncommitted changes; commit or discard them before rebasing"
		return &RebaseResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}

	if out, err := gitOutput(task.WorktreePath, "merge-base", "--is-ancestor", targetBranch, "HEAD"); err == nil {
		headOut, headErr := gitOutput(task.WorktreePath, "rev-parse", "HEAD")
		if headErr != nil {
			return &RebaseResult{ErrorMessage: fmt.Sprintf("resolving task HEAD: %s", strings.TrimSpace(string(headOut)))}, fmt.Errorf("resolving task HEAD: %w", headErr)
		}
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
		return &RebaseResult{Success: true, UpToDate: true, RebasedHead: strings.TrimSpace(string(headOut))}, nil
	} else if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		applog.Debugf("[worktree] task branch %s is behind %s before rebase: %s", task.WorktreeBranch, targetBranch, trimmed)
	}

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
	rebaseOut, rebaseErr := gitOutput(task.WorktreePath, "rebase", targetBranch)
	if rebaseErr != nil {
		conflictFiles := detectConflicts(task.WorktreePath)
		if len(conflictFiles) > 0 {
			_ = AbortRebase(task.WorktreePath)
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
			return &RebaseResult{
				Success:       false,
				ConflictFiles: conflictFiles,
				ErrorMessage:  fmt.Sprintf("Rebase onto %s encountered conflicts and was aborted. Resolve the conflicts in the task worktree or ask the agent to reconcile with %s, then try rebase again.", targetBranch, targetBranch),
			}, nil
		}
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := strings.TrimSpace(string(rebaseOut))
		if msg == "" {
			msg = rebaseErr.Error()
		}
		return &RebaseResult{ErrorMessage: msg}, fmt.Errorf("rebase task branch onto %s failed: %w", targetBranch, rebaseErr)
	}

	headOut, err := gitOutput(task.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &RebaseResult{ErrorMessage: fmt.Sprintf("resolving rebased HEAD: %s", strings.TrimSpace(string(headOut)))}, fmt.Errorf("resolving rebased HEAD: %w", err)
	}
	rebasedHead := strings.TrimSpace(string(headOut))
	if ws.llmSvc != nil && rebasedHead != "" {
		ws.recordTaskCommitRange(ctx, task, task.WorktreePath, targetBranch, rebasedHead)
	}
	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
	return &RebaseResult{Success: true, RebasedHead: rebasedHead}, nil
}

func (ws *WorktreeService) fastForwardTaskWorktreeToTarget(ctx context.Context, task *models.Task, repoDir string, targetBranch string) (*MergeResult, error) {
	currentBranchOut, err := gitOutput(task.WorktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := "task worktree must be on the expected task branch before fast-forward merge"
		return &MergeResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}
	currentBranch := strings.TrimSpace(string(currentBranchOut))
	if currentBranch != task.WorktreeBranch {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := fmt.Sprintf("task worktree is on branch %q, expected %q", currentBranch, task.WorktreeBranch)
		return &MergeResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}

	statusOut, err := GitStatusPorcelain(task.WorktreePath)
	if err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("checking task worktree status: %s", err.Error())}, fmt.Errorf("checking task worktree status: %w", err)
	}
	if strings.TrimSpace(statusOut) != "" {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := "task worktree has uncommitted changes; commit or discard them before fast-forward merge"
		return &MergeResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}

	rebased := false
	if out, err := gitOutput(task.WorktreePath, "merge-base", "--is-ancestor", targetBranch, "HEAD"); err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			applog.Debugf("[worktree] task branch %s is not already fast-forwardable from %s before auto-rebase: %s", task.WorktreeBranch, targetBranch, trimmed)
		}
		rebaseOut, rebaseErr := gitOutput(task.WorktreePath, "rebase", targetBranch)
		if rebaseErr != nil {
			conflictFiles := detectConflicts(task.WorktreePath)
			if len(conflictFiles) > 0 {
				_ = AbortRebase(task.WorktreePath)
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
				return &MergeResult{
					Success:       false,
					ConflictFiles: conflictFiles,
					ErrorMessage:  fmt.Sprintf("Local fast-forward merge requires updating branch from %s. Auto-rebase encountered conflicts; rebase was aborted. Resolve conflicts in worktree and retry merge.", targetBranch),
				}, nil
			}
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: strings.TrimSpace(string(rebaseOut))}, fmt.Errorf("auto-rebase task branch onto %s failed: %w", targetBranch, rebaseErr)
		}
		rebased = true
	}

	oldTargetOut, err := gitOutput(task.WorktreePath, "rev-parse", "refs/heads/"+targetBranch)
	if err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("resolving target branch: %s", strings.TrimSpace(string(oldTargetOut)))}, fmt.Errorf("resolving target branch %s: %w", targetBranch, err)
	}
	oldTarget := strings.TrimSpace(string(oldTargetOut))

	newTaskOut, err := gitOutput(task.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("resolving task HEAD: %s", strings.TrimSpace(string(newTaskOut)))}, fmt.Errorf("resolving task HEAD: %w", err)
	}
	newTask := strings.TrimSpace(string(newTaskOut))

	if out, err := gitOutput(task.WorktreePath, "merge-base", "--is-ancestor", oldTarget, newTask); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := fmt.Sprintf("fast-forward merge requires %s to be an ancestor of task HEAD", targetBranch)
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			msg = fmt.Sprintf("%s: %s", msg, trimmed)
		}
		return &MergeResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
	}
	if rebased && ws.llmSvc != nil {
		ws.recordTaskCommitRange(ctx, task, task.WorktreePath, oldTarget, newTask)
	}

	if targetWorktree, err := findWorktreeForBranch(repoDir, targetBranch); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: err.Error()}, fmt.Errorf("finding target worktree: %w", err)
	} else if targetWorktree != "" {
		mergeOut, mergeErr := gitOutput(targetWorktree, "merge", "--ff-only", "refs/heads/"+task.WorktreeBranch)
		if mergeErr != nil {
			mergeErrMsg := strings.TrimSpace(string(mergeOut))
			if mergeErrMsg == "" {
				mergeErrMsg = mergeErr.Error()
			}
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: mergeErrMsg}, fmt.Errorf("fast-forward merge in target worktree failed: %w", mergeErr)
		}

		mergedHeadOut, err := gitOutput(targetWorktree, "rev-parse", "HEAD")
		if err != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: fmt.Sprintf("resolving merged target HEAD: %s", strings.TrimSpace(string(mergedHeadOut)))}, fmt.Errorf("resolving merged target HEAD: %w", err)
		}
		mergedHead := strings.TrimSpace(string(mergedHeadOut))
		if mergedHead != newTask {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			msg := fmt.Sprintf("fast-forward merge ended at %s, expected rebased task HEAD %s", mergedHead, newTask)
			return &MergeResult{ErrorMessage: msg}, fmt.Errorf("%s", msg)
		}
	} else if out, err := gitOutput(task.WorktreePath, "update-ref", "refs/heads/"+targetBranch, newTask, oldTarget); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return &MergeResult{ErrorMessage: msg}, fmt.Errorf("updating target branch ref: %w", err)
	}

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)
	return &MergeResult{Success: true, MergeCommit: newTask}, nil
}

func (ws *WorktreeService) recordTaskCommitRange(ctx context.Context, task *models.Task, repoPath, baseSHA, headSHA string) {
	if ws == nil || ws.llmSvc == nil || task == nil || strings.TrimSpace(repoPath) == "" || strings.TrimSpace(baseSHA) == "" || strings.TrimSpace(headSHA) == "" {
		return
	}
	out, err := gitOutput(repoPath, "rev-list", "--reverse", baseSHA+".."+headSHA)
	if err != nil {
		applog.Infof("[task-commit-stats] error listing app rebased commits task=%s base=%s head=%s: %v", task.ID, baseSHA, headSHA, err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		sha := strings.TrimSpace(line)
		if sha == "" {
			continue
		}
		if err := ws.llmSvc.RecordTaskCommitStat(ctx, task, nil, repoPath, sha); err != nil {
			applog.Infof("[task-commit-stats] error recording app rebased commit stat task=%s sha=%s: %v", task.ID, sha, err)
		}
	}
}

func findWorktreeForBranch(repoDir string, branch string) (string, error) {
	worktrees, err := ListGitWorktrees(repoDir)
	if err != nil {
		return "", err
	}
	expectedRef := "refs/heads/" + branch
	for _, worktree := range worktrees {
		if worktree.Branch != branch {
			continue
		}
		out, err := gitOutput(worktree.Path, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(out)) == expectedRef {
			return worktree.Path, nil
		}
	}
	return "", nil
}

func AbortRebase(repoDir string) error {
	cmd := exec.Command("git", "rebase", "--abort")
	cmd.Dir = repoDir
	_, err := cmd.CombinedOutput()
	return err
}

func gitOutput(repoDir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	return cmd.CombinedOutput()
}

func StagedPaths(repoDir string) (map[string]bool, error) {
	out, err := gitOutput(repoDir, "diff", "--name-only", "--cached")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	paths := make(map[string]bool)
	for _, path := range strings.Fields(string(out)) {
		paths[path] = true
	}
	return paths, nil
}

func SquashMergePaths(repoDir string, stagedBefore map[string]bool) ([]string, error) {
	stagedAfter, err := StagedPaths(repoDir)
	if err != nil {
		return nil, err
	}
	var changed []string
	for path := range stagedAfter {
		if stagedBefore[path] {
			continue
		}
		changed = append(changed, path)
	}
	if len(changed) == 0 {
		return nil, fmt.Errorf("squash merge produced no staged changes")
	}
	return changed, nil
}

// ResetSquashMergeChanges restores only files changed by a failed squash merge
// attempt. Unlike `git reset --hard`, this does not reset the whole target
// checkout or touch staged user changes that existed before the squash attempt.
func ResetSquashMergeChanges(repoDir string, squashPaths []string) error {
	if len(squashPaths) == 0 {
		return nil
	}

	args := append([]string{"restore", "--staged", "--worktree", "--source=HEAD", "--"}, squashPaths...)
	if out, err := gitOutput(repoDir, args...); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ActiveConflictFiles returns files with active merge conflicts in the given repository.
func ActiveConflictFiles(repoDir string) []string {
	return detectConflicts(repoDir)
}

// detectConflicts returns a list of files with merge conflicts.
func detectConflicts(repoDir string) []string {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=U")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// AbortMerge aborts an in-progress merge.
func AbortMerge(repoDir string) error {
	cmd := exec.Command("git", "merge", "--abort")
	cmd.Dir = repoDir
	_, err := cmd.CombinedOutput()
	return err
}

// ResolveConflictsWithAI uses the LLM service to resolve merge conflicts.
func (ws *WorktreeService) ResolveConflictsWithAI(ctx context.Context, task *models.Task, repoDir string) (*MergeResult, error) {
	if ws.llmSvc == nil {
		return nil, fmt.Errorf("LLM service not available for conflict resolution")
	}

	conflictFiles := detectConflicts(repoDir)
	if len(conflictFiles) == 0 {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
		return &MergeResult{ErrorMessage: "no active merge conflicts found"}, fmt.Errorf("no active merge conflicts found")
	}

	// Build a prompt describing the conflicts
	var conflictDetails strings.Builder
	conflictDetails.WriteString("Please resolve the following merge conflicts. For each file, output the resolved content.\n\n")

	for _, file := range conflictFiles {
		content, err := os.ReadFile(filepath.Join(repoDir, file))
		if err != nil {
			continue
		}
		conflictDetails.WriteString(fmt.Sprintf("=== File: %s ===\n%s\n\n", file, string(content)))
	}

	conflictDetails.WriteString("\nResolve each conflict by choosing the appropriate changes or combining them intelligently. ")
	conflictDetails.WriteString("After resolving, stage the files with `git add` and commit with a descriptive message.")

	// Execute resolution via the agent in the repo directory
	agent, err := ws.llmSvc.getDefaultAgentForTask(ctx, task.ProjectID)
	if err != nil || agent == nil {
		return nil, fmt.Errorf("no agent available for conflict resolution")
	}

	_, _, _, err = ws.llmSvc.callLLM(ctx, conflictDetails.String(), nil, *agent, "", repoDir, "")
	if err != nil {
		return nil, fmt.Errorf("AI conflict resolution failed: %w", err)
	}

	// Check if conflicts are resolved
	remainingConflicts := detectConflicts(repoDir)
	if len(remainingConflicts) > 0 {
		return &MergeResult{
			ConflictFiles: remainingConflicts,
			ErrorMessage:  "AI could not resolve all conflicts",
		}, nil
	}

	// Commit the resolution. Both staging and committing must succeed before the
	// task can be considered merged.
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = repoDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("staging resolved conflicts failed: %s", strings.TrimSpace(string(out)))}, fmt.Errorf("staging resolved conflicts: %w", err)
	}

	commitCmd := exec.Command("git", "commit", "--no-edit")
	commitCmd.Dir = repoDir
	if out, err := commitCmd.CombinedOutput(); err != nil {
		commitErrMsg := strings.TrimSpace(string(out))
		if commitErrMsg == "" {
			commitErrMsg = err.Error()
		}
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("committing resolved merge failed: %s", commitErrMsg)}, fmt.Errorf("committing resolved merge: %w", err)
	}

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)

	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashCmd.Dir = repoDir
	hashOut, _ := hashCmd.Output()
	mergeCommit := strings.TrimSpace(string(hashOut))
	if ws.llmSvc != nil && mergeCommit != "" {
		if err := ws.llmSvc.RecordTaskCommitStat(ctx, task, nil, repoDir, mergeCommit); err != nil {
			applog.Infof("[task-commit-stats] error recording app conflict-resolution commit stat task=%s sha=%s: %v", task.ID, mergeCommit, err)
		}
	}

	return &MergeResult{
		Success:     true,
		MergeCommit: mergeCommit,
	}, nil
}

// CleanupWorktree removes the worktree and optionally deletes the branch.
func (ws *WorktreeService) CleanupWorktree(ctx context.Context, task *models.Task, repoDir string, deleteBranch bool) error {
	if task.WorktreePath == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Check for uncommitted changes
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = task.WorktreePath
	out, err := statusCmd.Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return fmt.Errorf("worktree has uncommitted changes; commit or discard them first")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Remove worktree
	removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", task.WorktreePath, "--force")
	removeCmd.Dir = repoDir
	if out, err := removeCmd.CombinedOutput(); err != nil {
		applog.Infof("[worktree] error removing worktree: %s", string(out))
		if err := ctx.Err(); err != nil {
			return err
		}
		// Try manual removal as fallback
		os.RemoveAll(task.WorktreePath)
		// Prune worktree list
		pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
		pruneCmd.Dir = repoDir
		pruneCmd.Run()
	}

	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer finalizeCancel()

	// Delete branch if requested, but guard against active descendants
	if deleteBranch && task.WorktreeBranch != "" {
		// Check if any descendants depend on this branch (non-terminal children)
		hasActiveDesc := false
		if ws.taskRepo != nil {
			active, descErr := ws.taskRepo.HasNonTerminalDescendants(finalizeCtx, task.ID)
			if descErr != nil {
				applog.Infof("[worktree] error checking descendants for task %s: %v", task.ID, descErr)
			} else {
				hasActiveDesc = active
			}
		}
		if hasActiveDesc {
			applog.Infof("[worktree] skipping branch deletion for task %s branch %s: has active descendants", task.ID, task.WorktreeBranch)
		} else {
			deleteCmd := exec.CommandContext(finalizeCtx, "git", "branch", "-D", task.WorktreeBranch)
			deleteCmd.Dir = repoDir
			if out, err := deleteCmd.CombinedOutput(); err != nil {
				applog.Infof("[worktree] error deleting branch %s: %s", task.WorktreeBranch, string(out))
			}
		}
	}

	// Clear worktree info from task
	if err := ws.taskRepo.ClearWorktreeInfo(finalizeCtx, task.ID); err != nil {
		applog.Infof("[worktree] error clearing worktree info: %v", err)
	}

	applog.Infof("[worktree] cleaned up worktree for task %s", task.ID)
	return nil
}

// GetWorktreeDiff returns the changes introduced on the worktree branch since
// it diverged from the target branch. This matches Git's three-dot/PR diff
// semantics, so target-only commits do not appear as reverse changes.
func GetWorktreeDiff(repoDir string, branchName string, targetBranch string) string {
	if branchName == "" || targetBranch == "" || !isGitRepoDir(repoDir) {
		return ""
	}
	if !gitRefExists(repoDir, branchName) || !gitRefExists(repoDir, targetBranch) {
		return ""
	}
	cmd := exec.Command("git", "diff", targetBranch+"..."+branchName)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		applog.Infof("[worktree] error getting worktree diff repo=%s target=%s branch=%s: %v: %s", repoDir, targetBranch, branchName, err, strings.TrimSpace(string(out)))
		return ""
	}
	return string(out)
}

// GetWorktreeDiffWithUncommitted returns the diff between the target/task merge
// base and the worktree working tree. This includes committed branch changes,
// staged changes, unstaged tracked changes, and synthetic diffs for untracked
// files without rendering the same tracked path once for the committed branch
// state and again for the uncommitted follow-up state.
func GetWorktreeDiffWithUncommitted(repoDir string, branchName string, targetBranch string, worktreePath string) string {
	if targetBranch == "" {
		return ""
	}
	if worktreePath != "" && isGitWorktreeDir(worktreePath) {
		trackedDiff, trackedDiffOK := captureWorktreeDiffAgainstTarget(worktreePath, targetBranch)
		if trackedDiffOK {
			untrackedDiff := captureWorktreeUntracked(worktreePath)
			if trackedDiff == "" {
				return untrackedDiff
			}
			if untrackedDiff == "" {
				return trackedDiff
			}
			return trackedDiff + "\n" + untrackedDiff
		}
	}
	return GetWorktreeDiff(repoDir, branchName, targetBranch)
}

// GetWorktreeDiffFileWithUncommitted returns only the requested file-index diff
// for the same target-to-current-worktree view as GetWorktreeDiffWithUncommitted.
// It resolves the changed-file order from compact name-status/untracked output,
// then runs a path-scoped git diff or synthesizes one untracked-file diff.
func GetWorktreeDiffFileWithUncommitted(repoDir string, branchName string, targetBranch string, worktreePath string, fileIndex int) (string, bool) {
	if fileIndex < 0 || targetBranch == "" {
		return "", false
	}
	if worktreePath != "" && isGitWorktreeDir(worktreePath) && gitRefExists(worktreePath, targetBranch) {
		mergeBaseOut, err := gitOutput(worktreePath, "merge-base", targetBranch, "HEAD")
		if err == nil {
			mergeBase := strings.TrimSpace(string(mergeBaseOut))
			if mergeBase != "" {
				return captureWorktreeDiffFileAgainstMergeBase(worktreePath, mergeBase, fileIndex)
			}
		}
	}

	if branchName == "" || targetBranch == "" || !isGitRepoDir(repoDir) || !gitRefExists(repoDir, branchName) || !gitRefExists(repoDir, targetBranch) {
		return "", false
	}
	return captureCommittedDiffFile(repoDir, targetBranch+"..."+branchName, fileIndex)
}

// captureWorktreeDiffAgainstTarget captures tracked changes between the
// target/HEAD merge base and the worktree's current working tree, including
// staged and unstaged changes.
func captureWorktreeDiffAgainstTarget(worktreePath, targetBranch string) (string, bool) {
	if worktreePath == "" || targetBranch == "" || !isGitWorktreeDir(worktreePath) || !gitRefExists(worktreePath, targetBranch) {
		return "", false
	}
	mergeBaseOut, err := gitOutput(worktreePath, "merge-base", targetBranch, "HEAD")
	if err != nil {
		return "", false
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))
	if mergeBase == "" {
		return "", false
	}
	cmd := exec.Command("git", "diff", mergeBase)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		applog.Infof("[worktree] error getting live worktree diff path=%s target=%s: %v: %s", worktreePath, targetBranch, err, strings.TrimSpace(string(out)))
		return "", false
	}
	return string(out), true
}

func isGitRepoDir(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func isGitWorktreeDir(dir string) bool {
	return isGitRepoDir(dir)
}

func gitRefExists(repoDir, ref string) bool {
	if repoDir == "" || ref == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

type worktreeDiffFileTarget struct {
	Path      string
	Pathspecs []string
	Untracked bool
}

func captureWorktreeDiffFileAgainstMergeBase(worktreePath string, mergeBase string, fileIndex int) (string, bool) {
	targets, err := worktreeDiffFileTargets(worktreePath, mergeBase)
	if err != nil || fileIndex < 0 || fileIndex >= len(targets) {
		return "", false
	}
	target := targets[fileIndex]
	if target.Untracked {
		diff := generateNewFileDiffForWorktree(worktreePath, target.Path)
		return diff, diff != ""
	}
	return captureTrackedDiffFile(worktreePath, mergeBase, target.Pathspecs)
}

func worktreeDiffFileTargets(worktreePath string, mergeBase string) ([]worktreeDiffFileTarget, error) {
	trackedCmd := exec.Command("git", "diff", "--name-status", mergeBase)
	trackedCmd.Dir = worktreePath
	trackedOut, err := trackedCmd.Output()
	if err != nil {
		return nil, err
	}
	targets := parseWorktreeDiffFileTargets(trackedOut)

	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedCmd.Dir = worktreePath
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return targets, nil
	}
	for _, path := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
		if path = strings.TrimSpace(path); path != "" {
			targets = append(targets, worktreeDiffFileTarget{Path: path, Pathspecs: []string{path}, Untracked: true})
		}
	}
	return targets, nil
}

type worktreeNameStatusRecord struct {
	Status     string
	Path       string
	SourcePath string
}

// parseWorktreeNameStatus decodes tracked git diff --name-status records for the
// Changes projections. Path is the destination for rename/copy records, while
// SourcePath is populated with their source path.
func parseWorktreeNameStatus(out []byte) []worktreeNameStatusRecord {
	var records []worktreeNameStatusRecord
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}

		record := worktreeNameStatusRecord{Status: parts[0], Path: parts[1]}
		if (strings.HasPrefix(record.Status, "R") || strings.HasPrefix(record.Status, "C")) && len(parts) >= 3 {
			record.SourcePath = parts[1]
			record.Path = parts[2]
		}
		records = append(records, record)
	}
	return records
}

func parseWorktreeDiffFileTargets(out []byte) []worktreeDiffFileTarget {
	records := parseWorktreeNameStatus(out)
	var targets []worktreeDiffFileTarget
	for _, record := range records {
		pathspecs := []string{record.Path}
		if record.SourcePath != "" {
			pathspecs = []string{record.SourcePath, record.Path}
		}
		targets = append(targets, worktreeDiffFileTarget{Path: record.Path, Pathspecs: pathspecs})
	}
	return targets
}

func captureTrackedDiffFile(worktreePath string, base string, pathspecs []string) (string, bool) {
	if len(pathspecs) == 0 {
		return "", false
	}
	args := append([]string{"diff", base, "--"}, pathspecs...)
	cmd := exec.Command("git", args...)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		applog.Infof("[worktree] error getting path-scoped live worktree diff path=%s base=%s file=%s: %v: %s", worktreePath, base, strings.Join(pathspecs, ","), err, strings.TrimSpace(string(out)))
		return "", false
	}
	return string(out), strings.TrimSpace(string(out)) != ""
}

func captureCommittedDiffFile(repoDir string, revision string, fileIndex int) (string, bool) {
	targetsCmd := exec.Command("git", "diff", "--name-status", revision)
	targetsCmd.Dir = repoDir
	targetsOut, err := targetsCmd.Output()
	if err != nil {
		return "", false
	}
	targets := parseWorktreeDiffFileTargets(targetsOut)
	if fileIndex < 0 || fileIndex >= len(targets) {
		return "", false
	}
	args := append([]string{"diff", revision, "--"}, targets[fileIndex].Pathspecs...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return string(out), strings.TrimSpace(string(out)) != ""
}

// captureWorktreeUntracked captures untracked files in a worktree directory as
// synthetic unified diffs because git diff does not include them.
func captureWorktreeUntracked(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}

	var result strings.Builder
	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedCmd.Dir = worktreePath
	untrackedOut, _ := untrackedCmd.Output()
	if len(untrackedOut) > 0 {
		untracked := strings.TrimSpace(string(untrackedOut))
		if untracked != "" {
			for _, f := range strings.Split(untracked, "\n") {
				f = strings.TrimSpace(f)
				if f == "" {
					continue
				}
				fileDiff := generateNewFileDiffForWorktree(worktreePath, f)
				if fileDiff != "" {
					result.WriteString(fileDiff)
				}
			}
		}
	}

	return result.String()
}

// generateNewFileDiffForWorktree creates a unified diff for a new (untracked) file.
func generateNewFileDiffForWorktree(worktreePath, relPath string) string {
	absPath := filepath.Join(worktreePath, relPath)
	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		return ""
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}

	// Check for binary
	checkLen := len(content)
	if checkLen > 8000 {
		checkLen = 8000
	}
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return fmt.Sprintf("\ndiff --git a/%s b/%s\nnew file mode 100644\nBinary files /dev/null and b/%s differ\n", relPath, relPath, relPath)
		}
	}

	lines := strings.Split(string(content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return fmt.Sprintf("\ndiff --git a/%s b/%s\nnew file mode 100644\n", relPath, relPath)
	}

	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("\ndiff --git a/%s b/%s\n", relPath, relPath))
	buf.WriteString("new file mode 100644\n")
	buf.WriteString("--- /dev/null\n")
	buf.WriteString(fmt.Sprintf("+++ b/%s\n", relPath))
	buf.WriteString(fmt.Sprintf("@@ -0,0 +1,%d @@\n", len(lines)))
	for _, l := range lines {
		buf.WriteString("+" + l + "\n")
	}
	return buf.String()
}

// GetWorktreeFileStats returns a summary of changed files in the worktree branch.
type WorktreeFileStat struct {
	Path   string
	Status string // "added", "modified", "deleted"
}

func GetWorktreeFileStats(repoDir string, branchName string, targetBranch string) []WorktreeFileStat {
	if branchName == "" || targetBranch == "" {
		return nil
	}
	cmd := exec.Command("git", "diff", "--name-status", targetBranch+"..."+branchName)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseWorktreeFileStats(out)
}

// GetWorktreeFileStatsWithUncommitted returns the file list for the same
// reviewable task output as GetWorktreeDiffWithUncommitted: merge base to the
// current worktree state. It intentionally does not merge branch-vs-target stats
// with git status (whose base is HEAD), because a post-commit revert to the
// target would then appear in the list even though it is absent from the diff.
func GetWorktreeFileStatsWithUncommitted(repoDir string, branchName string, targetBranch string, worktreePath string) []WorktreeFileStat {
	if worktreePath == "" || targetBranch == "" || !isGitWorktreeDir(worktreePath) || !gitRefExists(worktreePath, targetBranch) {
		return GetWorktreeFileStats(repoDir, branchName, targetBranch)
	}

	mergeBaseOut, err := gitOutput(worktreePath, "merge-base", targetBranch, "HEAD")
	if err != nil {
		return GetWorktreeFileStats(repoDir, branchName, targetBranch)
	}
	mergeBase := strings.TrimSpace(string(mergeBaseOut))
	if mergeBase == "" {
		return GetWorktreeFileStats(repoDir, branchName, targetBranch)
	}

	trackedCmd := exec.Command("git", "diff", "--name-status", mergeBase)
	trackedCmd.Dir = worktreePath
	trackedOut, err := trackedCmd.Output()
	if err != nil {
		return GetWorktreeFileStats(repoDir, branchName, targetBranch)
	}
	stats := parseWorktreeFileStats(trackedOut)

	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedCmd.Dir = worktreePath
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return stats
	}
	for _, path := range strings.Split(strings.TrimSpace(string(untrackedOut)), "\n") {
		if path = strings.TrimSpace(path); path != "" {
			stats = mergeWorktreeFileStats(stats, []WorktreeFileStat{{Path: path, Status: "added"}})
		}
	}
	return stats
}

func parseWorktreeFileStats(out []byte) []WorktreeFileStat {
	records := parseWorktreeNameStatus(out)
	var stats []WorktreeFileStat
	for _, record := range records {
		stats = append(stats, WorktreeFileStat{Path: record.Path, Status: gitStatusToWorktreeFileStatus(record.Status)})
	}
	return stats
}

func parseGitStatusFileStats(out []byte) []WorktreeFileStat {
	var stats []WorktreeFileStat
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := strings.TrimSpace(line[3:])
		if code == "" || path == "" {
			continue
		}
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = strings.TrimSpace(path[idx+4:])
		}
		stats = append(stats, WorktreeFileStat{Path: path, Status: gitStatusToWorktreeFileStatus(code)})
	}
	return stats
}

func gitStatusToWorktreeFileStatus(code string) string {
	if strings.Contains(code, "D") {
		return "deleted"
	}
	if strings.Contains(code, "A") || strings.Contains(code, "?") {
		return "added"
	}
	return "modified"
}

func mergeWorktreeFileStats(base []WorktreeFileStat, overlay []WorktreeFileStat) []WorktreeFileStat {
	if len(overlay) == 0 {
		return base
	}
	merged := make([]WorktreeFileStat, 0, len(base)+len(overlay))
	index := make(map[string]int, len(base)+len(overlay))
	for _, stat := range base {
		index[stat.Path] = len(merged)
		merged = append(merged, stat)
	}
	for _, stat := range overlay {
		if i, ok := index[stat.Path]; ok {
			merged[i] = stat
			continue
		}
		index[stat.Path] = len(merged)
		merged = append(merged, stat)
	}
	return merged
}

// getGlobalMergeTarget returns the global default merge target branch.
func (ws *WorktreeService) getGlobalMergeTarget(ctx context.Context) string {
	if ws.settingsRepo == nil {
		return ""
	}
	val, err := ws.settingsRepo.Get(ctx, "worktree_merge_target")
	if err != nil || val == "" {
		return ""
	}
	return val
}

// GetGlobalAutoMerge returns the global auto-merge default setting.
func (ws *WorktreeService) GetGlobalAutoMerge(ctx context.Context) bool {
	if ws.settingsRepo == nil {
		return false
	}
	val, err := ws.settingsRepo.Get(ctx, "worktree_auto_merge")
	if err != nil {
		return false
	}
	return val == "true"
}

// GetCleanupPolicy returns the worktree cleanup policy.
func (ws *WorktreeService) GetCleanupPolicy(ctx context.Context) string {
	if ws.settingsRepo == nil {
		return "after_merge"
	}
	val, err := ws.settingsRepo.Get(ctx, "worktree_cleanup")
	if err != nil || val == "" {
		return "after_merge"
	}
	return val
}

// IsBranchMerged checks if a branch has been fully merged into the target branch.
// Returns true if the branch is merged (no unique commits), false otherwise.
//
// NOTE: A missing branch is treated as merged here so cleanup logic can
// reclaim worktrees whose branches were manually deleted post-merge. Callers
// that need to distinguish "branch is provably merged in git right now" from
// "branch is missing" should use IsBranchTipMergedInto instead.
func IsBranchMerged(repoDir string, branchName string, targetBranch string) bool {
	return IsBranchMergedContext(context.Background(), repoDir, branchName, targetBranch)
}

func IsBranchMergedContext(ctx context.Context, repoDir string, branchName string, targetBranch string) bool {
	if branchName == "" || targetBranch == "" {
		return false
	}

	// Check if branch exists
	checkCmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branchName)
	checkCmd.Dir = repoDir
	if err := checkCmd.Run(); err != nil {
		if ctx.Err() != nil {
			return false
		}
		// Branch doesn't exist (might have been manually deleted)
		return true
	}

	// Use git merge-base --is-ancestor to check if branch is merged
	// This checks if the branch tip is reachable from target branch
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", branchName, targetBranch)
	cmd.Dir = repoDir
	err := cmd.Run()
	if ctx.Err() != nil {
		return false
	}

	// Exit code 0 means ancestor (merged), non-zero means not merged
	return err == nil
}

// IsBranchTipMergedInto reports whether `branchName` exists in the repo and
// its tip commit is an ancestor of `targetBranch` (i.e. the branch has been
// merged into the target). Unlike IsBranchMerged, a missing branch returns
// false so UI reconciliation does not falsely hide merge actions for tasks
// whose worktree branch was never actually created.
func IsBranchTipMergedInto(repoDir string, branchName string, targetBranch string) bool {
	if branchName == "" || targetBranch == "" {
		return false
	}
	if !IsGitRepo(repoDir) {
		return false
	}

	// Branch must exist locally to be considered merged-in-git.
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", branchName).Run(); err != nil {
		return false
	}
	// Target branch must also exist; otherwise we can't compare ancestry.
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", targetBranch).Run(); err != nil {
		return false
	}

	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", branchName, targetBranch)
	return cmd.Run() == nil
}

// IsBranchBehindTarget reports whether targetBranch has commits that are not
// reachable from branchName. It is used to decide whether a task branch can be
// usefully rebased onto its merge target.
func IsBranchBehindTarget(repoDir string, branchName string, targetBranch string) bool {
	if branchName == "" || targetBranch == "" {
		return false
	}
	if !IsGitRepo(repoDir) {
		return false
	}
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", branchName).Run(); err != nil {
		return false
	}
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", targetBranch).Run(); err != nil {
		return false
	}

	cmd := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", targetBranch, branchName)
	return cmd.Run() != nil
}

// IsBranchDivergedFromTarget reports whether both the target and task branch
// contain commits the other does not. Rebasing is useful in this state because
// there are target commits to incorporate and task commits to replay.
func IsBranchDivergedFromTarget(repoDir string, branchName string, targetBranch string) bool {
	if branchName == "" || targetBranch == "" || !IsGitRepo(repoDir) {
		return false
	}
	cmd := exec.Command("git", "-C", repoDir, "rev-list", "--left-right", "--count", targetBranch+"..."+branchName)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	counts := strings.Fields(string(out))
	if len(counts) != 2 {
		return false
	}
	targetOnly, err := strconv.Atoi(counts[0])
	if err != nil {
		return false
	}
	branchOnly, err := strconv.Atoi(counts[1])
	if err != nil {
		return false
	}
	return targetOnly > 0 && branchOnly > 0
}

// HandlePostExecution handles worktree operations after task execution completes.
// Called by the LLM service after a task finishes successfully.
func (ws *WorktreeService) HandlePostExecution(ctx context.Context, task *models.Task, execModel *models.Execution, repoDir string) {
	if task.WorktreePath == "" || task.WorktreeBranch == "" {
		return
	}

	// Commit any changes in the worktree. If this fails, do not mark the task
	// branch as ready/pending; otherwise the Changes tab can offer a branch merge
	// for a branch that does not actually contain the provider's file edits.
	commitCtx := WorktreeCommitMessageContext{
		Phase:     WorktreeCommitPhaseInitial,
		TaskTitle: task.Title,
	}
	if ws.llmSvc != nil && task.AgentID != nil {
		commitCtx.DiffSummary = ws.llmSvc.SummarizeWorktreeCommitDiffForAgentID(ctx, task.WorktreePath, *task.AgentID, commitCtx)
	}
	msg := BuildWorktreeCommitMessage(task.WorktreePath, commitCtx)
	var commitErr error
	if ws.llmSvc != nil {
		commitErr = ws.llmSvc.CommitTaskWorktreeChanges(ctx, task, execModel, task.WorktreePath, msg)
	} else {
		commitErr = CommitWorktreeChanges(task.WorktreePath, msg)
	}
	if commitErr != nil {
		applog.Infof("[worktree] error committing changes for task %s: %v", task.ID, commitErr)
		if ws.taskRepo != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		}
		return
	}

	// Auto-merge if enabled
	if task.AutoMerge {
		applog.Infof("[worktree] auto-merging task %s branch %s -> %s", task.ID, task.WorktreeBranch, task.MergeTargetBranch)
		result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
		if err != nil {
			applog.Infof("[worktree] auto-merge failed for task %s: %v", task.ID, err)
			return
		}
		if !result.Success && len(result.ConflictFiles) > 0 {
			applog.Infof("[worktree] auto-merge has conflicts for task %s, attempting AI resolution", task.ID)
			aiResult, aiErr := ws.ResolveConflictsWithAI(ctx, task, repoDir)
			if aiErr != nil || (aiResult != nil && !aiResult.Success) {
				applog.Infof("[worktree] AI conflict resolution failed for task %s, aborting merge", task.ID)
				AbortMerge(repoDir)
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
				return
			}
		}

		// Cleanup after successful merge if policy says so
		policy := ws.GetCleanupPolicy(ctx)
		if policy == "after_merge" {
			if cleanErr := ws.CleanupWorktree(ctx, task, repoDir, true); cleanErr != nil {
				applog.Infof("[worktree] cleanup after merge failed: %v", cleanErr)
			}
		}
	} else {
		// Set merge status to pending for manual merge
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)
	}
}

// CleanupMergedWorktrees scans all tasks with worktrees and cleans up those
// whose branches have been merged to their target branches.
// Called periodically by the scheduler to detect manual merges.
func (ws *WorktreeService) CleanupMergedWorktrees(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Get cleanup policy
	policy := ws.GetCleanupPolicy(ctx)
	if policy != "after_merge" {
		// Don't auto-cleanup if policy is "keep" or "manual"
		return nil
	}

	// Get all tasks with worktrees
	tasks, err := ws.taskRepo.ListWithWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("listing tasks with worktrees: %w", err)
	}

	if len(tasks) == 0 {
		return nil
	}

	applog.Infof("[worktree] cleanup scan: checking %d tasks with worktrees", len(tasks))

	cleanedCount := 0
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Skip tasks that are currently running or pending — their worktrees are in use
		if task.Status == models.StatusRunning || task.Status == models.StatusPending || task.Status == models.StatusQueued {
			continue
		}

		// Get the project to determine the repo directory
		project, err := ws.projectRepo.GetByID(ctx, task.ProjectID)
		if err != nil || project == nil {
			applog.Infof("[worktree] cleanup: skipping task %s (project not found)", task.ID)
			continue
		}

		repoDir := project.RepoPath
		if repoDir == "" || !IsGitRepoContext(ctx, repoDir) {
			applog.Infof("[worktree] cleanup: skipping task %s (not a git repo)", task.ID)
			continue
		}

		targetBranch := task.MergeTargetBranch
		if targetBranch == "" {
			targetBranch = ws.getGlobalMergeTarget(ctx)
		}
		if targetBranch == "" {
			targetBranch = GetDefaultBranchContext(ctx, repoDir)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		// Check if branch has been merged
		if IsBranchMergedContext(ctx, repoDir, task.WorktreeBranch, targetBranch) {
			applog.Infof("[worktree] cleanup: task %s branch %s is merged to %s, cleaning up",
				task.ID, task.WorktreeBranch, targetBranch)

			// Update merge status to merged if not already
			if task.MergeStatus != models.MergeStatusMerged {
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)
			}

			// Cleanup the worktree and delete the branch
			if err := ws.CleanupWorktree(ctx, &task, repoDir, true); err != nil {
				applog.Infof("[worktree] cleanup: failed to cleanup task %s: %v", task.ID, err)
			} else {
				cleanedCount++
			}
		}
	}

	if cleanedCount > 0 {
		applog.Infof("[worktree] cleanup scan: cleaned up %d merged worktrees", cleanedCount)
	}

	// Also cleanup orphaned worktrees (worktrees with no corresponding task)
	if err := ctx.Err(); err != nil {
		return err
	}
	orphanedCount, err := ws.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		applog.Infof("[worktree] cleanup: failed to cleanup orphaned worktrees: %v", err)
	} else if orphanedCount > 0 {
		applog.Infof("[worktree] cleanup scan: cleaned up %d orphaned worktrees", orphanedCount)
	}

	return nil
}

// CleanupOrphanedWorktrees removes worktrees that exist on disk but have no corresponding task in the database.
// This can happen when tasks are deleted but their worktrees weren't cleaned up.
// Returns the number of orphaned worktrees cleaned up.
func (ws *WorktreeService) CleanupOrphanedWorktrees(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Get cleanup policy
	policy := ws.GetCleanupPolicy(ctx)
	if policy == "keep" {
		// Don't auto-cleanup if policy is "keep"
		return 0, nil
	}

	// Get all projects to check their worktrees
	projects, err := ws.projectRepo.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing projects: %w", err)
	}

	cleanedCount := 0
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return cleanedCount, err
		}
		if project.RepoPath == "" || !IsGitRepoContext(ctx, project.RepoPath) {
			continue
		}

		// List all git worktrees for this repo
		worktrees, err := ListGitWorktreesContext(ctx, project.RepoPath)
		if err != nil {
			applog.Infof("[worktree] cleanup: failed to list worktrees for project %s: %v", project.ID, err)
			continue
		}

		// Get all tasks for this project. We need both:
		// 1) knownPaths (worktree path already recorded in DB)
		// 2) knownTaskIDs (task exists but may not have worktree_path persisted yet)
		allTasks, err := ws.taskRepo.ListByProject(ctx, project.ID, "")
		if err != nil {
			applog.Infof("[worktree] cleanup: failed to list tasks for project %s: %v", project.ID, err)
			continue
		}

		// Build maps of known paths, task IDs, and lineage branches. Cleanup is
		// intentionally conservative: any worktree/branch still referenced by task
		// metadata or chained-task lineage is treated as in-use.
		knownPaths := make(map[string]bool)
		knownTaskIDs := make(map[string]bool)
		knownLineageBranches := make(map[string]string)
		for _, task := range allTasks {
			knownTaskIDs[task.ID] = true
			if task.WorktreePath != "" {
				knownPaths[task.WorktreePath] = true
			}
			if task.WorktreeBranch != "" {
				knownLineageBranches[task.WorktreeBranch] = task.ID
			}
			if task.BaseBranch != "" {
				knownLineageBranches[task.BaseBranch] = task.ID
			}
		}

		targetBranch := ws.getGlobalMergeTarget(ctx)
		if targetBranch == "" {
			targetBranch = GetDefaultBranchContext(ctx, project.RepoPath)
		}

		// Check each worktree to see if it's orphaned
		for _, worktree := range worktrees {
			if err := ctx.Err(); err != nil {
				return cleanedCount, err
			}
			// Skip the main worktree (the original repo)
			if worktree.IsMain {
				continue
			}

			// Known in DB, not orphaned.
			if knownPaths[worktree.Path] {
				continue
			}

			// Worktree directories follow .worktrees/task_<taskID> and follow-up
			// worktrees follow .worktrees/task_<taskID>_followup_<timestamp>. If
			// the task still exists but metadata was stale or temporarily empty,
			// treat the worktree as in-use.
			if taskID, ok := taskIDFromWorktreePath(worktree.Path); ok && knownTaskIDs[taskID] {
				applog.Infof("[worktree] cleanup: skipping worktree at %s because task %s still exists", worktree.Path, taskID)
				continue
			}

			if ownerID, ok := knownLineageBranches[worktree.Branch]; ok {
				applog.Infof("[worktree] cleanup: skipping worktree at %s because branch %s is referenced by task lineage %s", worktree.Path, worktree.Branch, ownerID)
				continue
			}

			if dirty, ok := worktreeDirtyState(ctx, worktree.Path); !ok || dirty {
				applog.Infof("[worktree] cleanup: skipping worktree at %s because dirty state is unsafe (dirty=%v ok=%v)", worktree.Path, dirty, ok)
				continue
			}

			if !worktreeHeadMergedIntoTarget(ctx, worktree.Path, targetBranch) {
				applog.Infof("[worktree] cleanup: skipping worktree at %s because HEAD is not merged into %s", worktree.Path, targetBranch)
				continue
			}

			deleteBranch := worktreeBranchSafeToDelete(ctx, project.RepoPath, worktree.Branch, targetBranch, knownLineageBranches)
			if worktree.Branch != "" && !deleteBranch {
				applog.Infof("[worktree] cleanup: orphaned worktree at %s has branch %s that is not safe to delete; removing worktree only", worktree.Path, worktree.Branch)
			}

			applog.Infof("[worktree] cleanup: found orphaned worktree at %s (branch: %s)", worktree.Path, worktree.Branch)

			// Try to remove the worktree using git first
			cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", worktree.Path)
			cmd.Dir = project.RepoPath
			if output, err := cmd.CombinedOutput(); err != nil {
				outputText := string(output)
				if err := ctx.Err(); err != nil {
					return cleanedCount, err
				}

				// A locked worktree may still be actively initializing. Don't perform
				// manual filesystem deletion in this case; retry on a future cleanup cycle.
				if strings.Contains(outputText, "cannot remove a locked working tree") {
					applog.Infof("[worktree] cleanup: skipping locked orphaned worktree at %s (output: %s)", worktree.Path, outputText)
					continue
				}

				// If git worktree remove fails, try manual cleanup
				applog.Infof("[worktree] cleanup: git worktree remove failed, attempting manual cleanup: %v (output: %s)", err, outputText)

				// Remove the worktree directory manually
				if err := os.RemoveAll(worktree.Path); err != nil {
					applog.Infof("[worktree] cleanup: failed to remove orphaned worktree directory %s: %v", worktree.Path, err)
					continue
				}

				// Prune stale worktree entries
				pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
				pruneCmd.Dir = project.RepoPath
				_ = pruneCmd.Run() // Ignore errors
			}

			// Delete the branch only when it is conclusively merged and unreferenced.
			if deleteBranch {
				cmd = exec.CommandContext(ctx, "git", "branch", "-D", worktree.Branch)
				cmd.Dir = project.RepoPath
				if out, err := cmd.CombinedOutput(); err != nil {
					applog.Infof("[worktree] cleanup: failed to delete orphaned branch %s: %s", worktree.Branch, string(out))
				}
			}

			cleanedCount++
		}
	}

	return cleanedCount, nil
}

func taskIDFromWorktreePath(worktreePath string) (string, bool) {
	base := filepath.Base(strings.TrimSpace(worktreePath))
	if !strings.HasPrefix(base, "task_") {
		return "", false
	}
	taskID := strings.TrimPrefix(base, "task_")
	if idx := strings.Index(taskID, "_followup_"); idx >= 0 {
		taskID = taskID[:idx]
	}
	if taskID == "" {
		return "", false
	}
	return taskID, true
}

func worktreeDirtyState(ctx context.Context, worktreePath string) (dirty bool, ok bool) {
	if worktreePath == "" {
		return false, false
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) != "", true
}

func worktreeHeadMergedIntoTarget(ctx context.Context, worktreePath, targetBranch string) bool {
	if worktreePath == "" || targetBranch == "" {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", "HEAD", targetBranch)
	cmd.Dir = worktreePath
	return cmd.Run() == nil && ctx.Err() == nil
}

func worktreeBranchSafeToDelete(ctx context.Context, repoDir, branchName, targetBranch string, knownLineageBranches map[string]string) bool {
	if branchName == "" || targetBranch == "" {
		return false
	}
	if _, ok := knownLineageBranches[branchName]; ok {
		return false
	}
	checkBranch := exec.CommandContext(ctx, "git", "rev-parse", "--verify", branchName)
	checkBranch.Dir = repoDir
	if err := checkBranch.Run(); err != nil || ctx.Err() != nil {
		return false
	}
	checkTarget := exec.CommandContext(ctx, "git", "rev-parse", "--verify", targetBranch)
	checkTarget.Dir = repoDir
	if err := checkTarget.Run(); err != nil || ctx.Err() != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", branchName, targetBranch)
	cmd.Dir = repoDir
	return cmd.Run() == nil && ctx.Err() == nil
}

// WorktreeInfo represents information about a git worktree.
type WorktreeInfo struct {
	Path   string
	Branch string
	IsMain bool
}

// ListGitWorktrees lists all worktrees for a git repository.
func ListGitWorktrees(repoDir string) ([]WorktreeInfo, error) {
	return ListGitWorktreesContext(context.Background(), repoDir)
}

// ListGitWorktreesContext lists all worktrees for a git repository.
func ListGitWorktreesContext(ctx context.Context, repoDir string) ([]WorktreeInfo, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git worktree list failed: %w (output: %s)", err, string(output))
	}

	// Resolve repoDir symlinks for comparison
	resolvedRepoDir, _ := filepath.EvalSymlinks(repoDir)
	if resolvedRepoDir == "" {
		resolvedRepoDir = repoDir
	}

	var worktrees []WorktreeInfo
	var current WorktreeInfo
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			// End of a worktree entry
			if current.Path != "" {
				// Resolve symlinks for comparison
				resolvedPath, _ := filepath.EvalSymlinks(current.Path)
				if resolvedPath == "" {
					resolvedPath = current.Path
				}
				// Mark as main if this is the original repo directory
				if resolvedPath == resolvedRepoDir {
					current.IsMain = true
				}
				worktrees = append(worktrees, current)
				current = WorktreeInfo{}
			}
			continue
		}

		if strings.HasPrefix(line, "worktree ") {
			current.Path = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			current.Branch = strings.TrimPrefix(line, "branch ")
			// Remove "refs/heads/" prefix
			current.Branch = strings.TrimPrefix(current.Branch, "refs/heads/")
		} else if strings.HasPrefix(line, "HEAD ") && current.Branch == "" {
			// Detached HEAD, not on a branch
			current.Branch = ""
		}
	}

	// Don't forget the last entry if file doesn't end with blank line
	if current.Path != "" {
		// Resolve symlinks for comparison
		resolvedPath, _ := filepath.EvalSymlinks(current.Path)
		if resolvedPath == "" {
			resolvedPath = current.Path
		}
		// Mark as main if this is the original repo directory
		if resolvedPath == resolvedRepoDir {
			current.IsMain = true
		}
		worktrees = append(worktrees, current)
	}

	return worktrees, nil
}
