package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestWorktreeDiffIsEmpty(t *testing.T) {
	repo := createTestGitRepo(t)
	target := GetCurrentBranch(repo)
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitTest(t, repo, "worktree", "add", "-b", "task/empty", worktree, target)
	check := func(want bool) {
		t.Helper()
		got, err := WorktreeDiffIsEmpty(context.Background(), repo, target, worktree)
		if err != nil || got != want {
			t.Fatalf("empty=%v err=%v want=%v", got, err, want)
		}
	}
	check(true)
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	check(false) // Untracked changes count.
	runGitTest(t, worktree, "add", "new.txt")
	check(false) // Staged changes count.
	runGitTest(t, worktree, "commit", "-m", "new")
	check(false) // Earlier committed changes count on a no-op rerun.
	runGitTest(t, worktree, "revert", "--no-edit", "HEAD")
	check(true)
	if empty, err := WorktreeDiffIsEmpty(context.Background(), repo, "missing-target", worktree); err == nil || empty {
		t.Fatal("missing target treated as confirmed empty")
	}
	if empty, err := WorktreeDiffIsEmpty(context.Background(), repo, target, filepath.Join(worktree, "missing")); err == nil || empty {
		t.Fatal("missing worktree treated as confirmed empty")
	}
	lock := worktreeLifecycleLock(repo)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := WorktreeDiffIsEmpty(ctx, repo, target, worktree); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("empty check ignored cancellation waiting for repository lock")
	}
}

func TestManagedFinalizationInvalidatesRevertedDiff(t *testing.T) {
	ctx := context.Background()
	repo := createTestGitRepo(t)
	target := GetCurrentBranch(repo)
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitTest(t, repo, "worktree", "add", "-b", "task/reverted", worktree, target)
	if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "new.txt")
	runGitTest(t, worktree, "commit", "-m", "change")
	saved := GetWorktreeDiffWithUncommitted(repo, "task/reverted", target, worktree)
	runGitTest(t, worktree, "revert", "--no-edit", "HEAD")
	env := teststores.New(t)
	project := &models.Project{Name: "Validity", RepoPath: repo}
	if err := env.Stores.Projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Reverted", Status: models.StatusRunning, Category: models.CategoryActive, WorktreePath: worktree, WorktreeBranch: "task/reverted", MergeTargetBranch: target}
	if err := env.Stores.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
	if err := env.Stores.Executions.Create(ctx, execution); err != nil {
		t.Fatal(err)
	}
	if err := env.Stores.Executions.UpdateDiffOutput(ctx, execution.ID, saved); err != nil {
		t.Fatal(err)
	}
	svc := NewLLMService(nil, env.Stores.Executions, env.Stores.Tasks, env.Stores.Projects, nil, nil)
	svc.SetWorktreeService(NewWorktreeService(env.Stores.Tasks, env.Stores.Projects, env.Stores.Settings))
	result := svc.captureWorktreeDiffAfterExecution(ctx, execution, task, repo, "done", models.LLMConfig{})
	if result.CommitError != nil || !result.Finalized {
		t.Fatalf("finalization: %+v", result)
	}
	got, err := env.Stores.Executions.GetLatestNonEmptyDiffOutput(ctx, task.ID)
	if err != nil || got != "" {
		t.Fatalf("obsolete diff remains: %q, %v", got, err)
	}
}
