package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

// TestHandler_GetTaskChanges_MergedTaskShowsPreservedDiff verifies that after a task is merged,
// the changes tab still shows the preserved diff from the execution, not an empty live diff.
func TestHandler_GetTaskChanges_MergedTaskShowsPreservedDiff(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	_ = e // Use the echo instance variable

	// Get default project from test setup
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		// Create a project if none exists
		project := &models.Project{
			Name:     "Test Project",
			RepoPath: "/tmp/test-repo",
		}
		if err := projectRepo.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}
		projects = []models.Project{*project}
	}
	project := &projects[0]

	// Create task using the handler's task repo
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)

	// Create a task with worktree that has been merged
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merged Task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      "/tmp/.worktrees/task_123",
		WorktreeBranch:    "task/123-merged-task",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusMerged, // Key: task is merged
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get a default agent config for the execution
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agents, _ := llmConfigRepo.List(ctx)
	var agentID string
	if len(agents) > 0 {
		agentID = agents[0].ID
	}

	// Create an execution with preserved diff output
	preservedDiff := `diff --git a/file.txt b/file.txt
index abc123..def456 100644
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,3 @@
 line 1
-old content
+new content
 line 3`

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecCompleted,
		PromptSent:    "Test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Set the diff output separately (this is how it's done in llm_service.go)
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, preservedDiff); err != nil {
		t.Fatalf("failed to update diff output: %v", err)
	}
	exec.DiffOutput = preservedDiff // Update local copy for consistency

	// Verify task was created with worktree info
	t.Logf("Created task ID: %s, WorktreeBranch: %s, MergeStatus: %s", task.ID, task.WorktreeBranch, task.MergeStatus)

	// Fetch task again to verify it was saved correctly
	fetchedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch task: %v", err)
	}
	t.Logf("Fetched task ID: %s, WorktreeBranch: %s, MergeStatus: %s", fetchedTask.ID, fetchedTask.WorktreeBranch, fetchedTask.MergeStatus)

	// Verify execution was created with diff
	latestDiff, err := execRepo.GetLatestNonEmptyDiffOutput(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch latest diff output: %v", err)
	}
	t.Logf("Latest non-empty diff output length=%d", len(latestDiff))
	if len(latestDiff) > 100 {
		t.Logf("DiffOutput (first 100 chars): %s", latestDiff[:100])
	} else if latestDiff != "" {
		t.Logf("DiffOutput: %s", latestDiff)
	}

	// Create a request to get the changes tab
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	// Execute the handler
	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}

	// Verify the response contains the preserved diff
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d. Body: %s", rec.Code, body)
	}

	// The response should contain the preserved diff content
	if body == "" {
		t.Error("expected non-empty response body")
	}

	// Debug: print the first 500 chars of response
	if len(body) > 500 {
		t.Logf("Response body (first 500 chars): %s", body[:500])
	} else {
		t.Logf("Response body: %s", body)
	}

	// Verify the diff content is present (check for a unique string from the diff)
	if !containsString(body, "new content") {
		t.Errorf("expected preserved diff to contain 'new content', but it was missing")
	}

	// Verify it's showing the worktree changes view (not the regular execution view)
	// The worktree view should mention the branch name
	if !containsString(body, task.WorktreeBranch) {
		t.Errorf("expected response to show worktree branch %s", task.WorktreeBranch)
	}
}

// TestHandler_GetTaskChanges_UnmergedTaskShowsLiveDiff verifies that for unmerged tasks,
// we still attempt to show live diff if the worktree exists.
func TestHandler_GetTaskChanges_UnmergedTaskShowsLiveDiff(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	_ = e // Use the echo instance variable

	// Get default project from test setup
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		// Create a project if none exists
		project := &models.Project{
			Name:     "Test Project",
			RepoPath: "/tmp/test-repo",
		}
		if err := projectRepo.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}
		projects = []models.Project{*project}
	}
	project := &projects[0]

	// Create task using repos
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)

	// Create a task with worktree that is NOT merged (status = pending)
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Unmerged Task",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreePath:      "/tmp/.worktrees/task_456",
		WorktreeBranch:    "task/456-unmerged-task",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending, // Key: task is NOT merged yet
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get a default agent config for the execution
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agents, _ := llmConfigRepo.List(ctx)
	var agentID string
	if len(agents) > 0 {
		agentID = agents[0].ID
	}

	// Create an execution with preserved diff
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecCompleted,
		PromptSent:    "Test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Set the diff output separately
	preservedDiff := "diff --git a/file.txt b/file.txt\n+some changes"
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, preservedDiff); err != nil {
		t.Fatalf("failed to update diff output: %v", err)
	}
	exec.DiffOutput = preservedDiff

	// Create a request to get the changes tab
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	// Execute the handler
	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}

	// For unmerged tasks, if the worktree doesn't exist (which is likely in this test),
	// it should fall back to showing the preserved diff
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	if body == "" {
		t.Error("expected non-empty response body")
	}

	// Since the worktree path doesn't exist in the test environment,
	// it should show the preserved diff as a fallback
	if !containsString(body, "some changes") {
		t.Error("expected fallback to preserved diff when worktree doesn't exist")
	}
}

// TestHandler_GetTaskChanges_PendingMergeStatusButMergedBranchShowsPreservedDiff verifies
// stale merge_status records do not hide changes after a successful manual merge.
func TestHandler_GetTaskChanges_PendingMergeStatusButMergedBranchShowsPreservedDiff(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	mainBranch := gitCurrentBranch(t, repoDir)
	taskBranch := "task/stale-merge-status"

	runGit(t, repoDir, "checkout", "-b", taskBranch)
	testFile := filepath.Join(repoDir, "merged_file.txt")
	if err := os.WriteFile(testFile, []byte("new content from merged branch\n"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	runGit(t, repoDir, "add", "merged_file.txt")
	runGit(t, repoDir, "commit", "-m", "task branch change")
	runGit(t, repoDir, "checkout", mainBranch)
	runGit(t, repoDir, "merge", "--no-ff", "-m", "merge task branch", taskBranch)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{
		Name:     "Merged Branch Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agents, _ := llmConfigRepo.List(ctx)
	var agentID string
	if len(agents) > 0 {
		agentID = agents[0].ID
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Stale merge status task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      t.TempDir(),
		WorktreeBranch:    taskBranch,
		MergeTargetBranch: mainBranch,
		MergeStatus:       models.MergeStatusPending, // stale status despite merged branch
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecCompleted,
		PromptSent:    "stale merge status regression",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	preservedDiff := "diff --git a/merged_file.txt b/merged_file.txt\n+new content from merged branch"
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, preservedDiff); err != nil {
		t.Fatalf("update diff output: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !containsString(body, "new content from merged branch") {
		t.Fatalf("expected preserved diff content in response body, got: %s", body)
	}
}

func TestHandler_GetTaskChanges_CompletedUnmergedWorktreeShowsUncommittedChanges(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	mainBranch := gitCurrentBranch(t, repoDir)
	taskBranch := "task/8176b26b-anthropic-uncommitted"
	worktreePath := filepath.Join(repoDir, ".worktrees", "task_uncommitted")
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, mainBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "claude-uncommitted.txt"), []byte("anthropic edit preserved\n"), 0644); err != nil {
		t.Fatalf("write uncommitted file: %v", err)
	}

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Uncommitted Worktree Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Anthropic uncommitted task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      worktreePath,
		WorktreeBranch:    taskBranch,
		MergeTargetBranch: mainBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !containsString(body, "anthropic edit preserved") {
		t.Fatalf("expected uncommitted worktree diff in response body, got: %s", body)
	}
	if !containsString(body, "/tasks/"+task.ID+"/worktree/merge") {
		t.Fatalf("expected local merge actions to remain available for unmerged uncommitted worktree changes")
	}
}

// TestHandler_GetTaskChanges_FastForwardMergedBranchHidesMergeOptions
// reproduces the stale-state bug where a task branch that is already reachable
// from the target branch keeps surfacing local merge options on the Changes tab.
// After the fix, the changes-tab response must NOT include local /worktree/merge
// endpoint actions, and stale merge_status is back-filled to "merged" when found.
func TestHandler_GetTaskChanges_StaleMergedBlankMetadataRecoverableBranchShowsFastForwardOption(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Stale Metadata Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "stale_metadata.txt"), []byte("recover stale merge metadata\n"), 0644); err != nil {
		t.Fatalf("write stale metadata file: %v", err)
	}
	runGit(t, worktreePath, "add", "stale_metadata.txt")
	runGit(t, worktreePath, "commit", "-m", "recover stale metadata")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || !strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected fast-forward merge action for recoverable ahead branch, body=%s", body)
	}
	if !strings.Contains(body, "recover stale merge metadata") {
		t.Fatalf("expected live worktree diff, body=%s", body)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected stale merge_status reset to pending, got %s", updated.MergeStatus)
	}
	if updated.WorktreePath != worktreePath {
		t.Fatalf("expected worktree path recovered to %q, got %q", worktreePath, updated.WorktreePath)
	}
	if updated.WorktreeBranch != taskBranch {
		t.Fatalf("expected worktree branch recovered to %q, got %q", taskBranch, updated.WorktreeBranch)
	}
}

func TestHandler_GetTaskChanges_StaleMergedDivergedBranchShowsLocalActions(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Diverged Stale Merged Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "task_change.txt"), []byte("task branch change\n"), 0644); err != nil {
		t.Fatalf("write task change file: %v", err)
	}
	runGit(t, worktreePath, "add", "task_change.txt")
	runGit(t, worktreePath, "commit", "-m", "task branch change")

	if err := os.WriteFile(filepath.Join(repoDir, "target_change.txt"), []byte("target branch moved\n"), 0644); err != nil {
		t.Fatalf("write target change file: %v", err)
	}
	runGit(t, repoDir, "add", "target_change.txt")
	runGit(t, repoDir, "commit", "-m", "target branch moved")
	if counts := runGit(t, repoDir, "rev-list", "--left-right", "--count", targetBranch+"..."+taskBranch); counts != "1\t1" {
		t.Fatalf("expected diverged target/task branches, got counts %q", counts)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || !strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected local merge actions for stale merged diverged branch with task commits, body=%s", body)
	}
	if !strings.Contains(body, "task branch change") {
		t.Fatalf("expected live worktree diff for diverged branch, body=%s", body)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected stale merge_status reset to pending, got %s", updated.MergeStatus)
	}
	if updated.WorktreePath != worktreePath || updated.WorktreeBranch != taskBranch {
		t.Fatalf("expected recovered worktree metadata %q %q, got %q %q", worktreePath, taskBranch, updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestHandler_GetTask_DirectChangesTabLoadsRecoveredChangesRoute(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Direct Changes Tab Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "direct_changes.txt"), []byte("direct changes stale metadata\n"), 0644); err != nil {
		t.Fatalf("write direct changes file: %v", err)
	}
	runGit(t, worktreePath, "add", "direct_changes.txt")
	runGit(t, worktreePath, "commit", "-m", "direct changes stale metadata")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTask(c); err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, `hx-get="/tasks/`+task.ID+`/changes"`) || !strings.Contains(body, `hx-trigger="load"`) {
		t.Fatalf("expected direct changes tab to load recovered changes route, body=%s", body)
	}
	if strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected direct render to avoid stale inline merge controls before recovered route load, body=%s", body)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusMerged || updated.WorktreePath != "" || updated.WorktreeBranch != "" {
		t.Fatalf("expected direct page render not to mutate stale task before recovered route load, got status=%q path=%q branch=%q", updated.MergeStatus, updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestHandler_GetTaskWorktreeInfo_StaleMergedBlankMetadataShowsFastForwardOption(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Stale Metadata Worktree Info Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "worktree_info.txt"), []byte("worktree info stale metadata\n"), 0644); err != nil {
		t.Fatalf("write worktree info file: %v", err)
	}
	runGit(t, worktreePath, "add", "worktree_info.txt")
	runGit(t, worktreePath, "commit", "-m", "worktree info stale metadata")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskWorktreeInfo(c); err != nil {
		t.Fatalf("GetTaskWorktreeInfo failed: %v", err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || !strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected worktree panel fast-forward action for recoverable ahead branch, body=%s", body)
	}
	if !strings.Contains(body, taskBranch) || !strings.Contains(body, worktreePath) {
		t.Fatalf("expected recovered worktree metadata in panel, body=%s", body)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected stale merge_status reset to pending, got %s", updated.MergeStatus)
	}
	if updated.WorktreePath != worktreePath || updated.WorktreeBranch != taskBranch {
		t.Fatalf("expected recovered worktree metadata %q %q, got %q %q", worktreePath, taskBranch, updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestHandler_GetTaskChangesWorktree_StaleMergedBlankMetadataShowsFastForwardOption(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Stale Metadata Worktree Fragment Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "worktree_fragment.txt"), []byte("worktree fragment stale metadata\n"), 0644); err != nil {
		t.Fatalf("write worktree fragment file: %v", err)
	}
	runGit(t, worktreePath, "add", "worktree_fragment.txt")
	runGit(t, worktreePath, "commit", "-m", "worktree fragment stale metadata")

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/worktree", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChangesWorktree(c); err != nil {
		t.Fatalf("GetTaskChangesWorktree failed: %v", err)
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || !strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected worktree changes fragment fast-forward action for recoverable ahead branch, body=%s", body)
	}
	if !strings.Contains(body, "worktree fragment stale metadata") {
		t.Fatalf("expected live worktree diff, body=%s", body)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("expected stale merge_status reset to pending, got %s", updated.MergeStatus)
	}
	if updated.WorktreePath != worktreePath || updated.WorktreeBranch != taskBranch {
		t.Fatalf("expected recovered worktree metadata %q %q, got %q %q", worktreePath, taskBranch, updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestHandler_MergeTaskBranch_StaleMergedBlankMetadataFastForwardSucceeds(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	project := &models.Project{Name: "Stale Metadata Merge Project", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &models.Task{
		ID:                "f076cd4c16ee53c0a0e05418c388f12f",
		ProjectID:         project.ID,
		Title:             "Fix stale running executions causing completed tas",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	taskBranch := "task/f076cd4c-fix-stale-running-executions-causing-completed-tas"
	runGit(t, repoDir, "worktree", "add", "-b", taskBranch, worktreePath, targetBranch)
	if err := os.WriteFile(filepath.Join(worktreePath, "ff_stale_metadata.txt"), []byte("ff stale metadata\n"), 0644); err != nil {
		t.Fatalf("write ff stale metadata file: %v", err)
	}
	runGit(t, worktreePath, "add", "ff_stale_metadata.txt")
	runGit(t, worktreePath, "commit", "-m", "ff stale metadata")
	branchTip := runGit(t, repoDir, "rev-parse", taskBranch)

	form := url.Values{
		"merge_type":   {"ff"},
		"merge_source": {"changes_tab"},
	}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := runGit(t, repoDir, "rev-parse", targetBranch); got != branchTip {
		t.Fatalf("expected %s to fast-forward to task branch tip %s, got %s", targetBranch, branchTip, got)
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status merged after ff, got %s", updated.MergeStatus)
	}
	if updated.WorktreePath != worktreePath || updated.WorktreeBranch != taskBranch {
		t.Fatalf("expected recovered worktree metadata %q %q, got %q %q", worktreePath, taskBranch, updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestHandler_GetTaskChanges_FastForwardMergedBranchHidesMergeOptions(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	mainBranch := gitCurrentBranch(t, repoDir)
	taskBranch := "task/0f7ee252-smart-scrolling"

	// Create a task branch with a commit, then fast-forward main onto it.
	runGit(t, repoDir, "checkout", "-b", taskBranch)
	testFile := filepath.Join(repoDir, "scrolling.txt")
	if err := os.WriteFile(testFile, []byte("smart scrolling change\n"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	runGit(t, repoDir, "add", "scrolling.txt")
	runGit(t, repoDir, "commit", "-m", "implement smart scrolling")
	runGit(t, repoDir, "checkout", mainBranch)
	runGit(t, repoDir, "merge", "--ff-only", taskBranch)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "FF-Merged Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agents, _ := llmConfigRepo.List(ctx)
	var agentID string
	if len(agents) > 0 {
		agentID = agents[0].ID
	}

	// Worktree path doesn't need to exist for this test path; the handler
	// reconciles via IsBranchMerged on the project repo.
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Implement smart scrolling for active task thread streaming",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      t.TempDir(),
		WorktreeBranch:    taskBranch,
		MergeTargetBranch: mainBranch,
		MergeStatus:       models.MergeStatusPending, // stale stored state despite branch ancestry
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agentID,
		Status:        models.ExecCompleted,
		PromptSent:    "smart scrolling regression",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	preservedDiff := "diff --git a/scrolling.txt b/scrolling.txt\n+smart scrolling change"
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, preservedDiff); err != nil {
		t.Fatalf("update diff output: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Local merge actions must be hidden because the branch is already merged.
	if strings.Contains(body, "/worktree/merge") {
		t.Fatalf("expected local /worktree/merge actions to be hidden after fast-forward merge, body=%s", body)
	}
	// Preserved diff content should still render so the user can review what was merged.
	if !strings.Contains(body, "smart scrolling change") {
		t.Fatalf("expected preserved diff content in changes-tab response, body=%s", body)
	}

	// Stale merge_status should have been back-filled.
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status to be back-filled to merged, got %s", updated.MergeStatus)
	}
}

// TestHandler_MergeTaskBranch_RejectsAlreadyMergedBranch ensures that an
// in-flight merge POST (e.g. from a stale changes-tab UI) is rejected with 409
// when the branch is already merged into its target.
func TestHandler_MergeTaskBranch_RejectsAlreadyMergedBranch(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	mainBranch := gitCurrentBranch(t, repoDir)
	taskBranch := "task/already-merged"

	runGit(t, repoDir, "checkout", "-b", taskBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "x.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatalf("write x: %v", err)
	}
	runGit(t, repoDir, "add", "x.txt")
	runGit(t, repoDir, "commit", "-m", "x change")
	runGit(t, repoDir, "checkout", mainBranch)
	runGit(t, repoDir, "merge", "--ff-only", taskBranch)

	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Already Merged Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Already merged",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      t.TempDir(),
		WorktreeBranch:    taskBranch,
		MergeTargetBranch: mainBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	form := "merge_type=merge&merge_source=changes_tab"
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.MergeTaskBranch(c); err != nil {
		t.Fatalf("MergeTaskBranch returned error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for already-merged branch, got %d body=%s", rec.Code, rec.Body.String())
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("re-fetch task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status back-filled to merged after rejection, got %s", updated.MergeStatus)
	}
}

func TestHandler_TaskChangesEndpointsShareActiveLiveWorktreeResolution(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)

	project := &models.Project{Name: "Active Equivalent Changes Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Active equivalent changes",
		Category:          models.CategoryActive,
		Status:            models.StatusRunning,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusMerged, // stale from a previous execution
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	worktreeBranch := "task/" + task.ID[:8] + "-active-equivalent"
	runGit(t, repoDir, "worktree", "add", "-b", worktreeBranch, worktreePath, targetBranch)
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, worktreeBranch); err != nil {
		t.Fatalf("update worktree info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "active_live.txt"), []byte("active live equivalent diff\n"), 0644); err != nil {
		t.Fatalf("write active live file: %v", err)
	}

	agentID := firstAgentID(t, h)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agentID, Status: models.ExecRunning, PromptSent: "active equivalent"}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	staleDiff := "diff --git a/stale.txt b/stale.txt\n+stale preserved diff\n"
	if err := h.execRepo.UpdateDiffOutput(ctx, exec.ID, staleDiff); err != nil {
		t.Fatalf("update stale diff: %v", err)
	}

	fullBody := renderTaskChangesEndpoint(t, h, e, http.MethodGet, "/tasks/"+task.ID+"/changes", task.ID, false)
	worktreeBody := renderTaskChangesEndpoint(t, h, e, http.MethodGet, "/tasks/"+task.ID+"/changes/worktree", task.ID, true)

	assertBodyContainsAll(t, fullBody, "active_live.txt", "active live equivalent diff")
	assertBodyContainsAll(t, worktreeBody, "active_live.txt", "active live equivalent diff")
	assertBodyOmitsAll(t, fullBody, "stale.txt", "stale preserved diff")
	assertBodyOmitsAll(t, worktreeBody, "stale.txt", "stale preserved diff")
	if strings.Contains(fullBody, "/worktree/merge") != strings.Contains(worktreeBody, "/worktree/merge") {
		t.Fatalf("expected equivalent local action visibility full=%t worktree=%t", strings.Contains(fullBody, "/worktree/merge"), strings.Contains(worktreeBody, "/worktree/merge"))
	}
}

func TestHandler_TaskChangesEndpointsShareMissingWorktreeFallbackResolution(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := gitCurrentBranch(t, repoDir)
	taskBranch := "task/missing-worktree-equivalent"
	runGit(t, repoDir, "checkout", "-b", taskBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "branch_only.txt"), []byte("branch-only live diff\n"), 0644); err != nil {
		t.Fatalf("write branch-only file: %v", err)
	}
	runGit(t, repoDir, "add", "branch_only.txt")
	runGit(t, repoDir, "commit", "-m", "branch-only change")
	runGit(t, repoDir, "checkout", targetBranch)

	project := &models.Project{Name: "Missing Worktree Equivalent Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Missing worktree equivalent changes",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		WorktreePath:      filepath.Join(repoDir, ".worktrees", "does-not-exist"),
		WorktreeBranch:    taskBranch,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	agentID := firstAgentID(t, h)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agentID, Status: models.ExecCompleted, PromptSent: "missing worktree"}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	preservedDiff := "diff --git a/preserved_missing.txt b/preserved_missing.txt\n+preserved missing worktree diff\n"
	if err := h.execRepo.UpdateDiffOutput(ctx, exec.ID, preservedDiff); err != nil {
		t.Fatalf("update preserved diff: %v", err)
	}

	fullBody := renderTaskChangesEndpoint(t, h, e, http.MethodGet, "/tasks/"+task.ID+"/changes", task.ID, false)
	worktreeBody := renderTaskChangesEndpoint(t, h, e, http.MethodGet, "/tasks/"+task.ID+"/changes/worktree", task.ID, true)

	assertBodyContainsAll(t, fullBody, "preserved_missing.txt", "preserved missing worktree diff")
	assertBodyContainsAll(t, worktreeBody, "preserved_missing.txt", "preserved missing worktree diff")
	if strings.Contains(fullBody, "branch-only live diff") || strings.Contains(worktreeBody, "branch-only live diff") {
		t.Fatalf("expected both endpoints to use preserved fallback rather than branch live diff\nfull=%s\nworktree=%s", fullBody, worktreeBody)
	}
	if strings.Contains(fullBody, "/worktree/merge") != strings.Contains(worktreeBody, "/worktree/merge") {
		t.Fatalf("expected equivalent local action visibility full=%t worktree=%t", strings.Contains(fullBody, "/worktree/merge"), strings.Contains(worktreeBody, "/worktree/merge"))
	}
}

func renderTaskChangesEndpoint(t *testing.T, h *Handler, e *echo.Echo, method, path, taskID string, worktree bool) string {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(taskID)
	var err error
	if worktree {
		err = h.GetTaskChangesWorktree(c)
	} else {
		err = h.GetTaskChanges(c)
	}
	if err != nil {
		t.Fatalf("render %s failed: %v", path, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for %s, got %d body=%s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func firstAgentID(t *testing.T, h *Handler) string {
	t.Helper()
	agents, err := h.llmConfigRepo.List(context.Background())
	if err != nil || len(agents) == 0 {
		t.Fatalf("list agents: %v", err)
	}
	return agents[0].ID
}

func assertBodyContainsAll(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Fatalf("expected body to contain %q, body=%s", value, body)
		}
	}
}

func assertBodyOmitsAll(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Fatalf("expected body to omit %q, body=%s", value, body)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitCurrentBranch(t *testing.T, dir string) string {
	t.Helper()
	branch := runGit(t, dir, "branch", "--show-current")
	if branch == "" {
		t.Fatal("expected non-empty current branch")
	}
	return branch
}

// Helper to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
