package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// WorktreeService manages git worktrees for task isolation.
type WorktreeService struct {
	taskRepo     *repository.TaskRepo
	projectRepo  *repository.ProjectRepo
	settingsRepo *repository.SettingsRepo
	llmSvc       *LLMService
	githubSvc    *GitHubService
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

// SetGitHubService sets GitHub service for remote git auth when syncing worktrees.
func (ws *WorktreeService) SetGitHubService(githubSvc *GitHubService) {
	ws.githubSvc = githubSvc
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
	if dir == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// GetDefaultBranch returns the name of the default branch (main or master).
func GetDefaultBranch(repoDir string) string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD", "--short")
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
		checkCmd := exec.Command("git", "rev-parse", "--verify", name)
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

// SetupWorktree creates a git worktree for a task.
// For chained tasks with lineage metadata (BaseCommitSHA/BaseBranch), the worktree
// is created from the parent's commit SHA so child tasks inherit parent code changes.
// Returns the worktree path and branch name, or error.
func (ws *WorktreeService) SetupWorktree(ctx context.Context, task *models.Task, repoDir string) (worktreePath string, branchName string, err error) {
	if repoDir == "" || !IsGitRepo(repoDir) {
		return "", "", fmt.Errorf("not a git repository: %s", repoDir)
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
			log.Printf("[worktree] using lineage commit SHA %s as base for task %s (depth=%d)", baseRef, task.ID, task.LineageDepth)
		} else {
			log.Printf("[worktree] lineage commit SHA %s not found in repo for task %s, falling back", task.BaseCommitSHA, task.ID)
		}
	}
	if baseRef == "" && task.BaseBranch != "" {
		// Verify the branch exists
		checkBr := exec.Command("git", "rev-parse", "--verify", task.BaseBranch)
		checkBr.Dir = repoDir
		if checkBr.Run() == nil {
			baseRef = task.BaseBranch
			log.Printf("[worktree] using lineage branch %s as base for task %s (depth=%d)", baseRef, task.ID, task.LineageDepth)
		} else {
			log.Printf("[worktree] lineage branch %s not found in repo for task %s, falling back", task.BaseBranch, task.ID)
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

	// If this is a chained task and we couldn't resolve lineage, log a clear error
	if task.ParentTaskID != nil && task.BaseCommitSHA != "" && baseRef != task.BaseCommitSHA {
		log.Printf("[worktree] WARNING: chained task %s could not use parent lineage SHA %s, using fallback base %s", task.ID, task.BaseCommitSHA, baseRef)
	}

	// Create branch name from task
	slug := slugify(task.Title)
	if slug == "" {
		slug = task.ID[:8]
	}
	branchName = fmt.Sprintf("task/%s-%s", task.ID[:8], slug)

	// Worktree directory
	worktreePath = filepath.Join(repoDir, ".worktrees", fmt.Sprintf("task_%s", task.ID))

	// Check if worktree already exists
	if _, err := os.Stat(worktreePath); err == nil {
		log.Printf("[worktree] worktree already exists at %s, reusing", worktreePath)
		// Update task record
		if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); updateErr != nil {
			log.Printf("[worktree] error updating worktree info: %v", updateErr)
		}
		return worktreePath, branchName, nil
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
		// Create new branch from the resolved base ref
		cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath, baseRef)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", "", fmt.Errorf("creating worktree from base %s: %w: %s", baseRef, err, string(out))
		}
	}

	log.Printf("[worktree] created worktree at %s on branch %s (base: %s) for task %s (lineage_depth=%d)", worktreePath, branchName, baseRef, task.ID, task.LineageDepth)

	// Update task record with worktree info
	if updateErr := ws.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); updateErr != nil {
		log.Printf("[worktree] error updating worktree info: %v", updateErr)
	}
	if task.MergeTargetBranch == "" {
		// For chained tasks, set the merge target to the parent's branch if available,
		// otherwise use the standard target
		mergeTarget := baseRef
		if task.BaseBranch != "" {
			// Use parent's branch as merge target so changes merge back correctly
			mergeTarget = task.BaseBranch
		}
		task.MergeTargetBranch = mergeTarget
		if updateErr := ws.taskRepo.UpdateAutoMerge(ctx, task.ID, task.AutoMerge, mergeTarget); updateErr != nil {
			log.Printf("[worktree] error setting merge target branch: %v", updateErr)
		}
	}

	return worktreePath, branchName, nil
}

// SyncWorktreeFromMainAtStart updates a task branch with the latest main/default branch
// before task execution begins. It only runs when the worktree is clean.
func (ws *WorktreeService) SyncWorktreeFromMainAtStart(ctx context.Context, task *models.Task, repoDir string) error {
	if task == nil || task.WorktreePath == "" {
		return nil
	}
	authEnv := []string(nil)
	if ws.githubSvc != nil {
		authEnv = ws.githubSvc.GitAuthEnvForRepo(ctx, repoDir)
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
		if len(authEnv) > 0 {
			cmd.Env = append(cmd.Env, authEnv...)
		}
		return cmd.CombinedOutput()
	}

	currentBranch := GetCurrentBranch(task.WorktreePath)
	if currentBranch == "" {
		currentBranch = task.WorktreeBranch
	}
	log.Printf("[worktree] startup auto-merge check task=%s worktree=%s branch=%s", task.ID, task.WorktreePath, currentBranch)

	statusOut, statusErr := runGit(task.WorktreePath, "status", "--porcelain")
	if statusErr != nil {
		log.Printf("[worktree] startup auto-merge failed task=%s unable to read git status: %v", task.ID, statusErr)
		if ws.taskRepo != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		}
		return fmt.Errorf("could not check worktree status in %s: %w", task.WorktreePath, statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		log.Printf("[worktree] startup auto-merge skipped task=%s branch=%s reason=dirty_worktree", task.ID, currentBranch)
		return nil
	}

	syncBranch := "main"
	hasMain := false
	if _, err := runGit(repoDir, "show-ref", "--verify", "--quiet", "refs/heads/main"); err == nil {
		hasMain = true
	} else {
		_, err = runGit(repoDir, "show-ref", "--verify", "--quiet", "refs/remotes/origin/main")
		hasMain = err == nil
	}
	if !hasMain {
		syncBranch = GetDefaultBranch(repoDir)
	}

	mergeSource := syncBranch
	if _, originErr := runGit(repoDir, "remote", "get-url", "origin"); originErr == nil {
		fetchOut, fetchErr := runGit(task.WorktreePath, "fetch", "origin", syncBranch)
		if fetchErr != nil {
			log.Printf("[worktree] startup auto-merge task=%s fetch origin/%s skipped (non-fatal): %s", task.ID, syncBranch, strings.TrimSpace(string(fetchOut)))
			mergeSource = syncBranch
		} else {
			mergeSource = "origin/" + syncBranch
		}
	} else {
		log.Printf("[worktree] startup auto-merge task=%s no origin remote, using local %s", task.ID, syncBranch)
	}

	mergeOut, mergeErr := runGit(task.WorktreePath, "merge", "--no-edit", mergeSource)
	mergeMsg := strings.TrimSpace(string(mergeOut))
	if mergeErr != nil {
		conflictFiles := detectConflicts(task.WorktreePath)
		if len(conflictFiles) > 0 {
			abortErr := AbortMerge(task.WorktreePath)
			if ws.taskRepo != nil {
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
			}
			action := fmt.Sprintf("startup auto-merge conflict while merging %s into %s (conflicts: %s); merge was aborted. Resolve conflicts in %s and rerun the task", mergeSource, currentBranch, strings.Join(conflictFiles, ", "), task.WorktreePath)
			if abortErr != nil {
				action = fmt.Sprintf("%s; additionally, git merge --abort failed: %v", action, abortErr)
			}
			log.Printf("[worktree] startup auto-merge failed task=%s reason=conflict details=%s", task.ID, action)
			return fmt.Errorf("%s", action)
		}

		if ws.taskRepo != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		}
		if mergeMsg == "" {
			mergeMsg = mergeErr.Error()
		}
		log.Printf("[worktree] startup auto-merge failed task=%s branch=%s source=%s error=%s", task.ID, currentBranch, mergeSource, mergeMsg)
		return fmt.Errorf("startup auto-merge failed while merging %s into %s: %s", mergeSource, currentBranch, mergeMsg)
	}

	if mergeMsg == "" {
		mergeMsg = "already up to date"
	}
	log.Printf("[worktree] startup auto-merge ran task=%s branch=%s source=%s result=%s", task.ID, currentBranch, mergeSource, mergeMsg)
	return nil
}

// CommitWorktreeChanges stages and commits all changes in the worktree.
func CommitWorktreeChanges(worktreePath string, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("empty commit message")
	}

	// Check for changes
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = worktreePath
	out, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("checking git status: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil // no changes
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

	// First, commit any uncommitted changes in the worktree
	if task.WorktreePath != "" {
		if err := CommitWorktreeChanges(task.WorktreePath, fmt.Sprintf("Auto-commit changes for task: %s", task.Title)); err != nil {
			_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
			return &MergeResult{ErrorMessage: err.Error()}, fmt.Errorf("auto-commit before merge failed: %w", err)
		}
	}

	// Update merge status to pending
	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending)

	// Checkout target branch in the main repo
	checkoutCmd := exec.Command("git", "checkout", targetBranch)
	checkoutCmd.Dir = repoDir
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: fmt.Sprintf("checkout target: %s", string(out))}, fmt.Errorf("checkout target branch: %w", err)
	}

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
		_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusFailed)
		return &MergeResult{ErrorMessage: string(mergeOut)}, fmt.Errorf("merge failed: %w", mergeErr)
	}

	// For squash merge, we need to commit
	if mergeType == "squash" {
		commitCmd := exec.Command("git", "commit", "-m", fmt.Sprintf("Squash merge task: %s", task.Title))
		commitCmd.Dir = repoDir
		if out, err := commitCmd.CombinedOutput(); err != nil {
			log.Printf("[worktree] squash commit output: %s", string(out))
		}
	}

	// Get merge commit hash
	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashCmd.Dir = repoDir
	hashOut, _ := hashCmd.Output()

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)

	return &MergeResult{
		Success:     true,
		MergeCommit: strings.TrimSpace(string(hashOut)),
	}, nil
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
		return &MergeResult{Success: true}, nil
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

	// Commit the resolution
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = repoDir
	addCmd.Run()

	commitCmd := exec.Command("git", "commit", "--no-edit")
	commitCmd.Dir = repoDir
	commitCmd.Run()

	_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)

	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashCmd.Dir = repoDir
	hashOut, _ := hashCmd.Output()

	return &MergeResult{
		Success:     true,
		MergeCommit: strings.TrimSpace(string(hashOut)),
	}, nil
}

// CleanupWorktree removes the worktree and optionally deletes the branch.
func (ws *WorktreeService) CleanupWorktree(ctx context.Context, task *models.Task, repoDir string, deleteBranch bool) error {
	if task.WorktreePath == "" {
		return nil
	}

	// Check for uncommitted changes
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = task.WorktreePath
	out, err := statusCmd.Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return fmt.Errorf("worktree has uncommitted changes; commit or discard them first")
	}

	// Remove worktree
	removeCmd := exec.Command("git", "worktree", "remove", task.WorktreePath, "--force")
	removeCmd.Dir = repoDir
	if out, err := removeCmd.CombinedOutput(); err != nil {
		log.Printf("[worktree] error removing worktree: %s", string(out))
		// Try manual removal as fallback
		os.RemoveAll(task.WorktreePath)
		// Prune worktree list
		pruneCmd := exec.Command("git", "worktree", "prune")
		pruneCmd.Dir = repoDir
		pruneCmd.Run()
	}

	// Delete branch if requested, but guard against active descendants
	if deleteBranch && task.WorktreeBranch != "" {
		// Check if any descendants depend on this branch (non-terminal children)
		hasActiveDesc := false
		if ws.taskRepo != nil {
			active, descErr := ws.taskRepo.HasNonTerminalDescendants(ctx, task.ID)
			if descErr != nil {
				log.Printf("[worktree] error checking descendants for task %s: %v", task.ID, descErr)
			} else {
				hasActiveDesc = active
			}
		}
		if hasActiveDesc {
			log.Printf("[worktree] skipping branch deletion for task %s branch %s: has active descendants", task.ID, task.WorktreeBranch)
		} else {
			deleteCmd := exec.Command("git", "branch", "-D", task.WorktreeBranch)
			deleteCmd.Dir = repoDir
			if out, err := deleteCmd.CombinedOutput(); err != nil {
				log.Printf("[worktree] error deleting branch %s: %s", task.WorktreeBranch, string(out))
			}
		}
	}

	// Clear worktree info from task
	if err := ws.taskRepo.ClearWorktreeInfo(ctx, task.ID); err != nil {
		log.Printf("[worktree] error clearing worktree info: %v", err)
	}

	log.Printf("[worktree] cleaned up worktree for task %s", task.ID)
	return nil
}

// GetWorktreeDiff returns the diff between the worktree branch and the target branch.
func GetWorktreeDiff(repoDir string, branchName string, targetBranch string) string {
	if branchName == "" || targetBranch == "" {
		return ""
	}
	cmd := exec.Command("git", "diff", targetBranch+"..."+branchName)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		log.Printf("[worktree] error getting worktree diff: %v", err)
		return ""
	}
	return string(out)
}

// GetWorktreeDiffWithUncommitted returns the combined diff of committed branch changes
// plus any uncommitted changes in the worktree working directory. This provides a
// real-time view of all changes without needing to auto-commit during execution.
func GetWorktreeDiffWithUncommitted(repoDir string, branchName string, targetBranch string, worktreePath string) string {
	// Get committed branch diff
	committedDiff := GetWorktreeDiff(repoDir, branchName, targetBranch)

	if worktreePath == "" {
		return committedDiff
	}

	// Capture uncommitted changes in the worktree (staged + unstaged + untracked)
	uncommittedDiff := captureWorktreeUncommitted(worktreePath)

	if uncommittedDiff == "" {
		return committedDiff
	}
	if committedDiff == "" {
		return uncommittedDiff
	}
	return committedDiff + "\n" + uncommittedDiff
}

// captureWorktreeUncommitted captures all uncommitted changes (staged, unstaged,
// and untracked files) in a worktree directory as a unified diff.
func captureWorktreeUncommitted(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}

	// git diff HEAD captures staged + unstaged changes
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	result := string(out)

	// Also capture untracked files
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
					result += fileDiff
				}
			}
		}
	}

	return result
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

	var stats []WorktreeFileStat
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		status := "modified"
		switch parts[0] {
		case "A":
			status = "added"
		case "D":
			status = "deleted"
		case "M":
			status = "modified"
		}
		stats = append(stats, WorktreeFileStat{Path: parts[1], Status: status})
	}
	return stats
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
func IsBranchMerged(repoDir string, branchName string, targetBranch string) bool {
	if branchName == "" || targetBranch == "" {
		return false
	}

	// Check if branch exists
	checkCmd := exec.Command("git", "rev-parse", "--verify", branchName)
	checkCmd.Dir = repoDir
	if err := checkCmd.Run(); err != nil {
		// Branch doesn't exist (might have been manually deleted)
		return true
	}

	// Use git merge-base --is-ancestor to check if branch is merged
	// This checks if the branch tip is reachable from target branch
	cmd := exec.Command("git", "merge-base", "--is-ancestor", branchName, targetBranch)
	cmd.Dir = repoDir
	err := cmd.Run()

	// Exit code 0 means ancestor (merged), non-zero means not merged
	return err == nil
}

// HandlePostExecution handles worktree operations after task execution completes.
// Called by the LLM service after a task finishes successfully.
func (ws *WorktreeService) HandlePostExecution(ctx context.Context, task *models.Task, repoDir string) {
	if task.WorktreePath == "" || task.WorktreeBranch == "" {
		return
	}

	// Commit any changes in the worktree
	msg := fmt.Sprintf("Task completed: %s", task.Title)
	if err := CommitWorktreeChanges(task.WorktreePath, msg); err != nil {
		log.Printf("[worktree] error committing changes for task %s: %v", task.ID, err)
	}

	// Auto-merge if enabled
	if task.AutoMerge {
		log.Printf("[worktree] auto-merging task %s branch %s -> %s", task.ID, task.WorktreeBranch, task.MergeTargetBranch)
		result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
		if err != nil {
			log.Printf("[worktree] auto-merge failed for task %s: %v", task.ID, err)
			return
		}
		if !result.Success && len(result.ConflictFiles) > 0 {
			log.Printf("[worktree] auto-merge has conflicts for task %s, attempting AI resolution", task.ID)
			aiResult, aiErr := ws.ResolveConflictsWithAI(ctx, task, repoDir)
			if aiErr != nil || (aiResult != nil && !aiResult.Success) {
				log.Printf("[worktree] AI conflict resolution failed for task %s, aborting merge", task.ID)
				AbortMerge(repoDir)
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict)
				return
			}
		}

		// Cleanup after successful merge if policy says so
		policy := ws.GetCleanupPolicy(ctx)
		if policy == "after_merge" {
			if cleanErr := ws.CleanupWorktree(ctx, task, repoDir, true); cleanErr != nil {
				log.Printf("[worktree] cleanup after merge failed: %v", cleanErr)
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

	log.Printf("[worktree] cleanup scan: checking %d tasks with worktrees", len(tasks))

	cleanedCount := 0
	for _, task := range tasks {
		// Skip tasks that are currently running or pending — their worktrees are in use
		if task.Status == models.StatusRunning || task.Status == models.StatusPending || task.Status == models.StatusQueued {
			continue
		}

		// Get the project to determine the repo directory
		project, err := ws.projectRepo.GetByID(ctx, task.ProjectID)
		if err != nil || project == nil {
			log.Printf("[worktree] cleanup: skipping task %s (project not found)", task.ID)
			continue
		}

		repoDir := project.RepoPath
		if repoDir == "" || !IsGitRepo(repoDir) {
			log.Printf("[worktree] cleanup: skipping task %s (not a git repo)", task.ID)
			continue
		}

		targetBranch := task.MergeTargetBranch
		if targetBranch == "" {
			targetBranch = ws.getGlobalMergeTarget(ctx)
		}
		if targetBranch == "" {
			targetBranch = GetDefaultBranch(repoDir)
		}

		// Check if branch has been merged
		if IsBranchMerged(repoDir, task.WorktreeBranch, targetBranch) {
			log.Printf("[worktree] cleanup: task %s branch %s is merged to %s, cleaning up",
				task.ID, task.WorktreeBranch, targetBranch)

			// Update merge status to merged if not already
			if task.MergeStatus != models.MergeStatusMerged {
				_ = ws.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged)
			}

			// Cleanup the worktree and delete the branch
			if err := ws.CleanupWorktree(ctx, &task, repoDir, true); err != nil {
				log.Printf("[worktree] cleanup: failed to cleanup task %s: %v", task.ID, err)
			} else {
				cleanedCount++
			}
		}
	}

	if cleanedCount > 0 {
		log.Printf("[worktree] cleanup scan: cleaned up %d merged worktrees", cleanedCount)
	}

	// Also cleanup orphaned worktrees (worktrees with no corresponding task)
	orphanedCount, err := ws.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		log.Printf("[worktree] cleanup: failed to cleanup orphaned worktrees: %v", err)
	} else if orphanedCount > 0 {
		log.Printf("[worktree] cleanup scan: cleaned up %d orphaned worktrees", orphanedCount)
	}

	return nil
}

// CleanupOrphanedWorktrees removes worktrees that exist on disk but have no corresponding task in the database.
// This can happen when tasks are deleted but their worktrees weren't cleaned up.
// Returns the number of orphaned worktrees cleaned up.
func (ws *WorktreeService) CleanupOrphanedWorktrees(ctx context.Context) (int, error) {
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
		if project.RepoPath == "" || !IsGitRepo(project.RepoPath) {
			continue
		}

		// List all git worktrees for this repo
		worktrees, err := ListGitWorktrees(project.RepoPath)
		if err != nil {
			log.Printf("[worktree] cleanup: failed to list worktrees for project %s: %v", project.ID, err)
			continue
		}

		// Get all tasks for this project. We need both:
		// 1) knownPaths (worktree path already recorded in DB)
		// 2) knownTaskIDs (task exists but may not have worktree_path persisted yet)
		allTasks, err := ws.taskRepo.ListByProject(ctx, project.ID, "")
		if err != nil {
			log.Printf("[worktree] cleanup: failed to list tasks for project %s: %v", project.ID, err)
			continue
		}

		// Build maps of known paths and known task IDs.
		knownPaths := make(map[string]bool)
		knownTaskIDs := make(map[string]bool)
		for _, task := range allTasks {
			knownTaskIDs[task.ID] = true
			if task.WorktreePath != "" {
				knownPaths[task.WorktreePath] = true
			}
		}

		// Check each worktree to see if it's orphaned
		for _, worktree := range worktrees {
			// Skip the main worktree (the original repo)
			if worktree.IsMain {
				continue
			}

			// Known in DB, not orphaned.
			if knownPaths[worktree.Path] {
				continue
			}

			// Worktree directories follow .worktrees/task_<taskID>. If the task still
			// exists but worktree_path wasn't persisted yet, treat it as in-use.
			if taskID, ok := taskIDFromWorktreePath(worktree.Path); ok && knownTaskIDs[taskID] {
				log.Printf("[worktree] cleanup: skipping worktree at %s because task %s still exists", worktree.Path, taskID)
				continue
			}

			log.Printf("[worktree] cleanup: found orphaned worktree at %s (branch: %s)", worktree.Path, worktree.Branch)

			// Try to remove the worktree using git first
			cmd := exec.Command("git", "worktree", "remove", "--force", worktree.Path)
			cmd.Dir = project.RepoPath
			if output, err := cmd.CombinedOutput(); err != nil {
				outputText := string(output)

				// A locked worktree may still be actively initializing. Don't perform
				// manual filesystem deletion in this case; retry on a future cleanup cycle.
				if strings.Contains(outputText, "cannot remove a locked working tree") {
					log.Printf("[worktree] cleanup: skipping locked orphaned worktree at %s (output: %s)", worktree.Path, outputText)
					continue
				}

				// If git worktree remove fails, try manual cleanup
				log.Printf("[worktree] cleanup: git worktree remove failed, attempting manual cleanup: %v (output: %s)", err, outputText)

				// Remove the worktree directory manually
				if err := os.RemoveAll(worktree.Path); err != nil {
					log.Printf("[worktree] cleanup: failed to remove orphaned worktree directory %s: %v", worktree.Path, err)
					continue
				}

				// Prune stale worktree entries
				pruneCmd := exec.Command("git", "worktree", "prune")
				pruneCmd.Dir = project.RepoPath
				_ = pruneCmd.Run() // Ignore errors
			}

			// Delete the branch if it exists
			if worktree.Branch != "" {
				cmd = exec.Command("git", "branch", "-D", worktree.Branch)
				cmd.Dir = project.RepoPath
				_ = cmd.Run() // Ignore errors - branch might already be deleted
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
	if taskID == "" {
		return "", false
	}
	return taskID, true
}

// WorktreeInfo represents information about a git worktree.
type WorktreeInfo struct {
	Path   string
	Branch string
	IsMain bool
}

// ListGitWorktrees lists all worktrees for a git repository.
func ListGitWorktrees(repoDir string) ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
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
