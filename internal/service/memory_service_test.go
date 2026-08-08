package service

import (
	"context"
	"os"
	"testing"

	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func newTestMemoryResolver(t *testing.T) (*memory.PathResolver, string) {
	t.Helper()
	dir := t.TempDir()
	resolver, err := memory.NewPathResolver("", "")
	if err != nil {
		t.Fatalf("NewPathResolver: %v", err)
	}
	if err := resolver.SetProjectDirOverride("test-project", dir); err != nil {
		t.Fatalf("SetProjectDirOverride: %v", err)
	}
	return resolver, dir
}

// TestNewMemoryService verifies the constructor returns a non-nil service.
func TestNewMemoryService(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)
	if svc == nil {
		t.Fatal("expected non-nil MemoryService")
	}
}

// TestMemoryService_SetLifecycleRepo verifies nil safety on a nil service receiver.
func TestMemoryService_SetLifecycleRepo_NilSafe(t *testing.T) {
	var svc *MemoryService
	// Should not panic
	svc.SetLifecycleRepo(nil)
}

// TestMemoryService_SetLifecycleRepo verifies the repo is stored.
func TestMemoryService_SetLifecycleRepo_Stores(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	db := testutil.NewTestDB(t)
	repo := repository.NewLifecycleRepo(db)
	svc.SetLifecycleRepo(repo)

	if svc.lifecycleRepo != repo {
		t.Error("expected lifecycleRepo to be set")
	}
}

// TestMemoryService_EnsureProject_NilTaskRepo verifies that EnsureProject succeeds
// when taskRepo/scheduleRepo/agentRepo are nil (consolidation-task path is skipped).
// A directory override is pre-configured so the filesystem path succeeds.
func TestMemoryService_EnsureProject_NilTaskRepo(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	err := svc.EnsureProject(context.Background(), "test-project")
	if err != nil {
		t.Fatalf("EnsureProject with nil repos failed: %v", err)
	}
}

// TestMemoryService_EnsureProject_NilTaskRepo_Idempotent verifies double-call safety.
func TestMemoryService_EnsureProject_NilTaskRepo_Idempotent(t *testing.T) {
	resolver, _ := newTestMemoryResolver(t)
	store := memory.NewFileStore(resolver)
	svc := NewMemoryService(nil, nil, nil, nil, store, resolver)

	for i := 0; i < 3; i++ {
		if err := svc.EnsureProject(context.Background(), "test-project"); err != nil {
			t.Fatalf("EnsureProject call %d failed: %v", i+1, err)
		}
	}
}

// TestMemoryService_EnsureProject_NoRepoPath verifies that EnsureProject returns an
// error when the project has no local repo_path configured.
func TestMemoryService_EnsureProject_NoRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	// Create a project with no repo_path
	p := &models.Project{Name: "No-Path Project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	err := svc.EnsureProject(context.Background(), p.ID)
	// Should fail because no local repo_path is set
	if err == nil {
		t.Fatal("expected error for project with no repo_path, got nil")
	}
}

// TestMemoryService_EnsureProject_WithRepoPath exercises the full EnsureProject path:
// memory directory setup, agent reconciliation, and consolidation task/schedule creation.
func TestMemoryService_EnsureProject_WithRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	// Create a project with a real temp dir as the repo path
	repoPath := t.TempDir()
	p := &models.Project{Name: "Repo-Path Project", RepoPath: repoPath}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject failed: %v", err)
	}

	// Verify the memory directory was created
	expectedDir := repoPath + "/.openvibely/memories"
	if _, err := os.Stat(expectedDir); err != nil {
		t.Errorf("expected memory dir %s to exist: %v", expectedDir, err)
	}

	// Verify the consolidation task was created
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil {
		t.Fatalf("GetByProjectAndTitle: %v", err)
	}
	if task == nil {
		t.Fatal("expected consolidation task to be created")
	}
	if task.Category != models.CategoryScheduled {
		t.Errorf("expected category scheduled, got %q", task.Category)
	}

	// Verify the schedule was created
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Errorf("expected daily schedule, got %q", schedules[0].RepeatType)
	}
	if !schedules[0].ClearContextOnStart {
		t.Fatal("expected memory consolidation schedule to clear context on start")
	}
}

func TestMemoryService_EnsureProject_RepairsMaintenanceScheduleClearContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	p := &models.Project{Name: "Repair Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject initial: %v", err)
	}
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil || task == nil {
		t.Fatalf("expected consolidation task: %v", err)
	}
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask initial: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %d", len(schedules))
	}
	originalRunAt := schedules[0].RunAt
	originalNextRun := schedules[0].NextRun
	if err := scheduleRepo.UpdateClearContextOnStart(ctx, schedules[0].ID, task.ID, false); err != nil {
		t.Fatalf("make schedule stale: %v", err)
	}

	if err := svc.EnsureProject(ctx, p.ID); err != nil {
		t.Fatalf("EnsureProject repair: %v", err)
	}
	repaired, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask repaired: %v", err)
	}
	if len(repaired) != 1 {
		t.Fatalf("expected one repaired schedule, got %d", len(repaired))
	}
	if !repaired[0].ClearContextOnStart {
		t.Fatal("expected stale memory consolidation schedule clear-context flag to be repaired")
	}
	if !repaired[0].RunAt.Equal(originalRunAt) {
		t.Fatalf("repair changed run_at: got %s want %s", repaired[0].RunAt, originalRunAt)
	}
	if originalNextRun == nil || repaired[0].NextRun == nil || !repaired[0].NextRun.Equal(*originalNextRun) {
		t.Fatalf("repair changed next_run: got %v want %v", repaired[0].NextRun, originalNextRun)
	}
}

// TestMemoryService_EnsureProject_WithRepoPath_Idempotent verifies that calling
// EnsureProject multiple times does not create duplicate tasks or schedules.
func TestMemoryService_EnsureProject_WithRepoPath_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)

	resolver, _ := memory.NewPathResolver("", "")
	store := memory.NewFileStore(resolver)

	svc := NewMemoryService(taskRepo, scheduleRepo, agentRepo, projectRepo, store, resolver)

	repoPath := t.TempDir()
	p := &models.Project{Name: "Idempotent Project", RepoPath: repoPath}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := svc.EnsureProject(ctx, p.ID); err != nil {
			t.Fatalf("EnsureProject call %d failed: %v", i+1, err)
		}
	}

	// Verify exactly one consolidation task and one schedule
	task, err := taskRepo.GetByProjectAndTitle(ctx, p.ID, memoryConsolidationTaskTitle)
	if err != nil || task == nil {
		t.Fatalf("expected consolidation task: %v", err)
	}
	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(schedules) != 1 {
		t.Errorf("expected exactly 1 schedule after 3 calls, got %d", len(schedules))
	}
}
