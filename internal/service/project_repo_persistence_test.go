package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestValidateRepoPaths_DetectsMissingRepoPath verifies that startup validation
// catches project repo paths that no longer exist on disk — the core of the
// container-restart persistence bug.
func TestValidateRepoPaths_DetectsMissingRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	svc := NewProjectService(projectRepo)
	ctx := context.Background()

	// Create a project whose repo_path points to a non-existent directory
	// (simulates the state after container restart with ephemeral storage).
	p := &models.Project{
		Name:     "Ghost Repo Project",
		RepoPath: "/nonexistent/repos/ghost-project-id",
		RepoURL:  "https://github.com/owner/repo",
	}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	missing := svc.ValidateRepoPaths(ctx)
	if len(missing) != 1 {
		t.Fatalf("expected 1 missing repo path warning, got %d", len(missing))
	}
	if missing[0] == "" {
		t.Fatal("expected non-empty warning message")
	}
}

// TestValidateRepoPaths_SkipsExistingPath ensures valid repo paths don't
// produce false positives.
func TestValidateRepoPaths_SkipsExistingPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	svc := NewProjectService(projectRepo)
	ctx := context.Background()

	existingDir := t.TempDir()
	p := &models.Project{
		Name:     "Valid Repo Project",
		RepoPath: existingDir,
	}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	missing := svc.ValidateRepoPaths(ctx)
	if len(missing) != 0 {
		t.Fatalf("expected 0 missing repo path warnings, got %d: %v", len(missing), missing)
	}
}

// TestValidateRepoPaths_SkipsEmptyRepoPath ensures projects without repo_path
// (no repo configured) don't produce warnings.
func TestValidateRepoPaths_SkipsEmptyRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	svc := NewProjectService(projectRepo)
	ctx := context.Background()

	p := &models.Project{
		Name: "No Repo Project",
	}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	missing := svc.ValidateRepoPaths(ctx)
	if len(missing) != 0 {
		t.Fatalf("expected 0 missing repo path warnings, got %d", len(missing))
	}
}

// TestExecuteTaskWithAgent_FailsOnMissingRepoPath verifies that task execution
// produces a clear failure when the project repo path is gone (post-restart).
func TestExecuteTaskWithAgent_FailsOnMissingRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	// Create project with a non-existent repo path (simulates container restart)
	project := &models.Project{
		Name:     "Missing Repo Project",
		RepoPath: "/nonexistent/repos/missing-project",
		RepoURL:  "https://github.com/owner/repo",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Should Fail Missing Repo",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Execute — should not call the LLM at all since repo is missing
	_, execErr := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if execErr == nil {
		t.Error("expected error from ExecuteTaskWithAgent when repo path is missing")
	}

	if mock.CallCount() != 0 {
		t.Error("expected LLM mock NOT to be called when repo path is missing")
	}

	// Task should be marked failed
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Status != models.StatusFailed {
		t.Errorf("expected task status=%q, got %q", models.StatusFailed, updated.Status)
	}

	// Execution should capture the error
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) == 0 {
		t.Fatal("expected at least one execution record")
	}
	if execs[0].Status != models.ExecFailed {
		t.Errorf("expected execution status=%q, got %q", models.ExecFailed, execs[0].Status)
	}
}

// TestEnsureRepoRoot_UnderDataVolume verifies that when PROJECT_REPO_ROOT
// points to a /data subdirectory, cloned repos are stored under the
// persistent volume path.
func TestEnsureRepoRoot_UnderDataVolume(t *testing.T) {
	dataDir := t.TempDir()
	repoRoot := filepath.Join(dataDir, "repos")

	settingsRepo := repository.NewSettingsRepo(testutil.NewTestDB(t))
	svc := NewGitHubService(settingsRepo, "", "", "", repoRoot)

	ctx := context.Background()
	got, err := svc.ensureRepoRoot(ctx)
	if err != nil {
		t.Fatalf("ensureRepoRoot: %v", err)
	}

	// Verify directory was created under the data volume
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("expected repo root to exist at %s: %v", got, err)
	}

	absRepoRoot, _ := filepath.Abs(repoRoot)
	if got != absRepoRoot {
		t.Errorf("expected repo root=%q, got %q", absRepoRoot, got)
	}
}
