package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProjectService_DeleteRollsBackTasksWhenLegacyConstraintFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))

	project := &models.Project{Name: "Suggestion Engine"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Must survive rollback", Prompt: "test", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	attachmentPath := filepath.Join(t.TempDir(), "rollback-attachment.txt")
	if err := os.WriteFile(attachmentPath, []byte("must survive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := attachmentRepo.Create(ctx, &models.Attachment{TaskID: task.ID, FileName: "rollback-attachment.txt", FilePath: attachmentPath, MediaType: "text/plain", FileSize: 12}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE memory_consolidation_runs (id TEXT PRIMARY KEY);
		CREATE TABLE memory_consolidation_schedules (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			last_run_id TEXT REFERENCES memory_consolidation_runs(id) ON DELETE SET NULL
		);
		INSERT INTO memory_consolidation_schedules(project_id) VALUES (?)`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE memory_consolidation_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	attachmentReadableBeforeRelationalDelete := false
	projectSvc.beforeRelationalDeleteForTest = func() {
		content, readErr := os.ReadFile(attachmentPath)
		if readErr == nil && string(content) == "must survive" {
			attachmentReadableBeforeRelationalDelete = true
		}
	}

	err := projectSvc.Delete(ctx, project.ID)
	if err == nil || !strings.Contains(err.Error(), "no such table: main.memory_consolidation_runs") {
		t.Fatalf("Delete error = %v, want missing memory_consolidation_runs", err)
	}
	storedProject, getErr := projectRepo.GetByID(ctx, project.ID)
	if getErr != nil || storedProject == nil {
		t.Fatalf("project after failed deletion = %#v, err=%v", storedProject, getErr)
	}
	storedTask, getErr := taskRepo.GetByID(ctx, task.ID)
	if getErr != nil || storedTask == nil {
		t.Fatalf("task after failed deletion = %#v, err=%v", storedTask, getErr)
	}
	if !attachmentReadableBeforeRelationalDelete {
		t.Fatal("active task attachment was unavailable before relational deletion completed")
	}
	if content, readErr := os.ReadFile(attachmentPath); readErr != nil || string(content) != "must survive" {
		t.Fatalf("attachment after failed deletion = %q, err=%v", content, readErr)
	}
}

func TestProjectService_DeleteRollbackDoesNotCancelOrMoveRunningTaskWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	workerSvc := NewWorkerService(nil, 0, projectRepo)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(NewTaskService(taskRepo, repository.NewAttachmentRepo(db), workerSvc))

	repoDir := filepath.Join(t.TempDir(), "running-repository")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("running\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "Initial commit")

	project := &models.Project{Name: "Running rollback project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Running task", Prompt: "test", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit("worktree", "add", "-b", "task/"+task.ID[:8]+"-running", worktreePath)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET worktree_path = ? WHERE id = ?`, worktreePath, task.ID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerSvc.RegisterCancel(task.ID, cancel)

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE memory_consolidation_runs (id TEXT PRIMARY KEY);
		CREATE TABLE memory_consolidation_schedules (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			last_run_id TEXT REFERENCES memory_consolidation_runs(id) ON DELETE SET NULL
		);
		INSERT INTO memory_consolidation_schedules(project_id) VALUES (?)`, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE memory_consolidation_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	if err := projectSvc.Delete(ctx, project.ID); err == nil || !strings.Contains(err.Error(), "no such table: main.memory_consolidation_runs") {
		t.Fatalf("forced project deletion error = %v, want relational failure before repository cleanup", err)
	}
	select {
	case <-runCtx.Done():
		t.Fatal("database rollback canceled the running task")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Fatalf("database rollback moved or removed the running task worktree: %v", err)
	}
}

func TestProjectService_DeleteCleansLocalTaskWorktreeAndMutationHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil))

	repoDir := filepath.Join(t.TempDir(), "user-repository")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit(repoDir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("user data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "README.md")
	runGit(repoDir, "commit", "-m", "Initial commit")

	project := &models.Project{Name: "Local worktree cleanup", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Managed worktree", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	branch := "task/" + task.ID[:8] + "-cleanup"
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "worktree", "add", "-b", branch, worktreePath)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET worktree_path = ?, worktree_branch = ? WHERE id = ?`, worktreePath, branch, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO agent_config_mutations
			(id, task_run_id, project_id, target_type, target_key, action, idempotency_key)
		VALUES ('project-delete-mutation', 'run', ?, 'skill', 'example', 'delete', 'project-delete-mutation')`, project.ID); err != nil {
		t.Fatal(err)
	}

	if err := projectSvc.Delete(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "README.md")); err != nil {
		t.Fatalf("user repository was not preserved: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("managed task worktree still exists: %v", err)
	}
	worktreeList := exec.Command("git", "worktree", "list", "--porcelain")
	worktreeList.Dir = repoDir
	worktreeOutput, err := worktreeList.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeOutput), worktreePath) {
		t.Fatalf("managed task worktree metadata still exists:\n%s", worktreeOutput)
	}
	branchCheck := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	branchCheck.Dir = repoDir
	if err := branchCheck.Run(); err == nil {
		t.Fatalf("managed task branch %q still exists", branch)
	}
	var mutationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_config_mutations WHERE project_id = ?`, project.ID).Scan(&mutationCount); err != nil {
		t.Fatal(err)
	}
	if mutationCount != 0 {
		t.Fatalf("project mutation rows retained after deletion: %d", mutationCount)
	}
}

func TestProjectService_DeleteCleansManagedCloneAndPreservesUserOwnedGitHubCheckout(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil))
	managedRoot := filepath.Join(t.TempDir(), "managed-repositories")
	projectSvc.SetManagedProjectRepoResolver(NewGitHubService(repository.NewSettingsRepo(db), "", "", "", managedRoot))

	managed := &models.Project{Name: "Managed clone", RepoURL: "https://github.com/example/managed"}
	if err := projectRepo.Create(ctx, managed); err != nil {
		t.Fatal(err)
	}
	managed.RepoPath = filepath.Join(managedRoot, managed.ID)
	if err := os.MkdirAll(managed.RepoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed.RepoPath, "managed.txt"), []byte("managed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := projectRepo.Update(ctx, managed); err != nil {
		t.Fatal(err)
	}
	if err := projectSvc.Delete(ctx, managed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed.RepoPath); !os.IsNotExist(err) {
		t.Fatalf("managed clone still exists after project deletion: %v", err)
	}

	unownedPath := filepath.Join(t.TempDir(), "user-github-checkout")
	if err := os.MkdirAll(unownedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = unownedPath
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(unownedPath, "README.md"), []byte("user-owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "Initial commit")
	unowned := &models.Project{Name: "Unowned GitHub path", RepoPath: unownedPath, RepoURL: "https://github.com/example/unowned"}
	if err := projectRepo.Create(ctx, unowned); err != nil {
		t.Fatal(err)
	}
	unownedTask := &models.Task{ProjectID: unowned.ID, Title: "User checkout worktree", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, unownedTask); err != nil {
		t.Fatal(err)
	}
	unownedWorktree := filepath.Join(unownedPath, ".worktrees", "task_"+unownedTask.ID)
	if err := os.MkdirAll(filepath.Dir(unownedWorktree), 0o755); err != nil {
		t.Fatal(err)
	}
	unownedBranch := "task/" + unownedTask.ID[:8] + "-cleanup"
	runGit("worktree", "add", "-b", unownedBranch, unownedWorktree)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET worktree_path = ?, worktree_branch = ? WHERE id = ?`, unownedWorktree, unownedBranch, unownedTask.ID); err != nil {
		t.Fatal(err)
	}
	if err := projectSvc.Delete(ctx, unowned.ID); err != nil {
		t.Fatalf("deleting project with user-owned GitHub checkout: %v", err)
	}
	if stored, err := projectRepo.GetByID(ctx, unowned.ID); err != nil || stored != nil {
		t.Fatalf("project using user-owned GitHub checkout was retained: project=%#v err=%v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(unownedPath, "README.md")); err != nil {
		t.Fatalf("user-owned GitHub checkout was removed: %v", err)
	}
	if _, err := os.Stat(unownedWorktree); !os.IsNotExist(err) {
		t.Fatalf("OpenVibely-owned worktree in user GitHub checkout was retained: %v", err)
	}
	branchCheck := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+unownedBranch)
	branchCheck.Dir = unownedPath
	if err := branchCheck.Run(); err == nil {
		t.Fatalf("OpenVibely-owned branch %q in user GitHub checkout was retained", unownedBranch)
	}
}

func TestProjectService_DeleteFilesystemStagingFailurePreservesRelationalData(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))

	project := &models.Project{Name: "Filesystem rollback"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Retained task", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	invalidAttachment := filepath.Join(t.TempDir(), "attachment-is-directory")
	if err := os.MkdirAll(invalidAttachment, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := attachmentRepo.Create(ctx, &models.Attachment{TaskID: task.ID, FileName: "invalid.txt", FilePath: invalidAttachment, MediaType: "text/plain", FileSize: 1}); err != nil {
		t.Fatal(err)
	}

	if err := projectSvc.Delete(ctx, project.ID); err == nil || !strings.Contains(err.Error(), "unexpected file type") {
		t.Fatalf("filesystem staging error = %v", err)
	}
	if stored, err := projectRepo.GetByID(ctx, project.ID); err != nil || stored == nil {
		t.Fatalf("project after staging failure = %#v err=%v", stored, err)
	}
	if stored, err := taskRepo.GetByID(ctx, task.ID); err != nil || stored == nil {
		t.Fatalf("task after staging failure = %#v err=%v", stored, err)
	}
	if _, err := os.Stat(invalidAttachment); err != nil {
		t.Fatalf("attachment source changed after staging failure: %v", err)
	}
}

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

	promoted := make(chan string, 1)
	svc.SetQueuedTaskThreadPromoter(func(taskID string) { promoted <- taskID })

	// Execute — should not call the LLM at all since repo is missing
	_, execErr := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if execErr == nil {
		t.Error("expected error from ExecuteTaskWithAgent when repo path is missing")
	}

	select {
	case gotTaskID := <-promoted:
		if gotTaskID != task.ID {
			t.Fatalf("expected promoted task ID %q, got %q", task.ID, gotTaskID)
		}
	default:
		t.Fatal("expected queued task-thread promoter after missing repo path failure")
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

func TestProjectService_ValidatesProjectWorkerLimitAgainstGlobal(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	svc := NewProjectService(projectRepo)
	svc.SetWorkerRepo(workerRepo)

	if err := workerRepo.SetMaxWorkers(ctx, 25); err != nil {
		t.Fatalf("SetMaxWorkers(25): %v", err)
	}

	tooHigh := 26
	rejected := &models.Project{Name: "Over Global", MaxWorkers: &tooHigh}
	if err := svc.Create(ctx, rejected); err == nil {
		t.Fatal("Create accepted a project limit above the finite global limit")
	}
	if rejected.ID != "" {
		t.Fatalf("rejected project received an ID %q, suggesting it was persisted", rejected.ID)
	}

	equal := 25
	equalProject := &models.Project{Name: "At Global", MaxWorkers: &equal}
	if err := svc.Create(ctx, equalProject); err != nil {
		t.Fatalf("Create at global limit: %v", err)
	}

	below := 20
	belowProject := &models.Project{Name: "Below Global", MaxWorkers: &below}
	if err := svc.Create(ctx, belowProject); err != nil {
		t.Fatalf("Create below global limit: %v", err)
	}

	zero := 0
	zeroProject := &models.Project{Name: "Inherited Limit", MaxWorkers: &zero}
	if err := svc.Create(ctx, zeroProject); err != nil {
		t.Fatalf("Create with zero project limit: %v", err)
	}
	storedZero, err := projectRepo.GetByID(ctx, zeroProject.ID)
	if err != nil {
		t.Fatalf("GetByID zero project: %v", err)
	}
	if storedZero.MaxWorkers != nil {
		t.Fatalf("zero project limit persisted as %v, want nil", storedZero.MaxWorkers)
	}

	if err := workerRepo.SetMaxWorkers(ctx, 10); err != nil {
		t.Fatalf("SetMaxWorkers(10): %v", err)
	}
	stale := *belowProject
	stale.Name = "Below Global After Reduction"
	if err := svc.Update(ctx, &stale); err != nil {
		t.Fatalf("Update with unchanged stale project cap after global reduction: %v", err)
	}
	updated, err := projectRepo.GetByID(ctx, belowProject.ID)
	if err != nil {
		t.Fatalf("GetByID after global reduction: %v", err)
	}
	if updated.MaxWorkers == nil || *updated.MaxWorkers != below {
		t.Fatalf("global reduction changed existing project cap to %v, want %d", updated.MaxWorkers, below)
	}

	invalid := *updated
	invalid.MaxWorkers = &tooHigh
	if err := svc.Update(ctx, &invalid); err == nil {
		t.Fatal("Update accepted a changed project limit above the finite global limit")
	}
	unchanged, err := projectRepo.GetByID(ctx, belowProject.ID)
	if err != nil {
		t.Fatalf("GetByID after rejected update: %v", err)
	}
	if unchanged.MaxWorkers == nil || *unchanged.MaxWorkers != below {
		t.Fatalf("rejected update changed project cap to %v, want %d", unchanged.MaxWorkers, below)
	}

	cleared := *updated
	cleared.MaxWorkers = nil
	if err := svc.Update(ctx, &cleared); err != nil {
		t.Fatalf("clear project limit after global reduction: %v", err)
	}

	if err := workerRepo.SetMaxWorkers(ctx, 0); err != nil {
		t.Fatalf("SetMaxWorkers(0): %v", err)
	}
	high := 100
	unlimitedProject := &models.Project{Name: "Unlimited Global High Project", MaxWorkers: &high}
	if err := svc.Create(ctx, unlimitedProject); err != nil {
		t.Fatalf("Create high project under unlimited global: %v", err)
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
