package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskRepoListWithWorktreesUsesCleanupProjection(t *testing.T) {
	if got, want := worktreeCleanupTaskSelectColumns, "id, project_id, status, worktree_path, worktree_branch, merge_target_branch, merge_status"; got != want {
		t.Fatalf("cleanup projection changed: got %q, want %q", got, want)
	}
	for _, forbidden := range []string{"prompt", "chain_config", "swarm_config", "agent_id", "title", "created_at", "updated_at"} {
		if projectionContainsColumn(worktreeCleanupTaskSelectColumns, forbidden) {
			t.Fatalf("cleanup projection must not select unused full task column %q: %s", forbidden, worktreeCleanupTaskSelectColumns)
		}
	}

	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	withTarget := createWorktreeProjectionTask(t, ctx, repo, "with target", models.StatusCompleted, "/tmp/worktree-with-target", "task/with-target", "main", models.MergeStatusPending)
	withoutTarget := createWorktreeProjectionTask(t, ctx, repo, "without target", models.StatusFailed, "/tmp/worktree-without-target", "task/without-target", "", models.MergeStatusMerged)
	_ = createWorktreeProjectionTask(t, ctx, repo, "without worktree", models.StatusCompleted, "", "", "main", models.MergeStatusPending)

	tasks, err := repo.ListWithWorktrees(ctx)
	if err != nil {
		t.Fatalf("ListWithWorktrees: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks with worktrees, got %d", len(tasks))
	}

	got := map[string]models.Task{}
	for _, task := range tasks {
		got[task.ID] = task
		if task.Prompt != "" || task.ChainConfig != "" || task.SwarmConfig != "" {
			t.Fatalf("cleanup projection loaded unused payloads for task %s: prompt=%d chain=%d swarm=%d", task.ID, len(task.Prompt), len(task.ChainConfig), len(task.SwarmConfig))
		}
	}

	assertWorktreeCleanupTask(t, got[withTarget.ID], withTarget.ID, "default", models.StatusCompleted, "/tmp/worktree-with-target", "task/with-target", "main", models.MergeStatusPending)
	assertWorktreeCleanupTask(t, got[withoutTarget.ID], withoutTarget.ID, "default", models.StatusFailed, "/tmp/worktree-without-target", "task/without-target", "", models.MergeStatusMerged)
}

func BenchmarkTaskRepoListWithWorktreesCleanupProjection(b *testing.B) {
	fixtures := []struct {
		name      string
		taskCount int
		blobSize  int
	}{
		{name: "Large300x32KiBPromptAndConfig", taskCount: 300, blobSize: 32 * 1024},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db := newWorktreeCleanupBenchmarkDB(b, fixture.taskCount, fixture.blobSize)
			defer db.Close()
			repo := NewTaskRepo(db, nil)

			b.Run("Repository", func(b *testing.B) {
				benchmarkListWithWorktreesCleanupProjection(b, repo)
			})
		})
	}
}

func projectionContainsColumn(projection, column string) bool {
	for _, selected := range strings.Split(projection, ",") {
		selected = strings.TrimSpace(selected)
		selected = strings.TrimPrefix(selected, "t.")
		if selected == column {
			return true
		}
	}
	return false
}

func createWorktreeProjectionTask(t *testing.T, ctx context.Context, repo *TaskRepo, title string, status models.TaskStatus, worktreePath, worktreeBranch, mergeTargetBranch string, mergeStatus models.MergeStatus) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID:         "default",
		Title:             title,
		Category:          models.CategoryActive,
		Priority:          1,
		Status:            status,
		Prompt:            strings.Repeat("prompt", 1024),
		ChainConfig:       `{"enabled":true,"child_prompt_prefix":"` + strings.Repeat("chain", 256) + `"}`,
		SwarmConfig:       `{"planner_notes":"` + strings.Repeat("swarm", 256) + `"}`,
		WorktreePath:      worktreePath,
		WorktreeBranch:    worktreeBranch,
		MergeTargetBranch: mergeTargetBranch,
		MergeStatus:       mergeStatus,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create %q: %v", title, err)
	}
	return task
}

func assertWorktreeCleanupTask(t *testing.T, got models.Task, id, projectID string, status models.TaskStatus, worktreePath, worktreeBranch, mergeTargetBranch string, mergeStatus models.MergeStatus) {
	t.Helper()
	if got.ID != id || got.ProjectID != projectID || got.Status != status || got.WorktreePath != worktreePath || got.WorktreeBranch != worktreeBranch || got.MergeTargetBranch != mergeTargetBranch || got.MergeStatus != mergeStatus {
		t.Fatalf("unexpected cleanup task projection: got {id:%q project:%q status:%q path:%q branch:%q target:%q merge:%q}", got.ID, got.ProjectID, got.Status, got.WorktreePath, got.WorktreeBranch, got.MergeTargetBranch, got.MergeStatus)
	}
}

func benchmarkListWithWorktreesCleanupProjection(b *testing.B, repo *TaskRepo) {
	b.Helper()
	ctx := context.Background()
	var payloadBytes int64

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks, err := repo.ListWithWorktrees(ctx)
		if err != nil {
			b.Fatalf("list cleanup worktrees: %v", err)
		}
		payloadBytes = cleanupProjectionPayloadBytes(tasks)
	}
	b.StopTimer()
	b.ReportMetric(float64(payloadBytes), "payload_bytes/op")
}

func cleanupProjectionPayloadBytes(tasks []models.Task) int64 {
	var total int64
	for _, task := range tasks {
		total += int64(len(task.ID) + len(task.ProjectID) + len(task.Title) + len(task.Category) + len(task.Status) + len(task.Prompt) + len(task.Tag) + len(task.ChainConfig) + len(task.SwarmRole) + len(task.SwarmStatus) + len(task.SwarmConfig) + len(task.WorktreePath) + len(task.WorktreeBranch) + len(task.MergeTargetBranch) + len(task.MergeStatus) + len(task.BaseBranch) + len(task.BaseCommitSHA) + len(task.CreatedVia))
		if task.AgentID != nil {
			total += int64(len(*task.AgentID))
		}
		if task.AgentDefinitionID != nil {
			total += int64(len(*task.AgentDefinitionID))
		}
		if task.ParentTaskID != nil {
			total += int64(len(*task.ParentTaskID))
		}
	}
	return total
}

func newWorktreeCleanupBenchmarkDB(b *testing.B, taskCount, blobSize int) *sql.DB {
	b.Helper()
	db := testutil.NewTestDB(b)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	prompt := strings.Repeat("p", blobSize)
	chainConfig := `{"enabled":true,"child_prompt_prefix":"` + strings.Repeat("c", blobSize) + `"}`
	swarmConfig := `{"planner_notes":"` + strings.Repeat("s", blobSize) + `"}`

	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID:         "default",
			Title:             fmt.Sprintf("Benchmark worktree task %03d", i),
			Category:          models.CategoryActive,
			Priority:          (i % 4) + 1,
			Status:            models.StatusCompleted,
			Prompt:            prompt,
			Tag:               models.TagFeature,
			ChainConfig:       chainConfig,
			SwarmConfig:       swarmConfig,
			WorktreePath:      fmt.Sprintf("/tmp/openvibely-benchmark-worktree-%03d", i),
			WorktreeBranch:    fmt.Sprintf("task/benchmark-worktree-%03d", i),
			MergeTargetBranch: "main",
			MergeStatus:       models.MergeStatusPending,
		}
		if err := repo.Create(ctx, task); err != nil {
			db.Close()
			b.Fatalf("create benchmark task %d: %v", i, err)
		}
	}
	return db
}
