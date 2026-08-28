package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// createTestGitRepo creates a temporary git repository with an initial commit.
func createTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	// Create initial file and commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial commit failed: %v\n%s", err, out)
	}

	return dir
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitRevParseTest(t *testing.T, dir string, rev string) string {
	t.Helper()
	return runGitTest(t, dir, "rev-parse", rev)
}

func createBareTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init bare failed: %v\n%s", err, out)
	}
	return dir
}

func cloneTestGitRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", remote, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}
	runGitTest(t, dir, "config", "user.email", "test@test.com")
	runGitTest(t, dir, "config", "user.name", "Test")
	return dir
}

func writeAndCommitTestFile(t *testing.T, dir, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", name)
	runGitTest(t, dir, "commit", "-m", message)
}

func TestIsGitRepo(t *testing.T) {
	// Non-git directory
	tmpDir := t.TempDir()
	if IsGitRepo(tmpDir) {
		t.Error("expected non-git dir to return false")
	}

	// Git directory
	gitDir := createTestGitRepo(t)
	if !IsGitRepo(gitDir) {
		t.Error("expected git dir to return true")
	}

	// Empty string
	if IsGitRepo("") {
		t.Error("expected empty string to return false")
	}
}

func TestGetDefaultBranch(t *testing.T) {
	repoDir := createTestGitRepo(t)
	branch := GetDefaultBranch(repoDir)
	// Should be either "main" or "master" depending on git config
	if branch != "main" && branch != "master" {
		t.Errorf("expected main or master, got %q", branch)
	}
}

func TestGetCurrentBranch(t *testing.T) {
	repoDir := createTestGitRepo(t)
	branch := GetCurrentBranch(repoDir)
	if branch == "" {
		t.Error("expected non-empty branch name")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"Fix Bug #123", "fix-bug-123"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special!@#Characters", "special-characters"},
		{"", ""},
		{"A very long title that should be truncated to fifty characters maximum length", "a-very-long-title-that-should-be-truncated-to-fift"},
	}

	for _, tc := range tests {
		got := slugify(tc.input)
		if got != tc.expected {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestWorktreeCommitLabelHelpers(t *testing.T) {
	if got := commonChangeLabel(nil); got != "changes" {
		t.Fatalf("commonChangeLabel(nil) = %q", got)
	}
	if got := commonChangeLabel([]worktreeCommitChange{
		{Path: "internal/service/foo.go"},
		{Path: "internal/service/bar.go"},
	}); got != "internal service files" {
		t.Fatalf("common directory label = %q", got)
	}
	if got := commonChangeLabel([]worktreeCommitChange{
		{Path: "internal/service/task_handler.go"},
		{Path: "web/src/task_panel.tsx"},
	}); got != "tasks" {
		t.Fatalf("common base word label = %q", got)
	}
	if got := commonChangeLabel([]worktreeCommitChange{
		{Path: "internal/service/task_handler.go"},
		{Path: "web/src/dashboard_panel.tsx"},
	}); got != "2 files" {
		t.Fatalf("fallback change label = %q", got)
	}
	if got := pathTokens("internal/service/task_handler_test.go"); strings.Join(got, ",") != "internal,service,task,handler,test" {
		t.Fatalf("pathTokens = %#v", got)
	}
	if got := pathTokens(" "); got != nil {
		t.Fatalf("blank pathTokens = %#v", got)
	}
	if got := pluralizeCommitLabel(""); got != "files" {
		t.Fatalf("pluralize empty = %q", got)
	}
	if got := pluralizeCommitLabel("class"); got != "class" {
		t.Fatalf("pluralize suffix-s = %q", got)
	}
	if got := pluralizeCommitLabel("task"); got != "tasks" {
		t.Fatalf("pluralize task = %q", got)
	}
}

func TestParseGitStatusFileStats(t *testing.T) {
	stats := parseGitStatusFileStats([]byte(" M internal/service/foo.go\nA  new/file.go\nR  old.go -> newer.go\n?? scratch.txt\nD  removed.go\n"))
	if len(stats) != 5 {
		t.Fatalf("stats length = %d, want 5: %#v", len(stats), stats)
	}
	want := []WorktreeFileStat{
		{Path: "internal/service/foo.go", Status: "modified"},
		{Path: "new/file.go", Status: "added"},
		{Path: "newer.go", Status: "modified"},
		{Path: "scratch.txt", Status: "added"},
		{Path: "removed.go", Status: "deleted"},
	}
	for i := range want {
		if stats[i] != want[i] {
			t.Fatalf("stats[%d] = %#v, want %#v", i, stats[i], want[i])
		}
	}
}

func TestParseWorktreeNameStatus(t *testing.T) {
	input := []byte("\nM\tmodified.go\nA\tadded.go\nD\tdeleted.go\nR100\told.go\tnew.go\nC75\tsource.go\tcopy.go\nmalformed\nM\n")
	got := parseWorktreeNameStatus(input)
	want := []worktreeNameStatusRecord{
		{Status: "M", Path: "modified.go"},
		{Status: "A", Path: "added.go"},
		{Status: "D", Path: "deleted.go"},
		{Status: "R100", Path: "new.go", SourcePath: "old.go"},
		{Status: "C75", Path: "copy.go", SourcePath: "source.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %#v, want %#v", got, want)
	}
}

func TestParseWorktreeNameStatusProjections(t *testing.T) {
	input := []byte("\nM\tmodified.go\nA\tadded.go\nD\tdeleted.go\nR100\told.go\tnew.go\nC75\tsource.go\tcopy.go\nmalformed\nM\n")

	targets := parseWorktreeDiffFileTargets(input)
	wantTargets := []worktreeDiffFileTarget{
		{Path: "modified.go", Pathspecs: []string{"modified.go"}},
		{Path: "added.go", Pathspecs: []string{"added.go"}},
		{Path: "deleted.go", Pathspecs: []string{"deleted.go"}},
		{Path: "new.go", Pathspecs: []string{"old.go", "new.go"}},
		{Path: "copy.go", Pathspecs: []string{"source.go", "copy.go"}},
	}
	if !reflect.DeepEqual(targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", targets, wantTargets)
	}

	stats := parseWorktreeFileStats(input)
	wantStats := []WorktreeFileStat{
		{Path: "modified.go", Status: "modified"},
		{Path: "added.go", Status: "added"},
		{Path: "deleted.go", Status: "deleted"},
		{Path: "new.go", Status: "modified"},
		{Path: "copy.go", Status: "modified"},
	}
	if !reflect.DeepEqual(stats, wantStats) {
		t.Fatalf("stats = %#v, want %#v", stats, wantStats)
	}
}

func TestWorktreeGitStateHelpersDetectMergedBranchesAndConflicts(t *testing.T) {
	repoDir := createTestGitRepo(t)
	ws := &WorktreeService{}

	if ws.isBranchTipMergedIntoTarget(repoDir, "", "main") {
		t.Fatal("blank branch should not be considered merged")
	}
	if ws.isBranchTipMergedIntoTarget(repoDir, "missing", "main") {
		t.Fatal("missing branch should not be considered merged")
	}

	runGitTest(t, repoDir, "checkout", "-b", "feature")
	writeAndCommitTestFile(t, repoDir, "feature.txt", "feature", "add feature")
	if ws.isBranchTipMergedIntoTarget(repoDir, "feature", "main") {
		t.Fatal("unmerged feature branch reported merged")
	}
	if !ws.branchHasCommitsBeyondTarget(repoDir, "feature", "main") {
		t.Fatal("feature branch should have commits beyond main")
	}

	runGitTest(t, repoDir, "checkout", "main")
	runGitTest(t, repoDir, "merge", "--ff-only", "feature")
	if !ws.isBranchTipMergedIntoTarget(repoDir, "feature", "main") {
		t.Fatal("merged feature branch was not detected")
	}
	if ws.branchHasCommitsBeyondTarget(repoDir, "feature", "main") {
		t.Fatal("merged feature branch should not have commits beyond main")
	}
	if IsBranchBehindTarget(repoDir, "feature", "main") {
		t.Fatal("fast-forwarded branch should not be behind main")
	}

	runGitTest(t, repoDir, "checkout", "-b", "diverged")
	writeAndCommitTestFile(t, repoDir, "diverged.txt", "branch", "branch-only commit")
	runGitTest(t, repoDir, "checkout", "main")
	writeAndCommitTestFile(t, repoDir, "main.txt", "target", "target-only commit")
	if !IsBranchBehindTarget(repoDir, "diverged", "main") {
		t.Fatal("diverged branch should be behind target")
	}
	if !IsBranchDivergedFromTarget(repoDir, "diverged", "main") {
		t.Fatal("diverged branch was not detected")
	}
	stats := GetWorktreeFileStats(repoDir, "diverged", "main")
	if len(stats) != 1 || stats[0].Path != "diverged.txt" || stats[0].Status != "added" {
		t.Fatalf("branch file stats = %#v", stats)
	}

	mergeHead := filepath.Join(repoDir, ".git", "MERGE_HEAD")
	if err := os.WriteFile(mergeHead, []byte("pending"), 0o644); err != nil {
		t.Fatalf("write MERGE_HEAD: %v", err)
	}
	if !worktreeHasActiveMerge(repoDir) {
		t.Fatal("active merge was not detected")
	}
	if err := os.Remove(mergeHead); err != nil {
		t.Fatalf("remove MERGE_HEAD: %v", err)
	}
	if worktreeHasActiveMerge(repoDir) {
		t.Fatal("active merge remained after MERGE_HEAD removal")
	}

	writeAndCommitTestFile(t, repoDir, "conflicted.txt", "base\n", "add conflict base")
	runGitTest(t, repoDir, "checkout", "-b", "left")
	writeAndCommitTestFile(t, repoDir, "conflicted.txt", "left\n", "left conflict")
	runGitTest(t, repoDir, "checkout", "main")
	runGitTest(t, repoDir, "checkout", "-b", "right")
	writeAndCommitTestFile(t, repoDir, "conflicted.txt", "right\n", "right conflict")
	runGitTest(t, repoDir, "checkout", "left")
	mergeCmd := exec.Command("git", "merge", "right")
	mergeCmd.Dir = repoDir
	if err := mergeCmd.Run(); err == nil {
		t.Fatal("expected merge conflict")
	}
	if !worktreeHasConflictFiles(repoDir) {
		t.Fatal("unmerged conflict file was not detected")
	}
	if err := AbortMerge(repoDir); err != nil {
		t.Fatalf("abort merge: %v", err)
	}
	if worktreeHasConflictFiles(repoDir) {
		t.Fatal("clean file reported as conflicted")
	}
}

func TestClearStaleConflictStatusIfCleanUpdatesTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	ws := NewWorktreeService(taskRepo, repository.NewProjectRepo(db), settingsRepo)
	repoDir := createTestGitRepo(t)

	if err := settingsRepo.Set(ctx, "worktree_auto_merge", "true"); err != nil {
		t.Fatalf("set auto merge: %v", err)
	}
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "manual"); err != nil {
		t.Fatalf("set cleanup policy: %v", err)
	}
	if err := settingsRepo.Set(ctx, "worktree_merge_target", "release"); err != nil {
		t.Fatalf("set merge target: %v", err)
	}
	if !ws.GetGlobalAutoMerge(ctx) || ws.GetCleanupPolicy(ctx) != "manual" || ws.getGlobalMergeTarget(ctx) != "release" {
		t.Fatal("persisted worktree merge settings were not read")
	}

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Stale Conflict",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreePath:      repoDir,
		WorktreeBranch:    "feature",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusConflict,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ws.clearStaleConflictStatusIfClean(ctx, task)
	if task.MergeStatus != models.MergeStatusPending {
		t.Fatalf("task merge status = %q, want pending", task.MergeStatus)
	}
	persisted, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if persisted.MergeStatus != models.MergeStatusPending {
		t.Fatalf("persisted merge status = %q, want pending", persisted.MergeStatus)
	}

	if err := taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		t.Fatalf("reset conflict status: %v", err)
	}
	task.MergeStatus = models.MergeStatusConflict
	if err := os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	ws.clearStaleConflictStatusIfClean(ctx, task)
	if task.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("dirty task merge status = %q, want conflict", task.MergeStatus)
	}
}

func TestSetupWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Worktree Task",
		Category:  models.CategoryActive,
		Priority:  1,
		Prompt:    "Do something",
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Setup worktree
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}

	if wtPath == "" {
		t.Error("expected non-empty worktree path")
	}
	if branchName == "" {
		t.Error("expected non-empty branch name")
	}
	if !strings.HasPrefix(branchName, "task/") {
		t.Errorf("expected branch to start with 'task/', got %q", branchName)
	}

	// Verify worktree directory exists
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		t.Error("worktree directory should exist")
	}

	// Verify task was updated in DB
	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbTask.WorktreePath == "" {
		t.Error("expected worktree_path to be set in DB")
	}
	if dbTask.WorktreeBranch == "" {
		t.Error("expected worktree_branch to be set in DB")
	}
}

func TestSetupWorktree_ReusesStoredWorktreeWhenBaseNoLongerExists(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ws := NewWorktreeService(taskRepo, repository.NewProjectRepo(db), repository.NewSettingsRepo(db))
	ctx := context.Background()
	repoDir := createTestGitRepo(t)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Reuse Stored Worktree",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch
	task.MergeTargetBranch = "renamed-or-deleted-base"

	reusedPath, reusedBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if reusedPath != wtPath || reusedBranch != wtBranch {
		t.Fatalf("expected stored worktree %q on %q, got %q on %q", wtPath, wtBranch, reusedPath, reusedBranch)
	}
}

func TestSetupWorktree_PreservesOperationalBaseVerificationError(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	ws := NewWorktreeService(taskRepo, repository.NewProjectRepo(db), repository.NewSettingsRepo(db))
	repoDir := createTestGitRepo(t)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Cancelled Worktree Setup",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: "main",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := ws.SetupWorktree(ctx, task, repoDir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to be preserved, got %v", err)
	}
	if strings.Contains(err.Error(), "create an initial local commit") {
		t.Fatalf("expected operational error instead of missing-commit guidance, got %v", err)
	}
}

func TestSetupWorktree_NotGitRepo(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()
	_, _, err := ws.SetupWorktree(ctx, task, tmpDir)
	if err == nil {
		t.Error("expected error for non-git directory")
	}
}

func TestSyncWorktreeFromMainAtStart_CleanWorktreeAutoMergeSuccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Success",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	// Create new commit on main/default branch after worktree was created.
	newMainFile := filepath.Join(repoDir, "main_update.txt")
	if err := os.WriteFile(newMainFile, []byte("from main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "main_update.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add on main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main update")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit on main: %v\n%s", err, out)
	}

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("SyncWorktreeFromMainAtStart: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "main_update.txt")); err != nil {
		t.Fatalf("expected main_update.txt in worktree after startup sync: %v", err)
	}

	checkStatus := exec.Command("git", "status", "--porcelain")
	checkStatus.Dir = wtPath
	statusOut, err := checkStatus.Output()
	if err != nil {
		t.Fatalf("git status in worktree: %v", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		t.Fatalf("expected clean worktree after startup sync, got status: %s", string(statusOut))
	}
}

func TestSyncWorktreeFromMainAtStart_DirtyWorktreeSkipsAutoMerge(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Dirty Skip",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	// Make worktree dirty (untracked file).
	if err := os.WriteFile(filepath.Join(wtPath, "local_task_change.txt"), []byte("wip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create new commit on main/default branch.
	if err := os.WriteFile(filepath.Join(repoDir, "main_dirty_skip_update.txt"), []byte("from main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "main_dirty_skip_update.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add on main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main update for dirty skip")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit on main: %v\n%s", err, out)
	}

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("expected dirty worktree skip without error, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "main_dirty_skip_update.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected main update to not be merged into dirty worktree")
	}
	if _, err := os.Stat(filepath.Join(wtPath, "local_task_change.txt")); err != nil {
		t.Fatalf("expected local dirty file to remain: %v", err)
	}
}

func TestSyncWorktreeFromMainAtStart_ConflictFailsGracefully(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Conflict",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	// Commit change in worktree branch.
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("task-branch-change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task branch update readme"); err != nil {
		t.Fatalf("CommitWorktreeChanges in worktree: %v", err)
	}

	// Commit conflicting change on main/default branch.
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("main-branch-change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "README.md")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add on main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main branch update readme")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit on main: %v\n%s", err, out)
	}

	err = ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir)
	if err == nil {
		t.Fatal("expected startup auto-merge conflict error")
	}
	var conflictErr *StartupSyncConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected typed startup auto-merge conflict error, got: %T %v", err, err)
	}
	if conflictErr.TargetBranch != defaultBranch || conflictErr.TaskBranch != wtBranch || conflictErr.WorktreePath != wtPath {
		t.Fatalf("unexpected startup conflict details: %+v", conflictErr)
	}
	if len(conflictErr.ConflictFiles) != 1 || conflictErr.ConflictFiles[0] != "README.md" {
		t.Fatalf("expected README.md conflict, got %v", conflictErr.ConflictFiles)
	}

	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if dbTask.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected merge_status=conflict, got %q", dbTask.MergeStatus)
	}

	// Confirm repository is recoverable: no in-progress merge remains.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wtPath
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status in worktree: %v", err)
	}
	if strings.Contains(string(statusOut), "UU ") {
		t.Fatalf("expected no unresolved conflict entries after abort, got status: %s", string(statusOut))
	}
	headCmd := exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD")
	headCmd.Dir = wtPath
	if out, err := headCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected MERGE_HEAD to be absent after aborted startup merge, got: %s", strings.TrimSpace(string(out)))
	}
}

func TestSyncWorktreeFromMainAtStart_OriginMainDivergenceDoesNotCauseStartupConflictByDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	bare := createBareTestGitRepo(t)
	seed := createTestGitRepo(t)
	runGitTest(t, seed, "remote", "add", "origin", bare)
	runGitTest(t, seed, "push", "-u", "origin", "main")

	repoDir := cloneTestGitRepo(t, bare)
	writeAndCommitTestFile(t, repoDir, "README.md", "local main is source of truth\n", "local main update")

	remoteClone := cloneTestGitRepo(t, bare)
	writeAndCommitTestFile(t, remoteClone, "README.md", "origin main should not be merged\n", "origin main update")
	runGitTest(t, remoteClone, "push", "origin", "main")
	runGitTest(t, repoDir, "fetch", "origin", "main")

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Ignores Diverged Origin",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: "main",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("expected startup sync to keep local main despite diverged origin/main, got: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(wtPath, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "local main is source of truth\n" {
		t.Fatalf("expected worktree to keep local main content, got %q", string(content))
	}

	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if dbTask.MergeStatus == models.MergeStatusConflict {
		t.Fatal("expected origin/main divergence not to set startup merge conflict status")
	}
}

func TestSyncWorktreeFromMainAtStart_OriginMainExistenceAloneDoesNotChangeSyncSource(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	bare := createBareTestGitRepo(t)
	seed := createTestGitRepo(t)
	runGitTest(t, seed, "remote", "add", "origin", bare)
	runGitTest(t, seed, "push", "-u", "origin", "main")

	repoDir := cloneTestGitRepo(t, bare)
	writeAndCommitTestFile(t, repoDir, "local_only.txt", "local main\n", "local main only")

	remoteClone := cloneTestGitRepo(t, bare)
	writeAndCommitTestFile(t, remoteClone, "origin_only.txt", "origin main\n", "origin main only")
	runGitTest(t, remoteClone, "push", "origin", "main")
	runGitTest(t, repoDir, "fetch", "origin", "main")

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Ignores Origin Existence",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: "main",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("SyncWorktreeFromMainAtStart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "local_only.txt")); err != nil {
		t.Fatalf("expected local main file in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "origin_only.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected origin/main-only file not to be merged by startup sync")
	}
}

func TestSyncWorktreeFromMainAtStart_BrokenOriginStillUsesLocalBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	runGitTest(t, repoDir, "remote", "add", "origin", "/tmp/nonexistent-openvibely-origin")

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Broken Origin Ignored",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	writeAndCommitTestFile(t, repoDir, "local_sync_file.txt", "main update\n", "main update with broken origin")

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("expected local merge without contacting broken origin, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "local_sync_file.txt")); err != nil {
		t.Fatalf("expected local main branch update to be merged into worktree: %v", err)
	}
}

func TestSyncWorktreeFromMainAtStart_UpstreamOnlyUsesLocalBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	upstream := createBareTestGitRepo(t)
	runGitTest(t, repoDir, "remote", "add", "upstream", upstream)
	runGitTest(t, repoDir, "push", "-u", "upstream", defaultBranch)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Upstream Only",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	writeAndCommitTestFile(t, repoDir, "upstream_local_sync.txt", "local main\n", "local update with upstream remote")

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("SyncWorktreeFromMainAtStart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "upstream_local_sync.txt")); err != nil {
		t.Fatalf("expected startup sync to merge local branch with only upstream remote: %v", err)
	}
}

func TestSyncWorktreeFromMainAtStart_UsesMergeTargetBranchWhenSet(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	runGitTest(t, repoDir, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repoDir, "develop_only.txt"), []byte("from develop\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "develop_only.txt")
	runGitTest(t, repoDir, "commit", "-m", "develop update")
	runGitTest(t, repoDir, "checkout", defaultBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "main_only.txt"), []byte("from main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "main_only.txt")
	runGitTest(t, repoDir, "commit", "-m", "main-only update")

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Startup Sync Develop Target",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: "develop",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("SyncWorktreeFromMainAtStart: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "develop_only.txt")); err != nil {
		t.Fatalf("expected develop_only.txt from merge target in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "main_only.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected default-branch-only file not to be merged from main")
	}
}

func TestSetupFollowupWorktree_CompletedMergedTaskStartsFreshFromCurrentTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Merged Followup",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeStatus:       models.MergeStatusMerged,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	oldPath, oldBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = oldPath
	task.WorktreeBranch = oldBranch
	if err := os.WriteFile(filepath.Join(repoDir, "target.txt"), []byte("current target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "target.txt")
	runGitTest(t, repoDir, "commit", "-m", "target update")

	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected fresh current-target follow-up to skip startup sync")
	}
	if wtPath == oldPath || wtBranch == oldBranch {
		t.Fatalf("expected fresh follow-up worktree/branch, got path=%s branch=%s", wtPath, wtBranch)
	}
	if !strings.Contains(wtBranch, "-followup-") {
		t.Fatalf("expected follow-up branch, got %q", wtBranch)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "target.txt")); err != nil {
		t.Fatalf("expected fresh follow-up worktree based on current target: %v", err)
	}
}

func TestSetupFollowupWorktree_FailedConflictRetryStartsFreshFromCurrentTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Failed Conflict Retry",
		Category:          models.CategoryBacklog,
		Status:            models.StatusFailed,
		MergeStatus:       models.MergeStatusConflict,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	oldPath, oldBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = oldPath
	task.WorktreeBranch = oldBranch
	if err := os.WriteFile(filepath.Join(oldPath, "duplicate.txt"), []byte("task version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(oldPath, "task duplicate version"); err != nil {
		t.Fatalf("CommitWorktreeChanges: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "duplicate.txt"), []byte("accepted target version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "duplicate.txt")
	runGitTest(t, repoDir, "commit", "-m", "accepted duplicate version")

	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected conflict retry to start from current target and skip startup sync")
	}
	if wtPath == oldPath || wtBranch == oldBranch {
		t.Fatalf("expected retry to avoid stale original worktree, got path=%s branch=%s", wtPath, wtBranch)
	}
	status := runGitTest(t, oldPath, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("expected aborted stale worktree to remain clean, got status=%q", status)
	}
}

func TestSetupFollowupWorktree_ReusesStoredFollowupWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Reuse Followup",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeStatus:       models.MergeStatusMerged,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("first SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected first merged follow-up to create fresh worktree")
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	reusedPath, reusedBranch, reusedSkip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("second SetupFollowupWorktree: %v", err)
	}
	if reusedPath != wtPath || reusedBranch != wtBranch {
		t.Fatalf("expected clean read-only follow-up worktree reuse, got path=%s branch=%s", reusedPath, reusedBranch)
	}
	if reusedSkip {
		t.Fatal("expected reused worktree to allow normal startup sync")
	}
}

func TestSetupFollowupWorktree_DoesNotReuseStaleCommittedFollowupWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Stale Committed Followup",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeStatus:       models.MergeStatusMerged,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected initial fresh follow-up")
	}
	if err := os.WriteFile(filepath.Join(wtPath, "squashed.txt"), []byte("followup version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "followup stale commit"); err != nil {
		t.Fatalf("CommitWorktreeChanges: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "squashed.txt"), []byte("accepted target version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "squashed.txt")
	runGitTest(t, repoDir, "commit", "-m", "accepted squashed followup")
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	newPath, newBranch, newSkip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree retry: %v", err)
	}
	if !newSkip {
		t.Fatal("expected stale committed follow-up to start fresh from target")
	}
	if newPath == wtPath || newBranch == wtBranch {
		t.Fatalf("expected stale committed follow-up to not be reused, got path=%s branch=%s", newPath, newBranch)
	}
}

func TestSetupFollowupWorktree_PreservesDirtyStoredFollowupWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Dirty Followup",
		Category:          models.CategoryBacklog,
		Status:            models.StatusFailed,
		MergeStatus:       models.MergeStatusMerged,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected first follow-up to start fresh")
	}
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("unsaved follow-up work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	reusedPath, reusedBranch, reusedSkip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree retry: %v", err)
	}
	if reusedPath != wtPath || reusedBranch != wtBranch {
		t.Fatalf("expected dirty follow-up worktree to be preserved, got path=%s branch=%s", reusedPath, reusedBranch)
	}
	if reusedSkip {
		t.Fatal("expected reused dirty follow-up worktree not to skip startup sync decision")
	}
}

func TestSetupFollowupWorktree_CurrentTargetPersistsResolvedMergeTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	runGitTest(t, repoDir, "checkout", "-b", "historical-base")
	runGitTest(t, repoDir, "checkout", defaultBranch)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Persist Target",
		Category:    models.CategoryCompleted,
		Status:      models.StatusCompleted,
		MergeStatus: models.MergeStatusMerged,
		BaseBranch:  "historical-base",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, skip, err := ws.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupFollowupWorktree: %v", err)
	}
	if !skip {
		t.Fatal("expected current-target follow-up setup")
	}
	if wtPath == "" || wtBranch == "" {
		t.Fatalf("expected worktree metadata, got path=%q branch=%q", wtPath, wtBranch)
	}
	got, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeTargetBranch != defaultBranch {
		t.Fatalf("expected merge target %q, got %q", defaultBranch, got.MergeTargetBranch)
	}
}

func TestSyncWorktreeFromMainAtStart_DoesNotClearDirtyConflictStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Dirty Conflict State",
		Category:          models.CategoryActive,
		Status:            models.StatusFailed,
		MergeStatus:       models.MergeStatusConflict,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch
	if err := os.WriteFile(filepath.Join(wtPath, "manual-resolution.txt"), []byte("manual work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ws.SyncWorktreeFromMainAtStart(ctx, task, repoDir); err != nil {
		t.Fatalf("expected dirty worktree to skip startup sync without error: %v", err)
	}
	got, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected dirty conflict status to be preserved, got %q", got.MergeStatus)
	}
}

func TestBuildWorktreeCommitMessage_DiffSummaryBeatsUnrelatedContext(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, "web", "templates", "pages"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "web", "templates", "pages", "analytics.templ"), []byte("package pages\n"), 0644); err != nil {
		t.Fatal(err)
	}

	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{
		Phase:       WorktreeCommitPhaseInitial,
		TaskTitle:   "Improve worker dispatch",
		TurnIntent:  "Fix worker dispatch when queued follow-ups are waiting",
		Summary:     "fix worker dispatch when queued follow-ups are waiting",
		DiffSummary: "render model usage breakdown on analytics page",
	})

	if message != "Render model usage breakdown on analytics page" {
		t.Fatalf("expected semantic diff summary, got: %q", message)
	}
	if strings.Contains(message, "worker") || strings.Contains(message, "follow") {
		t.Fatalf("unrelated execution context should not override diff summary: %q", message)
	}
	if strings.Contains(message, "Execution phase") || strings.Contains(message, "task turn") || strings.Contains(message, "Changed files") || strings.Contains(message, "fix(") || strings.HasPrefix(message, "chore:") || strings.HasPrefix(message, "docs:") || strings.Contains(message, "\n") {
		t.Fatalf("did not expect process metadata, prefix, or file inventory in commit message, got: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_FallsBackToPathSummaryWithoutDiffSummary(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, "internal", "service"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "internal", "service", "worker.go"), []byte("package service\n"), 0644); err != nil {
		t.Fatal(err)
	}

	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{
		Phase:      WorktreeCommitPhaseInitial,
		TaskTitle:  "Improve worker dispatch",
		TurnIntent: "Fix worker dispatch when queued runs are waiting",
		Summary:    "fix worker dispatch when queued runs are waiting",
	})

	if message != "Add worker" {
		t.Fatalf("expected deterministic diff fallback instead of stored context, got: %q", message)
	}
	if strings.Contains(message, "queued") || strings.Contains(message, "Execution phase") || strings.Contains(message, "task turn") || strings.Contains(message, "Changed files") || strings.Contains(message, "fix(") || strings.HasPrefix(message, "chore:") || strings.HasPrefix(message, "docs:") || strings.Contains(message, "\n") {
		t.Fatalf("did not expect stored context, process metadata, prefix, or file inventory in commit message, got: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_LaterExecutionFromDiff(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, "internal", "service"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "internal", "service", "worktree_service_test.go"), []byte("package service\n"), 0644); err != nil {
		t.Fatal(err)
	}

	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{
		Phase:      WorktreeCommitPhaseFollowup,
		TaskTitle:  "Generic title",
		TurnIntent: "Update analytics dashboard copy",
	})

	if message != "Add worktree service tests" {
		t.Fatalf("expected later execution subject from changed test file, got: %q", message)
	}
	if strings.Contains(message, "analytics") || strings.Contains(message, "Execution phase") || strings.Contains(message, "task turn") || strings.Contains(message, "follow-up") || strings.Contains(message, "Followup") || strings.Contains(message, "\n") {
		t.Fatalf("did not expect unrelated context or lifecycle metadata in commit message, got: %q", message)
	}
}

func TestSummarizeWorktreeCommitDiff_UsesActualDiffHunks(t *testing.T) {
	repoDir := createTestGitRepo(t)
	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n\n## Usage\nRun `openvibely serve`.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	svc := NewLLMService(llmConfigRepo, nil, nil, nil, nil, nil)
	mock := &testutil.MockLLMCaller{Response: `{"subject":"docs: document serve command usage"}`}
	svc.SetLLMCaller(mock)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model", Name: "Test Agent"}

	summary := svc.SummarizeWorktreeCommitDiff(context.Background(), repoDir, agent, WorktreeCommitMessageContext{
		TaskTitle:  "Completely unrelated worker change",
		TurnIntent: "Fix worker dispatch",
	})

	if summary != "Document serve command usage" {
		t.Fatalf("expected cleaned LLM diff summary, got %q", summary)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected one LLM call, got %d", mock.CallCount())
	}
	prompt := mock.LastCall().Prompt
	for _, want := range []string{"Use an imperative, capitalized subject", `{"subject":"Add concise description"}`, "Actual diff facts and hunks:", "README.md", "+## Usage", "+Run `openvibely serve`.", "Supporting context, only if it agrees with the diff:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", want, prompt)
		}
	}
	if strings.Contains(summary, "docs:") || strings.Contains(summary, "worker") || strings.Contains(summary, "\n") {
		t.Fatalf("summary should be plain one-line diff summary, got %q", summary)
	}
}

func TestParseWorktreeCommitSummaryOutputRequiresExactJSONShape(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "valid", output: `{"subject":"Centralize channel mutation responses"}`, want: "Centralize channel mutation responses"},
		{name: "cleans conventional prefix", output: `{"subject":"fix: preserve skills viewport"}`, want: "Preserve skills viewport"},
		{name: "plain narration", output: "I'll inspect the worktree diff first.", want: ""},
		{name: "narration before JSON", output: "I'll inspect the worktree diff first.\n{\"subject\":\"Preserve skills viewport\"}", want: "Preserve skills viewport"},
		{name: "markdown fence", output: "```json\n{\"subject\":\"Preserve skills viewport\"}\n```", want: "Preserve skills viewport"},
		{name: "extra field", output: `{"subject":"Preserve skills viewport","notes":"done"}`, want: ""},
		{name: "ambiguous objects", output: `{"subject":"Preserve skills viewport"} {"subject":"Change another thing"}`, want: ""},
		{name: "missing field", output: `{"message":"Preserve skills viewport"}`, want: ""},
		{name: "non-string subject", output: `{"subject":42}`, want: ""},
		{name: "multiline subject", output: `{"subject":"Preserve skills viewport\nChanged files"}`, want: ""},
		{name: "too long", output: `{"subject":"This commit subject is deliberately longer than seventy two characters and must be rejected"}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseWorktreeCommitSummaryOutput(tt.output); got != tt.want {
				t.Fatalf("parseWorktreeCommitSummaryOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeWorktreeCommitDiffRejectsUnstructuredModelOutput(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "app.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db := testutil.NewTestDB(t)
	svc := NewLLMService(repository.NewLLMConfigRepo(db), nil, nil, nil, nil, nil)
	svc.SetLLMCaller(&testutil.MockLLMCaller{Response: "I'll inspect the worktree diff first."})
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model", Name: "Test Agent"}

	summary := svc.SummarizeWorktreeCommitDiff(context.Background(), repoDir, agent, WorktreeCommitMessageContext{})
	if summary != "" {
		t.Fatalf("expected unstructured output to be rejected, got %q", summary)
	}
	if message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{DiffSummary: summary}); message != "Add app" {
		t.Fatalf("expected deterministic fallback, got %q", message)
	}
}

func TestSummarizeWorktreeCommitDiff_DoesNotReadUntrackedSymlinkTargets(t *testing.T) {
	repoDir := createTestGitRepo(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("external secret must not reach prompt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(repoDir, "linked-secret.txt")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	svc := NewLLMService(llmConfigRepo, nil, nil, nil, nil, nil)
	mock := &testutil.MockLLMCaller{Response: `{"subject":"Add safe symlink placeholder"}`}
	svc.SetLLMCaller(mock)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model", Name: "Test Agent"}

	summary := svc.SummarizeWorktreeCommitDiff(context.Background(), repoDir, agent, WorktreeCommitMessageContext{})

	if summary != "Add safe symlink placeholder" {
		t.Fatalf("expected cleaned model summary, got %q", summary)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected one LLM call, got %d", mock.CallCount())
	}
	prompt := mock.LastCall().Prompt
	if !strings.Contains(prompt, "?? linked-secret.txt") {
		t.Fatalf("expected symlink path to remain in status facts, got:\n%s", prompt)
	}
	if strings.Contains(prompt, "external secret must not reach prompt") || strings.Contains(prompt, "Untracked file snippets:") {
		t.Fatalf("untracked symlink target content leaked into prompt:\n%s", prompt)
	}
}

func TestCollectUntrackedFileSnippets_DoesNotReadOutsideResolvedWorktree(t *testing.T) {
	repoDir := createTestGitRepo(t)
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("parent symlink secret must not reach prompt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(repoDir, "linked-dir")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	snippets := collectUntrackedFileSnippets(repoDir, "?? linked-dir/secret.txt")

	if snippets != "" {
		t.Fatalf("expected no snippets for path resolved outside worktree, got:\n%s", snippets)
	}
}

func TestSummarizeWorktreeCommitDiff_ReturnsEmptyWhenModelUnavailable(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Changed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	summary := svc.SummarizeWorktreeCommitDiff(context.Background(), repoDir, models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}, WorktreeCommitMessageContext{})
	if summary != "" {
		t.Fatalf("expected empty summary when model call is unavailable, got %q", summary)
	}
	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{DiffSummary: summary})
	if message != "Update README.md" {
		t.Fatalf("expected deterministic fallback after empty LLM summary, got %q", message)
	}
}

func TestBuildWorktreeCommitMessage_UntrackedNestedFileSummary(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, "web", "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "web", "templates", "analytics.templ"), []byte("package templates\n"), 0644); err != nil {
		t.Fatal(err)
	}

	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{})

	if message != "Add analytics template" {
		t.Fatalf("unexpected subject: %q", strings.Split(message, "\n")[0])
	}
	if strings.Contains(message, "Changed files") || strings.Contains(message, "\n") || strings.HasPrefix(message, "chore:") {
		t.Fatalf("did not expect file inventory body, got: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_SkipsStatusMarkerSummary(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "app.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	message := BuildWorktreeCommitMessage(repoDir, WorktreeCommitMessageContext{
		Summary: "[STATUS: SUCCESS]",
	})

	if message != "Add app" {
		t.Fatalf("unexpected subject: %q", strings.Split(message, "\n")[0])
	}
	if strings.Contains(message, "STATUS") {
		t.Fatalf("status marker leaked into commit message: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_StripsConventionalPrefixFromDiffSummary(t *testing.T) {
	message := BuildWorktreeCommitMessage("", WorktreeCommitMessageContext{
		DiffSummary: "fix(worktree): generate useful commit messages",
	})

	if message != "Generate useful commit messages" {
		t.Fatalf("unexpected subject: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_CapitalizesUnicodeDiffSummary(t *testing.T) {
	message := BuildWorktreeCommitMessage("", WorktreeCommitMessageContext{
		DiffSummary: "überarbeiten analytics usage",
	})

	if message != "Überarbeiten analytics usage" {
		t.Fatalf("unexpected unicode-capitalized subject: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_SkipsLLMBodyAndFileListBoilerplate(t *testing.T) {
	message := BuildWorktreeCommitMessage("", WorktreeCommitMessageContext{
		DiffSummary: "docs: document analytics usage\n\nChanged files:\n- web/templates/pages/analytics.templ\n- internal/handler/analytics_handler.go",
	})

	if message != "Document analytics usage" {
		t.Fatalf("expected capitalized subject without conventional prefix or body, got %q", message)
	}
	if strings.Contains(message, "docs:") || strings.Contains(message, "Changed files") || strings.Contains(message, "analytics.templ") || strings.Contains(message, "\n") {
		t.Fatalf("did not expect conventional prefix, file list, or body in subject: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_EmptyNoSummaryFallback(t *testing.T) {
	message := BuildWorktreeCommitMessage("", WorktreeCommitMessageContext{})
	if message != "Update changes" {
		t.Fatalf("unexpected fallback message: %q", message)
	}
	if strings.Contains(message, "Execution phase") || strings.Contains(message, "task turn") || strings.Contains(message, "follow-up") || strings.Contains(message, "Followup") {
		t.Fatalf("did not expect lifecycle wording in fallback message: %q", message)
	}
}

func TestBuildWorktreeCommitMessage_LaterExecutionNoSummaryFallback(t *testing.T) {
	message := BuildWorktreeCommitMessage("", WorktreeCommitMessageContext{Phase: WorktreeCommitPhaseFollowup})
	if message != "Refine changes" {
		t.Fatalf("unexpected later-execution fallback message: %q", message)
	}
	if strings.Contains(message, "Execution phase") || strings.Contains(message, "task turn") || strings.Contains(message, "follow-up") || strings.Contains(message, "Followup") {
		t.Fatalf("did not expect lifecycle wording in later-execution fallback message: %q", message)
	}
}

func TestCommitWorktreeChanges(t *testing.T) {
	repoDir := createTestGitRepo(t)

	// Empty commit message should fail
	if err := CommitWorktreeChanges(repoDir, "   "); err == nil {
		t.Fatal("expected error for empty commit message")
	}

	// No changes to commit
	if err := CommitWorktreeChanges(repoDir, "no changes"); err != nil {
		t.Errorf("expected nil for no changes, got: %v", err)
	}

	// Make a change and commit
	if err := os.WriteFile(filepath.Join(repoDir, "new_file.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(repoDir, "test commit"); err != nil {
		t.Errorf("CommitWorktreeChanges: %v", err)
	}

	// Verify the commit was made
	cmd := exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "test commit") {
		t.Errorf("expected commit message in log, got: %s", string(out))
	}
}

func TestCommitWorktreeChanges_AutoConfigSetup(t *testing.T) {
	// Create repo WITHOUT git config (simulating VPS scenario)
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Create initial file and commit using explicit config
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	cmd.Run()
	cmd = exec.Command("git", "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "initial")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial commit failed: %v\n%s", err, out)
	}

	// Verify no local user.email is configured in repo
	cmd = exec.Command("git", "config", "--local", "user.email")
	cmd.Dir = dir
	out, _ := cmd.Output()
	hasLocalConfig := len(strings.TrimSpace(string(out))) > 0

	// Make a change and commit - should succeed even without local config
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(dir, "auto config test"); err != nil {
		t.Fatalf("CommitWorktreeChanges failed (should auto-set config if needed): %v", err)
	}

	// If there was no local config before, verify bot config was auto-set
	if !hasLocalConfig {
		cmd = exec.Command("git", "config", "--local", "user.email")
		cmd.Dir = dir
		out, _ := cmd.Output()
		email := strings.TrimSpace(string(out))
		// If local config was set, it should be the bot email
		// If it's empty, that's ok too (means global config was used)
		if email != "" && email != "bot@openvibely.ai" {
			t.Errorf("expected bot@openvibely.ai or empty (using global) in local config, got: %s", email)
		}

		// Verify the effective config (local or global) allowed the commit
		cmd = exec.Command("git", "config", "user.email")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		effectiveEmail := strings.TrimSpace(string(out))
		if effectiveEmail == "" {
			t.Error("expected some user.email configured (local or global)")
		}
	}

	// Verify commit was made
	cmd = exec.Command("git", "log", "--oneline", "-1")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "auto config test") {
		t.Errorf("expected commit in log, got: %s", string(out))
	}
}

func TestCommitWorktreeChanges_AutoConfigSetupMissingName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "initial")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial commit failed: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "config", "--local", "user.email", "partial@example.com")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set local email failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "config", "--local", "--unset", "user.name")
	cmd.Dir = dir
	_ = cmd.Run()

	if err := os.WriteFile(filepath.Join(dir, "partial-config.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(dir, "partial config commit"); err != nil {
		t.Fatalf("CommitWorktreeChanges should set missing user.name independently: %v", err)
	}

	cmd = exec.Command("git", "config", "--local", "user.name")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if name := strings.TrimSpace(string(out)); name != "OpenVibely Bot" {
		t.Fatalf("expected missing user.name to be filled, got %q", name)
	}
}

func TestWorktreeDiff(t *testing.T) {
	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	// Create a new branch and make changes
	cmd := exec.Command("git", "checkout", "-b", "test-branch")
	cmd.Dir = repoDir
	cmd.Run()

	if err := os.WriteFile(filepath.Join(repoDir, "new_file.txt"), []byte("hello from branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "branch commit")
	cmd.Dir = repoDir
	cmd.Run()

	// Switch back to default branch
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = repoDir
	cmd.Run()

	// Get diff
	diff := GetWorktreeDiff(repoDir, "test-branch", defaultBranch)
	if diff == "" {
		t.Error("expected non-empty diff")
	}
	if !strings.Contains(diff, "new_file.txt") {
		t.Error("expected diff to contain new_file.txt")
	}
}

func TestWorktreeFileStats(t *testing.T) {
	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	// Create a new branch with changes
	cmd := exec.Command("git", "checkout", "-b", "stats-branch")
	cmd.Dir = repoDir
	cmd.Run()

	os.WriteFile(filepath.Join(repoDir, "added.txt"), []byte("new file\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Modified\n"), 0644)

	cmd = exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "changes")
	cmd.Dir = repoDir
	cmd.Run()

	// Go back
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = repoDir
	cmd.Run()

	stats := GetWorktreeFileStats(repoDir, "stats-branch", defaultBranch)
	if len(stats) == 0 {
		t.Error("expected file stats")
	}

	// Check for added file
	foundAdded := false
	foundModified := false
	for _, s := range stats {
		if s.Path == "added.txt" && s.Status == "added" {
			foundAdded = true
		}
		if s.Path == "README.md" && s.Status == "modified" {
			foundModified = true
		}
	}
	if !foundAdded {
		t.Error("expected added.txt in stats")
	}
	if !foundModified {
		t.Error("expected README.md modified in stats")
	}
}

func TestGetWorktreeFileStatsWithUncommittedIncludesLiveWorktreeChanges(t *testing.T) {
	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	worktreePath := filepath.Join(t.TempDir(), "live-worktree")

	cmd := exec.Command("git", "branch", "live-stats", defaultBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v output=%s", err, string(out))
	}
	cmd = exec.Command("git", "worktree", "add", worktreePath, "live-stats")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add worktree: %v output=%s", err, string(out))
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# Live\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "untracked.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}

	stats := GetWorktreeFileStatsWithUncommitted(repoDir, "live-stats", defaultBranch, worktreePath)
	foundModified := false
	foundAdded := false
	for _, stat := range stats {
		if stat.Path == "README.md" && stat.Status == "modified" {
			foundModified = true
		}
		if stat.Path == "untracked.txt" && stat.Status == "added" {
			foundAdded = true
		}
	}
	if !foundModified {
		t.Fatalf("expected live README.md modification in stats, got %+v", stats)
	}
	if !foundAdded {
		t.Fatalf("expected live untracked.txt addition in stats, got %+v", stats)
	}
}

func TestWorktreeRepo_UpdateAndClear(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Worktree Update Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Update worktree info
	if err := repo.UpdateWorktreeInfo(ctx, task.ID, "/path/to/worktree", "task/test-branch"); err != nil {
		t.Fatal(err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.WorktreePath != "/path/to/worktree" {
		t.Errorf("expected worktree_path=/path/to/worktree, got %q", got.WorktreePath)
	}
	if got.WorktreeBranch != "task/test-branch" {
		t.Errorf("expected worktree_branch=task/test-branch, got %q", got.WorktreeBranch)
	}

	// Update merge status
	if err := repo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, task.ID)
	if got.MergeStatus != models.MergeStatusPending {
		t.Errorf("expected merge_status=pending, got %q", got.MergeStatus)
	}

	// Update auto-merge
	if err := repo.UpdateAutoMerge(ctx, task.ID, true, "main"); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, task.ID)
	if !got.AutoMerge {
		t.Error("expected auto_merge=true")
	}
	if got.MergeTargetBranch != "main" {
		t.Errorf("expected merge_target_branch=main, got %q", got.MergeTargetBranch)
	}

	// Clear worktree info
	if err := repo.ClearWorktreeInfo(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, task.ID)
	if got.WorktreePath != "" {
		t.Errorf("expected empty worktree_path after clear, got %q", got.WorktreePath)
	}
	if got.WorktreeBranch != "" {
		t.Errorf("expected empty worktree_branch after clear, got %q", got.WorktreeBranch)
	}
}

func TestMergeBranch(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Merge Test Task",
		Category:          models.CategoryActive,
		Priority:          1,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Setup worktree
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	// Make changes in worktree
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("new feature\n"), 0644)
	CommitWorktreeChanges(wtPath, "add feature")

	// Merge
	result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
	if err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if !result.Success {
		t.Errorf("expected merge success, got error: %s", result.ErrorMessage)
	}
	if result.MergeCommit == "" {
		t.Error("expected merge commit hash")
	}

	// Verify merge status in DB
	dbTask, _ := taskRepo.GetByID(ctx, task.ID)
	if dbTask.MergeStatus != models.MergeStatusMerged {
		t.Errorf("expected merge_status=merged, got %q", dbTask.MergeStatus)
	}

	// Verify the file exists on the target branch
	if _, err := os.Stat(filepath.Join(repoDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("expected feature.txt to exist on target branch after merge")
	}
}

func TestMergeBranch_ReturnsErrorWhenAutoCommitFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Auto Commit Failure",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
		WorktreePath:      t.TempDir(),
		WorktreeBranch:    "task/auto-commit-fail",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(task.WorktreePath, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
	if err == nil {
		t.Fatal("expected merge error when auto-commit fails")
	}
	if result == nil || result.ErrorMessage == "" {
		t.Fatalf("expected merge result with error message, got %#v", result)
	}
	if !strings.Contains(result.ErrorMessage, "checking git status") && !strings.Contains(result.ErrorMessage, "staging changes") && !strings.Contains(result.ErrorMessage, "committing changes") {
		t.Fatalf("expected commit failure details, got %q", result.ErrorMessage)
	}

	dbTask, dbErr := taskRepo.GetByID(ctx, task.ID)
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	if dbTask.MergeStatus != models.MergeStatusFailed {
		t.Fatalf("expected merge status failed, got %q", dbTask.MergeStatus)
	}
}

func TestMergeBranch_UnrelatedDirtyTargetFileCanMerge(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Dirty Target",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	if err := os.WriteFile(filepath.Join(wtPath, "task_change.txt"), []byte("task change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task change"); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(repoDir, "local_dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("do not touch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
	if err != nil {
		t.Fatalf("expected non-overlapping dirty target file to be allowed, got %v result=%#v", err, result)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful merge, got %#v", result)
	}
	if got, readErr := os.ReadFile(dirtyPath); readErr != nil || string(got) != "do not touch\n" {
		t.Fatalf("expected dirty target file to remain untouched, got %q err=%v", got, readErr)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status=merged, got %q", updated.MergeStatus)
	}
}

func TestMergeBranch_OverlappingDirtyTargetFileFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Overlapping Dirty Target",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	sharedPath := filepath.Join(wtPath, "README.md")
	if err := os.WriteFile(sharedPath, []byte("# Task branch change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task changes README"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Local dirty change\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "merge")
	if err == nil {
		t.Fatal("expected Git to reject merge that would overwrite local dirty changes")
	}
	if result == nil || !strings.Contains(strings.ToLower(result.ErrorMessage), "would be overwritten") {
		t.Fatalf("expected overwrite warning from git, got result=%#v err=%v", result, err)
	}
	if got, readErr := os.ReadFile(filepath.Join(repoDir, "README.md")); readErr != nil || string(got) != "# Local dirty change\n" {
		t.Fatalf("expected local dirty file to remain untouched, got %q err=%v", got, readErr)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusFailed {
		t.Fatalf("expected merge_status=failed, got %q", updated.MergeStatus)
	}
}

func TestRebaseBranch_RebasesOntoTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Rebase Task", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	if err := os.WriteFile(filepath.Join(wtPath, "task.txt"), []byte("task\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task commit"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "main.txt"), []byte("main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "main.txt")
	runGitTest(t, repoDir, "commit", "-m", "main commit")

	if !IsBranchBehindTarget(repoDir, branchName, defaultBranch) {
		t.Fatal("expected task branch to be behind target before rebase")
	}
	if !IsBranchDivergedFromTarget(repoDir, branchName, defaultBranch) {
		t.Fatal("expected task and target branches to be diverged before rebase")
	}
	result, err := ws.RebaseBranch(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("RebaseBranch: %v", err)
	}
	if result == nil || !result.Success || result.UpToDate {
		t.Fatalf("expected successful non-noop rebase, got %#v", result)
	}
	if IsBranchBehindTarget(repoDir, branchName, defaultBranch) {
		t.Fatal("expected task branch to include target after rebase")
	}
	if IsBranchDivergedFromTarget(repoDir, branchName, defaultBranch) {
		t.Fatal("expected task and target branches not to be diverged after rebase")
	}
	if out := runGitTest(t, wtPath, "log", "--oneline", defaultBranch+"..HEAD"); !strings.Contains(out, "task commit") {
		t.Fatalf("expected rebased task commit above target, got %q", out)
	}
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected merge status pending after rebase, got %q", updated.MergeStatus)
	}
}

func TestRebaseBranch_AlreadyUpToDate(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Rebase Up To Date", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	before := gitRevParseTest(t, wtPath, "HEAD")

	result, err := ws.RebaseBranch(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("RebaseBranch already up to date: %v", err)
	}
	if result == nil || !result.Success || !result.UpToDate {
		t.Fatalf("expected up-to-date success, got %#v", result)
	}
	if result.RebasedHead != before {
		t.Fatalf("expected HEAD unchanged, got before=%s result=%s", before, result.RebasedHead)
	}
}

func TestRebaseBranch_ConflictAborts(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	if err := os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "shared.txt")
	runGitTest(t, repoDir, "commit", "-m", "add shared")

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	task := &models.Task{ProjectID: "default", Title: "Rebase Conflict", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "shared.txt"), []byte("task change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task changes shared"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("target change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "shared.txt")
	runGitTest(t, repoDir, "commit", "-m", "target changes shared")

	result, err := ws.RebaseBranch(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("expected conflict result without hard error, got %v", err)
	}
	if result == nil || result.Success || len(result.ConflictFiles) == 0 {
		t.Fatalf("expected rebase conflict result, got %#v", result)
	}
	if !strings.Contains(strings.ToLower(result.ErrorMessage), "conflicts") || !strings.Contains(strings.ToLower(result.ErrorMessage), "aborted") {
		t.Fatalf("expected conflict-aborted guidance, got %q", result.ErrorMessage)
	}
	statusOut := runGitTest(t, wtPath, "status", "--porcelain")
	if strings.Contains(statusOut, "UU ") {
		t.Fatalf("expected rebase conflict to be aborted, got status %q", statusOut)
	}
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected pending status after aborted rebase conflict, got %q", updated.MergeStatus)
	}
}

func TestMergeBranch_FastForward(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "FF Merge Test",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	os.WriteFile(filepath.Join(wtPath, "ff_feature.txt"), []byte("fast forward\n"), 0644)
	CommitWorktreeChanges(wtPath, "add ff feature")

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch ff: %v", err)
	}
	if !result.Success {
		t.Errorf("expected ff merge success: %s", result.ErrorMessage)
	}
	if !IsBranchTipMergedInto(repoDir, branchName, defaultBranch) {
		t.Fatalf("expected task branch tip to be merged into %s after app fast-forward merge", defaultBranch)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "ff_feature.txt")); err != nil {
		t.Fatalf("expected checked-out target worktree files to be refreshed after ff merge: %v", err)
	}
	if staged := runGitTest(t, repoDir, "diff", "--name-only", "--cached"); staged != "" {
		t.Fatalf("expected no staged changes left in checked-out target worktree after ff merge, got %q", staged)
	}
	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task after ff merge: %v", err)
	}
	if dbTask.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge status merged after app fast-forward merge, got %q", dbTask.MergeStatus)
	}
}

func TestMergeBranch_FastForward_SkipsAutoRebaseWhenAlreadyFastForwardable(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Already FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "already_ff.txt"), []byte("already fast-forwardable\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "add already fast-forwardable change"); err != nil {
		t.Fatal(err)
	}
	expectedHead := gitRevParseTest(t, wtPath, "HEAD")
	if out, err := gitOutput(wtPath, "merge-base", "--is-ancestor", defaultBranch, "HEAD"); err != nil {
		t.Fatalf("test setup expected %s to already be an ancestor of task HEAD: %v\n%s", defaultBranch, err, out)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\n" +
		"if [ \"$1\" = \"rebase\" ]; then\n" +
		"  echo unexpected rebase >&2\n" +
		"  exit 42\n" +
		"fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch ff should skip unnecessary rebase: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected fast-forward merge success without rebase: %s", result.ErrorMessage)
	}
	if got := gitRevParseTest(t, repoDir, "refs/heads/"+defaultBranch); got != expectedHead {
		t.Fatalf("expected target branch to advance to task head %s, got %s", expectedHead, got)
	}
}

func TestMergeBranch_FastForward_SequentialMergesAutoRebaseSecondTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	taskA := &models.Task{
		ProjectID:         "default",
		Title:             "Sequential Merge Task A",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, taskA); err != nil {
		t.Fatal(err)
	}
	wtPathA, branchA, err := ws.SetupWorktree(ctx, taskA, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	taskA.WorktreePath = wtPathA
	taskA.WorktreeBranch = branchA
	if err := os.WriteFile(filepath.Join(wtPathA, "task_a.txt"), []byte("task a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPathA, "task a commit"); err != nil {
		t.Fatal(err)
	}

	taskB := &models.Task{
		ProjectID:         "default",
		Title:             "Sequential Merge Task B",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, taskB); err != nil {
		t.Fatal(err)
	}
	wtPathB, branchB, err := ws.SetupWorktree(ctx, taskB, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	taskB.WorktreePath = wtPathB
	taskB.WorktreeBranch = branchB
	if err := os.WriteFile(filepath.Join(wtPathB, "task_b.txt"), []byte("task b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPathB, "task b commit"); err != nil {
		t.Fatal(err)
	}

	resultA, err := ws.MergeBranch(ctx, taskA, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch task A ff: %v", err)
	}
	if !resultA.Success {
		t.Fatalf("expected task A ff merge success: %s", resultA.ErrorMessage)
	}

	resultB, err := ws.MergeBranch(ctx, taskB, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch task B ff: %v", err)
	}
	if !resultB.Success {
		t.Fatalf("expected task B ff merge success after auto-rebase, got: %s", resultB.ErrorMessage)
	}

	if _, err := os.Stat(filepath.Join(repoDir, "task_a.txt")); os.IsNotExist(err) {
		t.Fatal("expected task_a.txt to exist after sequential merge")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "task_b.txt")); os.IsNotExist(err) {
		t.Fatal("expected task_b.txt to exist after sequential merge")
	}
	mainHead := gitRevParseTest(t, repoDir, "refs/heads/"+defaultBranch)
	taskHead := gitRevParseTest(t, wtPathB, "HEAD")
	if mainHead != taskHead {
		t.Fatalf("expected target branch to advance to rebased task worktree HEAD, got target=%s task=%s", mainHead, taskHead)
	}
}

func TestMergeBranch_FastForward_SequentialMergesRebaseConflictPreserved(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	sharedFile := filepath.Join(repoDir, "shared.txt")
	if err := os.WriteFile(sharedFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "shared.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add shared file: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "add shared file")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit shared file: %v\n%s", err, out)
	}

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	taskA := &models.Task{
		ProjectID:         "default",
		Title:             "Sequential Conflict Task A",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, taskA); err != nil {
		t.Fatal(err)
	}
	wtPathA, branchA, err := ws.SetupWorktree(ctx, taskA, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	taskA.WorktreePath = wtPathA
	taskA.WorktreeBranch = branchA
	if err := os.WriteFile(filepath.Join(wtPathA, "shared.txt"), []byte("task-a-change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPathA, "task a shared update"); err != nil {
		t.Fatal(err)
	}

	taskB := &models.Task{
		ProjectID:         "default",
		Title:             "Sequential Conflict Task B",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, taskB); err != nil {
		t.Fatal(err)
	}
	wtPathB, branchB, err := ws.SetupWorktree(ctx, taskB, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	taskB.WorktreePath = wtPathB
	taskB.WorktreeBranch = branchB
	if err := os.WriteFile(filepath.Join(wtPathB, "shared.txt"), []byte("task-b-change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPathB, "task b shared update"); err != nil {
		t.Fatal(err)
	}

	resultA, err := ws.MergeBranch(ctx, taskA, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch task A ff: %v", err)
	}
	if !resultA.Success {
		t.Fatalf("expected task A ff merge success: %s", resultA.ErrorMessage)
	}

	resultB, err := ws.MergeBranch(ctx, taskB, repoDir, "ff")
	if err != nil {
		t.Fatalf("expected conflict result without hard error, got: %v", err)
	}
	if resultB.Success {
		t.Fatalf("expected task B ff merge conflict after auto-rebase attempt")
	}
	if len(resultB.ConflictFiles) == 0 {
		t.Fatalf("expected conflict files for task B rebase conflict")
	}
	if !strings.Contains(strings.ToLower(resultB.ErrorMessage), "auto-rebase") {
		t.Fatalf("expected conflict message to mention auto-rebase, got %q", resultB.ErrorMessage)
	}

	updatedTaskB, err := taskRepo.GetByID(ctx, taskB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTaskB.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected merge status conflict for task B, got %q", updatedTaskB.MergeStatus)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wtPathB
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status in task B worktree: %v", err)
	}
	if strings.Contains(string(statusOut), "UU ") {
		t.Fatalf("expected task B worktree conflicts to be aborted after failed auto-rebase, got: %s", string(statusOut))
	}
}

func TestMergeBranch_FastForward_DirtyWorktreeRejected(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Dirty FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err == nil {
		t.Fatal("expected dirty task worktree to reject fast-forward merge")
	}
	if result == nil || !strings.Contains(result.ErrorMessage, "uncommitted changes") {
		t.Fatalf("expected dirty worktree message, got result=%#v err=%v", result, err)
	}
	if gitRevParseTest(t, repoDir, "refs/heads/"+defaultBranch) != gitRevParseTest(t, repoDir, "HEAD") {
		t.Fatal("expected target branch to remain unchanged after dirty worktree rejection")
	}
}

func TestMergeBranch_FastForward_WrongTaskWorktreeBranchRejected(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Wrong Branch FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	runGitTest(t, wtPath, "checkout", "-b", "task/wrong-branch")

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err == nil {
		t.Fatal("expected wrong task worktree branch to reject fast-forward merge")
	}
	if result == nil || !strings.Contains(result.ErrorMessage, "expected") {
		t.Fatalf("expected wrong branch message, got result=%#v err=%v", result, err)
	}
}

func TestMergeBranch_FastForward_CheckedOutTargetVerifiesMergedHead(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Wrong Merge Head FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "expected_task.txt"), []byte("expected\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "expected task change"); err != nil {
		t.Fatal(err)
	}
	expectedTaskHead := gitRevParseTest(t, wtPath, "HEAD")

	runGitTest(t, repoDir, "checkout", "-b", "wrong-merge-head", defaultBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "wrong.txt"), []byte("wrong\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "wrong.txt")
	runGitTest(t, repoDir, "commit", "-m", "wrong merge head")
	wrongHead := gitRevParseTest(t, repoDir, "HEAD")
	runGitTest(t, repoDir, "checkout", defaultBranch)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\n" +
		"if [ \"$1\" = \"merge\" ] && [ \"$2\" = \"--ff-only\" ] && [ \"$3\" = \"refs/heads/" + task.WorktreeBranch + "\" ]; then\n" +
		"  exec \"" + realGit + "\" reset --hard " + wrongHead + "\n" +
		"fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err == nil {
		t.Fatal("expected checked-out target merge with wrong HEAD to fail")
	}
	if result == nil || result.Success {
		t.Fatalf("expected failed wrong-head result, got %#v", result)
	}
	if !strings.Contains(result.ErrorMessage, "expected rebased task HEAD") {
		t.Fatalf("expected wrong-head verification message, got %q", result.ErrorMessage)
	}
	if got := gitRevParseTest(t, repoDir, "HEAD"); got != wrongHead {
		t.Fatalf("expected wrapper to leave target worktree at wrong head %s, got %s", wrongHead, got)
	}
	if expectedTaskHead == wrongHead {
		t.Fatal("test setup invalid: expected task head and wrong head should differ")
	}
}

func TestMergeBranch_FastForward_UncheckedOutTargetUsesUpdateRef(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Update Ref FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "update_ref.txt"), []byte("update-ref\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "add update-ref feature"); err != nil {
		t.Fatal(err)
	}

	runGitTest(t, repoDir, "checkout", "-b", "parking")
	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err != nil {
		t.Fatalf("MergeBranch ff update-ref fallback: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected update-ref fallback success: %s", result.ErrorMessage)
	}
	if gitRevParseTest(t, repoDir, "refs/heads/"+defaultBranch) != gitRevParseTest(t, wtPath, "HEAD") {
		t.Fatal("expected unchecked-out target branch ref to advance to task worktree HEAD")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "update_ref.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected checked-out parking worktree files not to be refreshed by update-ref fallback, err=%v", err)
	}
}

func TestMergeBranch_FastForward_StaleMainUpdateRefRejected(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{ProjectID: "default", Title: "Stale Ref FF", Category: models.CategoryActive, Status: models.StatusPending, MergeTargetBranch: defaultBranch}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := os.WriteFile(filepath.Join(wtPath, "stale_task.txt"), []byte("task\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task stale ref change"); err != nil {
		t.Fatal(err)
	}

	externalCommitBranch := "external-main-advance"
	runGitTest(t, repoDir, "checkout", "-b", externalCommitBranch, defaultBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "external.txt"), []byte("external\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "external.txt")
	runGitTest(t, repoDir, "commit", "-m", "external main advance")
	externalCommit := gitRevParseTest(t, repoDir, "HEAD")
	runGitTest(t, repoDir, "checkout", "-b", "parking")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapperDir := t.TempDir()
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := "#!/bin/sh\n" +
		"if [ \"$1\" = \"update-ref\" ] && [ \"$2\" = \"refs/heads/" + defaultBranch + "\" ]; then\n" +
		"  \"" + realGit + "\" -C \"" + repoDir + "\" update-ref refs/heads/" + defaultBranch + " " + externalCommit + "\n" +
		"fi\n" +
		"exec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := ws.MergeBranch(ctx, task, repoDir, "ff")
	if err == nil {
		t.Fatal("expected stale target update-ref to fail")
	}
	if result == nil || result.Success {
		t.Fatalf("expected failed stale-ref result, got %#v", result)
	}
	if got := gitRevParseTest(t, repoDir, "refs/heads/"+defaultBranch); got != externalCommit {
		t.Fatalf("expected stale update to preserve externally advanced target ref %s, got %s", externalCommit, got)
	}
}

func TestMergeBranch_FastForward_DetachedTargetCommitUsesUpdateRefFallback(t *testing.T) {
	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	head := gitRevParseTest(t, repoDir, "HEAD")
	runGitTest(t, repoDir, "checkout", "--detach", head)

	worktreePath, err := findWorktreeForBranch(repoDir, defaultBranch)
	if err != nil {
		t.Fatal(err)
	}
	if worktreePath != "" {
		t.Fatalf("expected detached worktree at target commit not to count as checked-out %s, got %q", defaultBranch, worktreePath)
	}
}

func TestMergeBranch_SquashAllowsUnrelatedStagedUserChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Squash With User Staged File",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	if err := os.WriteFile(filepath.Join(wtPath, "task_squash.txt"), []byte("task squash\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "task squash change"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "user_staged.txt"), []byte("user staged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stageCmd := exec.Command("git", "add", "user_staged.txt")
	stageCmd.Dir = repoDir
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("stage user file: %v\n%s", err, out)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "squash")
	if err != nil {
		t.Fatalf("expected squash merge to allow unrelated staged user file, got %v result=%#v", err, result)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful squash merge, got %#v", result)
	}

	showCmd := exec.Command("git", "show", "--name-only", "--pretty=format:", "HEAD")
	showCmd.Dir = repoDir
	showOut, err := showCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	changedInCommit := string(showOut)
	if !strings.Contains(changedInCommit, "task_squash.txt") {
		t.Fatalf("expected squash commit to contain task file, got %q", changedInCommit)
	}
	if strings.Contains(changedInCommit, "user_staged.txt") {
		t.Fatalf("expected squash commit not to include unrelated staged user file, got %q", changedInCommit)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoDir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(statusOut), "A  user_staged.txt") {
		t.Fatalf("expected unrelated user file to remain staged, status=%q", statusOut)
	}
}

func TestMergeBranch_SquashCommitFailureMarksMergeFailedAndDoesNotUseHardReset(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Squash Commit Failure",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	if err := os.WriteFile(filepath.Join(wtPath, "squash_hook_failure.txt"), []byte("squash commit should fail\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "commit before failing squash hook"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "user_staged_survives_failure.txt"), []byte("user staged survives failure\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stageCmd := exec.Command("git", "add", "user_staged_survives_failure.txt")
	stageCmd.Dir = repoDir
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("stage user file: %v\n%s", err, out)
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho blocked squash commit >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	result, err := ws.MergeBranch(ctx, task, repoDir, "squash")
	if err == nil {
		t.Fatal("expected squash commit failure")
	}
	if result == nil || !strings.Contains(result.ErrorMessage, "blocked squash commit") {
		t.Fatalf("expected squash commit failure details, got %#v", result)
	}

	dbTask, dbErr := taskRepo.GetByID(ctx, task.ID)
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	if dbTask.MergeStatus != models.MergeStatusFailed {
		t.Fatalf("expected merge_status=failed after squash commit failure, got %q", dbTask.MergeStatus)
	}

	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoDir
	statusOut, statusErr := statusCmd.Output()
	if statusErr != nil {
		t.Fatalf("git status after failed squash commit: %v", statusErr)
	}
	status := string(statusOut)
	if !strings.Contains(status, "A  user_staged_survives_failure.txt") {
		t.Fatalf("expected pre-existing staged user file to survive failed squash cleanup, status=%q", statusOut)
	}
	if strings.Contains(status, "squash_hook_failure.txt") {
		t.Fatalf("expected failed squash cleanup to remove squash-produced changes, status=%q", statusOut)
	}

	serviceSource, readErr := os.ReadFile("worktree_service.go")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(serviceSource), "reset\", \"--hard") {
		t.Fatal("merge cleanup must not use git reset --hard")
	}
}

func TestResolveConflictsWithAI_NoActiveConflictsClearsStaleConflictStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	ws.SetLLMService(&LLMService{})

	task := &models.Task{
		ProjectID:   "default",
		Title:       "Stale Conflict",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		MergeStatus: models.MergeStatusConflict,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		t.Fatal(err)
	}

	result, err := ws.ResolveConflictsWithAI(ctx, task, repoDir)
	if err == nil {
		t.Fatal("expected stale conflict resolution to fail when no active conflicts exist")
	}
	if result == nil || !strings.Contains(result.ErrorMessage, "no active merge conflicts") {
		t.Fatalf("expected no-conflicts error message, got %#v", result)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected stale conflict status to reset to pending, got %q", updated.MergeStatus)
	}
}

func TestMergeBranch_Squash(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Squash Merge Test",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	// Multiple commits
	os.WriteFile(filepath.Join(wtPath, "file1.txt"), []byte("file1\n"), 0644)
	CommitWorktreeChanges(wtPath, "commit 1")
	os.WriteFile(filepath.Join(wtPath, "file2.txt"), []byte("file2\n"), 0644)
	CommitWorktreeChanges(wtPath, "commit 2")

	result, err := ws.MergeBranch(ctx, task, repoDir, "squash")
	if err != nil {
		t.Fatalf("MergeBranch squash: %v", err)
	}
	if !result.Success {
		t.Errorf("expected squash merge success: %s", result.ErrorMessage)
	}
}

func TestCleanupWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Cleanup Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, branchName, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName

	// Cleanup
	if err := ws.CleanupWorktree(ctx, task, repoDir, true); err != nil {
		t.Fatalf("CleanupWorktree: %v", err)
	}

	// Verify directory removed
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("expected worktree directory to be removed")
	}

	// Verify branch deleted
	checkCmd := exec.Command("git", "rev-parse", "--verify", branchName)
	checkCmd.Dir = repoDir
	if checkCmd.Run() == nil {
		t.Error("expected branch to be deleted")
	}

	// Verify task DB cleared
	dbTask, _ := taskRepo.GetByID(ctx, task.ID)
	if dbTask.WorktreePath != "" {
		t.Error("expected worktree_path to be cleared in DB")
	}
	if dbTask.WorktreeBranch != "" {
		t.Error("expected worktree_branch to be cleared in DB")
	}
}

func TestCreateTaskWithAutoMerge(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Auto Merge Task",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		AutoMerge:         true,
		MergeTargetBranch: "develop",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if !got.AutoMerge {
		t.Error("expected auto_merge=true")
	}
	if got.MergeTargetBranch != "develop" {
		t.Errorf("expected merge_target_branch=develop, got %q", got.MergeTargetBranch)
	}
}

func TestIsBranchMerged(t *testing.T) {
	repoDir := createTestGitRepo(t)
	ctx := context.Background()
	_ = ctx

	// Create a test branch
	branchName := "feature/test"
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating branch failed: %v", err)
	}

	// Make a change and commit
	testFile := filepath.Join(repoDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = repoDir
	cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "test commit")
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	// Branch should NOT be merged yet
	mainBranch := GetDefaultBranch(repoDir)
	if IsBranchMerged(repoDir, branchName, mainBranch) {
		t.Error("branch should not be merged before merging")
	}

	// Checkout main and merge
	cmd = exec.Command("git", "checkout", mainBranch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("checkout main failed: %v", err)
	}
	cmd = exec.Command("git", "merge", "--no-ff", "-m", "merge test", branchName)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	// Branch should be merged now
	if !IsBranchMerged(repoDir, branchName, mainBranch) {
		t.Error("branch should be merged after merging")
	}

	// Non-existent branch should be considered merged (cleanup edge case)
	if !IsBranchMerged(repoDir, "non-existent-branch", mainBranch) {
		t.Error("non-existent branch should be considered merged")
	}
}

func TestCleanupMergedWorktrees(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Create a test git repo
	repoDir := createTestGitRepo(t)
	mainBranch := GetDefaultBranch(repoDir)

	// Create project with repo path
	project := &models.Project{
		Name:     "Test Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Set cleanup policy to "after_merge"
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatal(err)
	}

	// Create worktree service
	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	// Create a task with worktree
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Test Task",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		MergeTargetBranch: mainBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Setup worktree
	wtPath, wtBranch, err := ws.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree failed: %v", err)
	}

	// Make a change in the worktree
	testFile := filepath.Join(wtPath, "worktree_test.txt")
	if err := os.WriteFile(testFile, []byte("test from worktree\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Commit the change
	if err := CommitWorktreeChanges(wtPath, "test commit from worktree"); err != nil {
		t.Fatalf("CommitWorktreeChanges failed: %v", err)
	}

	// Merge the worktree branch to main manually (simulating manual merge)
	cmd := exec.Command("git", "checkout", mainBranch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("checkout main failed: %v", err)
	}
	cmd = exec.Command("git", "merge", "--no-ff", "-m", "manual merge", wtBranch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("manual merge failed: %v", err)
	}

	// Verify the branch is merged
	if !IsBranchMerged(repoDir, wtBranch, mainBranch) {
		t.Error("branch should be merged")
	}

	// Mark task as completed (cleanup skips running/pending tasks)
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	// Run the cleanup scan
	if err := ws.CleanupMergedWorktrees(ctx); err != nil {
		t.Errorf("CleanupMergedWorktrees failed: %v", err)
	}

	// Verify the worktree was cleaned up
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("worktree directory should be removed")
	}

	// Verify the branch was deleted
	cmd = exec.Command("git", "rev-parse", "--verify", wtBranch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err == nil {
		t.Error("branch should be deleted")
	}

	// Verify task DB was updated
	dbTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbTask.WorktreePath != "" {
		t.Error("expected worktree_path to be cleared in DB")
	}
	if dbTask.WorktreeBranch != "" {
		t.Error("expected worktree_branch to be cleared in DB")
	}
	if dbTask.MergeStatus != models.MergeStatusMerged {
		t.Errorf("expected merge_status=merged, got %q", dbTask.MergeStatus)
	}
}

func TestCleanupMergedWorktrees_EmptyMergeTargetSkipsBranchDeletionWithNonTerminalDescendant(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	mainBranch := GetDefaultBranch(repoDir)

	project := &models.Project{
		Name:     "Cleanup Descendant Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatal(err)
	}

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)
	parent := &models.Task{
		ProjectID: project.ID,
		Title:     "Merged Parent With Active Child",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}

	wtPath, wtBranch, err := ws.SetupWorktree(ctx, parent, repoDir)
	if err != nil {
		t.Fatalf("SetupWorktree failed: %v", err)
	}
	if err := taskRepo.UpdateAutoMerge(ctx, parent.ID, false, ""); err != nil {
		t.Fatalf("clear merge target branch fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "descendant_guard.txt"), []byte("descendant guard\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "descendant guard worktree commit"); err != nil {
		t.Fatalf("CommitWorktreeChanges failed: %v", err)
	}

	cmd := exec.Command("git", "checkout", mainBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout main failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "merge", "--no-ff", "-m", "manual merge descendant guard", wtBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("manual merge failed: %v\n%s", err, out)
	}

	if err := taskRepo.UpdateStatus(ctx, parent.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	child := &models.Task{
		ProjectID:    project.ID,
		Title:        "Active Child Blocks Branch Deletion",
		Category:     models.CategoryActive,
		Status:       models.StatusPending,
		ParentTaskID: &parent.ID,
		Prompt:       "child remains active",
	}
	if err := taskRepo.Create(ctx, child); err != nil {
		t.Fatal(err)
	}

	if err := ws.CleanupMergedWorktrees(ctx); err != nil {
		t.Fatalf("CleanupMergedWorktrees failed: %v", err)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("worktree directory should be removed")
	}
	cmd = exec.Command("git", "rev-parse", "--verify", wtBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("branch should remain because a descendant is non-terminal: %v\n%s", err, out)
	}
	dbTask, err := taskRepo.GetByID(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbTask.MergeTargetBranch != "" {
		t.Fatalf("expected empty merge_target_branch fixture, got %q", dbTask.MergeTargetBranch)
	}
	if dbTask.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status=merged, got %q", dbTask.MergeStatus)
	}
	if dbTask.WorktreePath != "" || dbTask.WorktreeBranch != "" {
		t.Fatalf("expected worktree metadata cleared, got path=%q branch=%q", dbTask.WorktreePath, dbTask.WorktreeBranch)
	}
}

func TestCleanupMergedWorktrees_KeepPolicy(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	// Set cleanup policy to "keep"
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "keep"); err != nil {
		t.Fatal(err)
	}

	ws := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	// Should return immediately with "keep" policy
	if err := ws.CleanupMergedWorktrees(ctx); err != nil {
		t.Errorf("CleanupMergedWorktrees should not error with keep policy: %v", err)
	}
}

func TestCleanupOrphanedWorktrees(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	// Create a test project with a git repo
	tempDir := t.TempDir()
	repoDir := filepath.Join(tempDir, "test-repo")
	exec.Command("git", "init", "-b", "main", repoDir).Run()
	exec.Command("git", "-C", repoDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", repoDir, "config", "user.name", "Test User").Run()

	// Create initial commit
	testFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(testFile, []byte("# Test"), 0644)
	exec.Command("git", "-C", repoDir, "add", ".").Run()
	exec.Command("git", "-C", repoDir, "commit", "-m", "initial").Run()

	// Set cleanup policy to after_merge
	settingsRepo.Set(ctx, "worktree_cleanup", "after_merge")

	project := &models.Project{
		ID:       "test-project",
		Name:     "Test Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task and its worktree
	task := &models.Task{
		ID:        "test-task",
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Create worktree for the task
	worktreeDir, branch, err := worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatal("worktree directory was not created")
	}

	// Verify branch exists
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatal("worktree branch was not created")
	}

	// Now delete the task from the database (simulating orphaned worktree)
	if err := taskRepo.Delete(ctx, task.ID); err != nil {
		t.Fatalf("failed to delete task: %v", err)
	}

	// Verify worktree still exists on disk
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatal("worktree directory should still exist after task deletion")
	}

	// Run orphaned worktree cleanup
	cleanedCount, err := worktreeSvc.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees failed: %v", err)
	}

	if cleanedCount != 1 {
		t.Errorf("expected 1 orphaned worktree to be cleaned, got %d", cleanedCount)
	}

	// Verify worktree directory was removed
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Error("expected orphaned worktree directory to be removed")
	}

	// Verify branch was deleted
	cmd = exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err == nil {
		t.Error("expected orphaned branch to be deleted")
	}
}

func TestCleanupOrphanedWorktrees_SkipsWhenTaskStillExists(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	repoDir := createTestGitRepo(t)
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatalf("failed to set cleanup policy: %v", err)
	}

	project := &models.Project{
		ID:       "project-skip-existing-task",
		Name:     "Skip Existing Task",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ID:        "existing-task-id",
		ProjectID: project.ID,
		Title:     "Existing Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	worktreeDir, branch, err := worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Simulate stale metadata: task exists but worktree fields are empty in DB.
	if err := taskRepo.ClearWorktreeInfo(ctx, task.ID); err != nil {
		t.Fatalf("failed to clear worktree info: %v", err)
	}

	cleanedCount, err := worktreeSvc.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees failed: %v", err)
	}
	if cleanedCount != 0 {
		t.Fatalf("expected 0 orphaned worktrees cleaned, got %d", cleanedCount)
	}

	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatalf("worktree should not be cleaned while task %s still exists", task.ID)
	}

	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("worktree branch should still exist: %v", err)
	}
}

func TestCleanupOrphanedWorktrees_SkipsFollowupWhenTaskStillExistsWithStaleMetadata(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	repoDir := createTestGitRepo(t)
	defaultBranch := GetCurrentBranch(repoDir)
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatalf("failed to set cleanup policy: %v", err)
	}

	project := &models.Project{
		ID:       "project-followup-existing-task",
		Name:     "Followup Existing Task",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ID:                "followup-existing-task-id",
		ProjectID:         project.ID,
		Title:             "Existing Followup Task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeStatus:       models.MergeStatusMerged,
		MergeTargetBranch: defaultBranch,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	worktreeDir, branch, skip, err := worktreeSvc.SetupFollowupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("failed to create follow-up worktree: %v", err)
	}
	if !skip {
		t.Fatal("expected fresh follow-up worktree")
	}
	if !strings.Contains(filepath.Base(worktreeDir), "_followup_") || !strings.Contains(branch, "-followup-") {
		t.Fatalf("expected actual follow-up naming, got path=%s branch=%s", worktreeDir, branch)
	}

	if err := os.WriteFile(filepath.Join(worktreeDir, "followup.txt"), []byte("follow-up work\n"), 0644); err != nil {
		t.Fatalf("write follow-up file: %v", err)
	}
	if err := CommitWorktreeChanges(worktreeDir, "follow-up commit"); err != nil {
		t.Fatalf("commit follow-up changes: %v", err)
	}
	followupCommit := gitRevParseTest(t, worktreeDir, "HEAD")

	// Simulate stale metadata: the task still exists but no longer records the
	// active follow-up worktree path/branch.
	if err := taskRepo.ClearWorktreeInfo(ctx, task.ID); err != nil {
		t.Fatalf("failed to clear worktree info: %v", err)
	}

	cleanedCount, err := worktreeSvc.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees failed: %v", err)
	}
	if cleanedCount != 0 {
		t.Fatalf("expected 0 orphaned worktrees cleaned, got %d", cleanedCount)
	}
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatal("follow-up worktree should not be cleaned while task still exists")
	}
	if got := gitRevParseTest(t, repoDir, branch); got != followupCommit {
		t.Fatalf("follow-up branch should still point at commit %s, got %s", followupCommit, got)
	}
	if err := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", followupCommit, branch).Run(); err != nil {
		t.Fatalf("follow-up commit should remain reachable from branch %s: %v", branch, err)
	}
}

func TestCleanupOrphanedWorktrees_SkipsDirtyOrUnmergedOrphanedWorktrees(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	worktreeSvc := NewWorktreeService(taskRepo, projectRepo, settingsRepo)

	repoDir := createTestGitRepo(t)
	if err := settingsRepo.Set(ctx, "worktree_cleanup", "after_merge"); err != nil {
		t.Fatalf("failed to set cleanup policy: %v", err)
	}
	project := &models.Project{ID: "project-orphan-safety", Name: "Orphan Safety", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	unmergedTask := &models.Task{
		ID:        "unmerged-orphan-task-id",
		ProjectID: project.ID,
		Title:     "Unmerged Orphan",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, unmergedTask); err != nil {
		t.Fatalf("failed to create unmerged task: %v", err)
	}
	unmergedDir, unmergedBranch, err := worktreeSvc.SetupWorktree(ctx, unmergedTask, repoDir)
	if err != nil {
		t.Fatalf("failed to create unmerged worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unmergedDir, "unmerged.txt"), []byte("unmerged work\n"), 0644); err != nil {
		t.Fatalf("write unmerged file: %v", err)
	}
	if err := CommitWorktreeChanges(unmergedDir, "unmerged commit"); err != nil {
		t.Fatalf("commit unmerged work: %v", err)
	}
	unmergedCommit := gitRevParseTest(t, unmergedDir, "HEAD")
	if err := taskRepo.Delete(ctx, unmergedTask.ID); err != nil {
		t.Fatalf("delete unmerged task: %v", err)
	}

	dirtyTask := &models.Task{
		ID:        "dirty-orphan-task-id",
		ProjectID: project.ID,
		Title:     "Dirty Orphan",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, dirtyTask); err != nil {
		t.Fatalf("failed to create dirty task: %v", err)
	}
	dirtyDir, dirtyBranch, err := worktreeSvc.SetupWorktree(ctx, dirtyTask, repoDir)
	if err != nil {
		t.Fatalf("failed to create dirty worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirtyDir, "dirty.txt"), []byte("dirty work\n"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	if err := taskRepo.Delete(ctx, dirtyTask.ID); err != nil {
		t.Fatalf("delete dirty task: %v", err)
	}

	cleanedCount, err := worktreeSvc.CleanupOrphanedWorktrees(ctx)
	if err != nil {
		t.Fatalf("CleanupOrphanedWorktrees failed: %v", err)
	}
	if cleanedCount != 0 {
		t.Fatalf("expected 0 orphaned worktrees cleaned, got %d", cleanedCount)
	}
	if _, err := os.Stat(unmergedDir); os.IsNotExist(err) {
		t.Fatal("unmerged orphaned worktree should not be removed")
	}
	if got := gitRevParseTest(t, repoDir, unmergedBranch); got != unmergedCommit {
		t.Fatalf("unmerged branch should still point at commit %s, got %s", unmergedCommit, got)
	}
	if _, err := os.Stat(dirtyDir); os.IsNotExist(err) {
		t.Fatal("dirty orphaned worktree should not be removed")
	}
	if got := gitRevParseTest(t, repoDir, dirtyBranch); got == "" {
		t.Fatal("dirty orphaned branch should still exist")
	}
}

func TestTaskIDFromWorktreePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		id   string
		ok   bool
	}{
		{name: "valid", path: "/tmp/repo/.worktrees/task_abc123", id: "abc123", ok: true},
		{name: "followup", path: "/tmp/repo/.worktrees/task_abc123_followup_1781070494882330000", id: "abc123", ok: true},
		{name: "actual long followup", path: "/tmp/repo/.worktrees/task_c9e1a52b817437cef5c95387ccc011e1_followup_1781070494882330000", id: "c9e1a52b817437cef5c95387ccc011e1", ok: true},
		{name: "missing prefix", path: "/tmp/repo/.worktrees/abc123", id: "", ok: false},
		{name: "empty id", path: "/tmp/repo/.worktrees/task_", id: "", ok: false},
		{name: "empty followup id", path: "/tmp/repo/.worktrees/task__followup_1781070494882330000", id: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := taskIDFromWorktreePath(tt.path)
			if id != tt.id || ok != tt.ok {
				t.Fatalf("taskIDFromWorktreePath(%q) = (%q, %v), want (%q, %v)", tt.path, id, ok, tt.id, tt.ok)
			}
		})
	}
}

func TestListGitWorktrees(t *testing.T) {
	// Create a test git repo
	tempDir := t.TempDir()
	repoDir := filepath.Join(tempDir, "test-repo")
	exec.Command("git", "init", repoDir).Run()
	exec.Command("git", "-C", repoDir, "config", "user.email", "test@test.com").Run()
	exec.Command("git", "-C", repoDir, "config", "user.name", "Test User").Run()

	// Create initial commit
	testFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(testFile, []byte("# Test"), 0644)
	exec.Command("git", "-C", repoDir, "add", ".").Run()
	exec.Command("git", "-C", repoDir, "commit", "-m", "initial").Run()

	// Create a worktree
	worktreeDir := filepath.Join(tempDir, "test-worktree")
	exec.Command("git", "-C", repoDir, "worktree", "add", worktreeDir, "-b", "test-branch").Run()

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	resolvedWorktreeDir, _ := filepath.EvalSymlinks(worktreeDir)
	if resolvedWorktreeDir == "" {
		resolvedWorktreeDir = worktreeDir
	}

	// List worktrees
	worktrees, err := ListGitWorktrees(repoDir)
	if err != nil {
		t.Fatalf("ListGitWorktrees failed: %v", err)
	}

	// Should have at least 2 worktrees: main repo + test worktree
	if len(worktrees) < 2 {
		t.Fatalf("expected at least 2 worktrees, got %d", len(worktrees))
	}

	// Find the test worktree
	var foundTestWorktree bool
	for _, wt := range worktrees {
		// Resolve symlinks for comparison
		resolvedWtPath, _ := filepath.EvalSymlinks(wt.Path)
		if resolvedWtPath == "" {
			resolvedWtPath = wt.Path
		}

		if resolvedWtPath == resolvedWorktreeDir {
			foundTestWorktree = true
			if wt.Branch != "test-branch" {
				t.Errorf("expected branch 'test-branch', got '%s'", wt.Branch)
			}
			if wt.IsMain {
				t.Error("test worktree should not be marked as main")
			}
		}
	}

	if !foundTestWorktree {
		t.Errorf("test worktree not found in list")
	}
}

func TestGetWorktreeDiffMissingRefsAndWorktreeReturnEmpty(t *testing.T) {
	repoDir := createTestGitRepo(t)
	defaultBranch := GetDefaultBranch(repoDir)

	if diff := GetWorktreeDiff(repoDir, "missing-branch", defaultBranch); diff != "" {
		t.Fatalf("expected empty diff for missing branch, got %q", diff)
	}
	if diff := GetWorktreeDiffWithUncommitted(repoDir, "missing-branch", defaultBranch, filepath.Join(repoDir, ".worktrees", "missing")); diff != "" {
		t.Fatalf("expected empty diff for missing branch and worktree, got %q", diff)
	}
	if diff := GetWorktreeDiff(filepath.Join(repoDir, "missing-repo"), "missing-branch", defaultBranch); diff != "" {
		t.Fatalf("expected empty diff for missing repo, got %q", diff)
	}
}

func TestGetWorktreeDiffWithUncommitted(t *testing.T) {
	repoDir := createTestGitRepo(t)

	// Create a feature branch and worktree
	branchName := "task/test-uncommitted"
	mainBranch := GetDefaultBranch(repoDir)
	wtPath := filepath.Join(repoDir, ".worktrees", "task_test")

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, wtPath, mainBranch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree: %v\n%s", err, out)
	}

	// Set git config in worktree
	for _, args := range [][]string{
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wtPath
		cmd.Run()
	}

	// Test 1: No changes - should return empty
	diff := GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if diff != "" {
		t.Errorf("expected empty diff with no changes, got: %q", diff)
	}

	// Test 2: Committed changes only
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("committed update\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "committed.txt"), []byte("committed content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(wtPath, "add committed file"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if !strings.Contains(diff, "committed.txt") {
		t.Error("expected committed changes to appear in diff")
	}

	// Target-only commits must not appear as reverse changes in the task diff.
	if err := os.WriteFile(filepath.Join(repoDir, "target-only.txt"), []byte("target only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "target-only.txt")
	runGitTest(t, repoDir, "commit", "-m", "advance target")
	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if strings.Contains(diff, "target-only.txt") || strings.Contains(diff, "target only") {
		t.Fatalf("target-only change appeared reversed in task diff:\n%s", diff)
	}
	if !strings.Contains(diff, "committed.txt") {
		t.Fatalf("expected task change after target advanced, got:\n%s", diff)
	}

	// Test 3: Uncommitted changes should also appear
	if err := os.WriteFile(filepath.Join(wtPath, "uncommitted.txt"), []byte("uncommitted content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if !strings.Contains(diff, "committed.txt") {
		t.Error("expected committed changes to still appear")
	}
	if !strings.Contains(diff, "uncommitted.txt") {
		t.Error("expected uncommitted (untracked) file to appear in diff")
	}

	// Test 4: Modified tracked file (uncommitted) should appear as one net diff
	// for the path, not one committed diff block plus one follow-up/uncommitted block.
	if err := os.WriteFile(filepath.Join(wtPath, "committed.txt"), []byte("modified content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if !strings.Contains(diff, "modified content") {
		t.Error("expected uncommitted modification to appear in diff")
	}
	if count := strings.Count(diff, "diff --git a/committed.txt b/committed.txt"); count != 1 {
		t.Fatalf("expected one combined diff block for committed.txt, got %d:\n%s", count, diff)
	}
	if strings.Contains(diff, "+committed content\n") {
		t.Errorf("expected stale intermediate committed content to be collapsed from combined diff, got:\n%s", diff)
	}

	// Test 5: Reverting a committed tracked file back to the target tree should
	// not fall back to the stale committed branch diff.
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, wtPath)
	if strings.Contains(diff, "README.md") {
		t.Errorf("expected reverted tracked file to disappear from live net diff, got:\n%s", diff)
	}

	// Test 6: Without worktree path, only committed changes shown
	diff = GetWorktreeDiffWithUncommitted(repoDir, branchName, mainBranch, "")
	if !strings.Contains(diff, "committed.txt") {
		t.Error("expected committed changes with empty worktree path")
	}
	if strings.Contains(diff, "uncommitted.txt") {
		t.Error("should not show untracked files when worktree path is empty")
	}
}

func TestGetWorktreeDiffFileWithUncommittedTargetsOneChangedFile(t *testing.T) {
	repoDir := createTestGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoDir, "delete-me.txt"), []byte("delete me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "rename-old.txt"), []byte("rename me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "binary.bin"), []byte{0, 1, 2}, 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", ".")
	runGitTest(t, repoDir, "commit", "-m", "add target fixtures")

	targetBranch := GetDefaultBranch(repoDir)
	branchName := "task/lazy-file-target"
	worktreePath := filepath.Join(repoDir, ".worktrees", "lazy-file-target")
	runGitTest(t, repoDir, "worktree", "add", "-b", branchName, worktreePath, targetBranch)

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# Test\ntracked edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(worktreePath, "delete-me.txt")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktreePath, "mv", "rename-old.txt", "rename-new.txt")
	if err := os.WriteFile(filepath.Join(worktreePath, "rename-new.txt"), []byte("renamed content\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "binary.bin"), []byte{0, 3, 4}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "untracked.txt"), []byte("only this untracked file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mergeBase := runGitTest(t, worktreePath, "merge-base", targetBranch, "HEAD")
	targets, err := worktreeDiffFileTargets(worktreePath, mergeBase)
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	indexes := map[string]int{}
	for i, target := range targets {
		indexes[target.Path] = i
	}

	cases := []struct {
		path   string
		want   string
		forbid string
	}{
		{path: "README.md", want: "tracked edit", forbid: "only this untracked file"},
		{path: "delete-me.txt", want: "deleted file mode", forbid: "tracked edit"},
		{path: "binary.bin", want: "Binary files", forbid: "delete-me.txt"},
		{path: "untracked.txt", want: "only this untracked file", forbid: "tracked edit"},
	}
	for _, tc := range cases {
		idx, ok := indexes[tc.path]
		if !ok {
			t.Fatalf("missing target %s in %#v", tc.path, targets)
		}
		diff, ok := GetWorktreeDiffFileWithUncommitted(repoDir, branchName, targetBranch, worktreePath, idx)
		if !ok {
			t.Fatalf("expected diff for %s", tc.path)
		}
		if !strings.Contains(diff, tc.want) || strings.Contains(diff, tc.forbid) {
			t.Fatalf("unexpected targeted diff for %s:\n%s", tc.path, diff)
		}
	}

	renameIdx, ok := indexes["rename-new.txt"]
	if !ok {
		t.Fatalf("missing rename target in %#v", targets)
	}
	renameDiff, ok := GetWorktreeDiffFileWithUncommitted(repoDir, branchName, targetBranch, worktreePath, renameIdx)
	if !ok || !strings.Contains(renameDiff, "rename-new.txt") || strings.Contains(renameDiff, "only this untracked file") {
		t.Fatalf("unexpected rename targeted diff:\n%s", renameDiff)
	}
}

func TestGetWorktreeDiffFileWithUncommittedUsesPathScopedGitDiff(t *testing.T) {
	repoDir := createTestGitRepo(t)
	for i := 0; i < 200; i++ {
		path := filepath.Join(repoDir, "bulk", "file-"+leftPad3(i)+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("base\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repoDir, "add", ".")
	runGitTest(t, repoDir, "commit", "-m", "add bulk files")

	targetBranch := GetDefaultBranch(repoDir)
	branchName := "task/lazy-path-count"
	worktreePath := filepath.Join(repoDir, ".worktrees", "lazy-path-count")
	runGitTest(t, repoDir, "worktree", "add", "-b", branchName, worktreePath, targetBranch)
	for i := 0; i < 200; i++ {
		path := filepath.Join(worktreePath, "bulk", "file-"+leftPad3(i)+".txt")
		if err := os.WriteFile(path, []byte("base\nchanged file "+leftPad3(i)+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	shimDir := t.TempDir()
	logPath := filepath.Join(shimDir, "git.log")
	shimPath := filepath.Join(shimDir, "git")
	shim := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\t' \"$arg\"; done >> " + shellQuoteForTest(logPath) + "\n" +
		"printf '\\n' >> " + shellQuoteForTest(logPath) + "\n" +
		"exec " + shellQuoteForTest(realGit) + " \"$@\"\n"
	if err := os.WriteFile(shimPath, []byte(shim), 0755); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	beforeStart := time.Now()
	fullDiff := GetWorktreeDiffWithUncommitted(repoDir, branchName, targetBranch, worktreePath)
	fullStats := GetWorktreeFileStatsWithUncommitted(repoDir, branchName, targetBranch, worktreePath)
	beforeElapsed := time.Since(beforeStart)
	if !strings.Contains(fullDiff, "changed file 000") || !strings.Contains(fullDiff, "changed file 199") {
		t.Fatalf("expected baseline full diff to include all changed files")
	}
	if len(fullStats) != 200 {
		t.Fatalf("expected baseline full stats for 200 files, got %d", len(fullStats))
	}
	beforeLogBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read baseline git log: %v", err)
	}
	beforeCommands := strings.FieldsFunc(strings.TrimSpace(string(beforeLogBytes)), func(r rune) bool { return r == '\n' })
	baselineFullPatchDiff := false
	for _, command := range beforeCommands {
		if strings.HasPrefix(command, "diff\t") && !strings.Contains(command, "--name-status") && !strings.Contains(command, "--\t") {
			baselineFullPatchDiff = true
			break
		}
	}
	if !baselineFullPatchDiff {
		t.Fatalf("expected baseline resolver shape to run a full patch diff, got:\n%s", beforeLogBytes)
	}
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("reset git log: %v", err)
	}

	afterStart := time.Now()
	diff, ok := GetWorktreeDiffFileWithUncommitted(repoDir, branchName, targetBranch, worktreePath, 123)
	afterElapsed := time.Since(afterStart)
	if !ok {
		t.Fatal("expected targeted diff")
	}
	if !strings.Contains(diff, "changed file 123") || strings.Contains(diff, "changed file 122") || strings.Contains(diff, "changed file 124") {
		t.Fatalf("expected only requested file diff, got:\n%s", diff)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read git log: %v", err)
	}
	commands := strings.FieldsFunc(strings.TrimSpace(string(logBytes)), func(r rune) bool { return r == '\n' })
	if len(commands) > 6 {
		t.Fatalf("expected bounded git subprocess count, got %d commands:\n%s", len(commands), logBytes)
	}
	if len(beforeCommands) <= len(commands) {
		t.Fatalf("expected targeted path to use fewer git subprocesses than baseline, before=%d after=%d\nbefore:\n%s\nafter:\n%s", len(beforeCommands), len(commands), beforeLogBytes, logBytes)
	}
	pathScopedPatchDiff := false
	for _, command := range commands {
		if strings.HasPrefix(command, "diff\t") && strings.Contains(command, "--\tbulk/file-123.txt") {
			pathScopedPatchDiff = true
		}
		if strings.HasPrefix(command, "diff\t") && !strings.Contains(command, "--name-status") && !strings.Contains(command, "--\t") {
			t.Fatalf("unexpected full patch diff command %q in:\n%s", command, logBytes)
		}
	}
	if !pathScopedPatchDiff {
		t.Fatalf("expected one path-scoped patch diff command, got:\n%s", logBytes)
	}
	t.Logf("200-file lazy diff baseline used %d git subprocesses in %s; targeted used %d git subprocesses in %s", len(beforeCommands), beforeElapsed, len(commands), afterElapsed)
}

func leftPad3(i int) string {
	if i < 10 {
		return "00" + string(rune('0'+i))
	}
	if i < 100 {
		return "0" + string(rune('0'+i/10)) + string(rune('0'+i%10))
	}
	return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func TestGetWorktreeFileStatsWithUncommittedMatchesNetTargetDiff(t *testing.T) {
	repoDir := createTestGitRepo(t)
	targetBranch := GetDefaultBranch(repoDir)
	branchName := "task/net-file-stats"
	worktreePath := filepath.Join(repoDir, ".worktrees", "net-file-stats")

	cmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath, targetBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create worktree: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("committed version\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "committed.txt"), []byte("committed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := CommitWorktreeChanges(worktreePath, "commit task output"); err != nil {
		t.Fatalf("commit task output: %v", err)
	}

	// Revert one committed path to the target, stage a post-commit path, leave
	// another tracked path unstaged, and append an untracked file. The summary
	// must describe exactly the same target-to-working-tree state as the diff.
	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "committed.txt"), []byte("unstaged update\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "staged.txt"), []byte("staged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "staged.txt")
	cmd.Dir = worktreePath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stage file: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "untracked.txt"), []byte("untracked\n"), 0644); err != nil {
		t.Fatal(err)
	}

	diff := GetWorktreeDiffWithUncommitted(repoDir, branchName, targetBranch, worktreePath)
	stats := GetWorktreeFileStatsWithUncommitted(repoDir, branchName, targetBranch, worktreePath)
	paths := make(map[string]string, len(stats))
	for _, stat := range stats {
		paths[stat.Path] = stat.Status
	}
	if strings.Contains(diff, "README.md") {
		t.Fatalf("expected reverted path to be absent from net diff:\n%s", diff)
	}
	if _, ok := paths["README.md"]; ok {
		t.Fatalf("expected reverted path to be absent from net file stats, got %#v", stats)
	}
	for path, status := range map[string]string{"committed.txt": "added", "staged.txt": "added", "untracked.txt": "added"} {
		if paths[path] != status {
			t.Fatalf("expected %s status %s, got %q in %#v", path, status, paths[path], stats)
		}
		if !strings.Contains(diff, path) {
			t.Fatalf("expected %s in net diff:\n%s", path, diff)
		}
	}
}

func TestGetWorktreeDiffUsesMergeBaseToShowTaskChanges(t *testing.T) {
	repoDir := createTestGitRepo(t)
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	defaultBranch := GetCurrentBranch(repoDir)
	if defaultBranch == "" {
		defaultBranch = GetDefaultBranch(repoDir)
	}

	// Simulate an older task branch with a large historical change.
	runGit("checkout", "-b", "task/stale-base")
	if err := os.WriteFile(filepath.Join(repoDir, "large-feature.txt"), []byte("large feature\n"), 0644); err != nil {
		t.Fatalf("write large feature: %v", err)
	}
	runGit("add", "large-feature.txt")
	runGit("commit", "-m", "large task change")

	// Target advances with an equivalent squashed version of that old change.
	runGit("checkout", defaultBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "large-feature.txt"), []byte("large feature\n"), 0644); err != nil {
		t.Fatalf("write squashed large feature: %v", err)
	}
	runGit("add", "large-feature.txt")
	runGit("commit", "-m", "squash large task change")

	// The task branch adds a follow-up after the target independently receives
	// equivalent content. The Changes UI should still show both changes authored
	// on the task branch, without reversing target-only changes.
	runGit("checkout", "task/stale-base")
	if err := os.WriteFile(filepath.Join(repoDir, "followup.txt"), []byte("small followup\n"), 0644); err != nil {
		t.Fatalf("write followup: %v", err)
	}
	runGit("add", "followup.txt")
	runGit("commit", "-m", "small followup")

	diff := GetWorktreeDiff(repoDir, "task/stale-base", defaultBranch)
	if !strings.Contains(diff, "large-feature.txt") || !strings.Contains(diff, "large feature") {
		t.Fatalf("expected task-authored feature in merge-base diff, got:\n%s", diff)
	}
	if !strings.Contains(diff, "followup.txt") || !strings.Contains(diff, "small followup") {
		t.Fatalf("expected follow-up change in diff, got:\n%s", diff)
	}

	stats := GetWorktreeFileStats(repoDir, "task/stale-base", defaultBranch)
	if len(stats) != 2 {
		t.Fatalf("expected both task-authored file stats, got %#v", stats)
	}
	paths := map[string]bool{}
	for _, stat := range stats {
		paths[stat.Path] = true
	}
	if !paths["large-feature.txt"] || !paths["followup.txt"] {
		t.Fatalf("expected file stats for both task-authored changes, got %#v", stats)
	}
}

func TestIsBranchDivergedFromTargetRequiresUniqueCommitsOnBothSides(t *testing.T) {
	repoDir := createTestGitRepo(t)
	targetBranch := GetCurrentBranch(repoDir)
	branchName := "task/rebase-eligibility"
	runGitTest(t, repoDir, "branch", branchName, targetBranch)

	if err := os.WriteFile(filepath.Join(repoDir, "target-only.txt"), []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "target-only.txt")
	runGitTest(t, repoDir, "commit", "-m", "target-only commit")
	if IsBranchDivergedFromTarget(repoDir, branchName, targetBranch) {
		t.Fatal("expected a branch with no unique task commits not to offer rebase")
	}

	runGitTest(t, repoDir, "checkout", branchName)
	if err := os.WriteFile(filepath.Join(repoDir, "task-only.txt"), []byte("task\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repoDir, "add", "task-only.txt")
	runGitTest(t, repoDir, "commit", "-m", "task-only commit")
	if !IsBranchDivergedFromTarget(repoDir, branchName, targetBranch) {
		t.Fatal("expected branches with unique commits on both sides to offer rebase")
	}
	if IsBranchDivergedFromTarget(repoDir, "missing-branch", targetBranch) {
		t.Fatal("expected missing branch not to offer rebase")
	}
}
