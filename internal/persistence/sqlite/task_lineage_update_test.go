package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestTaskLineage_UpdateLineage verifies the UpdateLineage repo method.
func TestTaskLineage_UpdateLineage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := NewTaskRepo(db, broadcaster)
	projectRepo := NewProjectRepo(db)

	project := &models.Project{Name: "Update Lineage", Description: "test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	task := &models.Task{
		ProjectID:   project.ID,
		Title:       "Task",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		Prompt:      "prompt",
		ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Initially empty
	got, _ := taskRepo.GetByID(ctx, task.ID)
	if got.BaseBranch != "" || got.BaseCommitSHA != "" || got.LineageDepth != 0 {
		t.Fatalf("Expected empty lineage, got branch=%q sha=%q depth=%d", got.BaseBranch, got.BaseCommitSHA, got.LineageDepth)
	}

	// Update lineage
	if err := taskRepo.UpdateLineage(ctx, task.ID, "task/parent-branch", "abc123", 2); err != nil {
		t.Fatalf("UpdateLineage: %v", err)
	}

	got, _ = taskRepo.GetByID(ctx, task.ID)
	if got.BaseBranch != "task/parent-branch" {
		t.Errorf("BaseBranch: got %q, want %q", got.BaseBranch, "task/parent-branch")
	}
	if got.BaseCommitSHA != "abc123" {
		t.Errorf("BaseCommitSHA: got %q, want %q", got.BaseCommitSHA, "abc123")
	}
	if got.LineageDepth != 2 {
		t.Errorf("LineageDepth: got %d, want 2", got.LineageDepth)
	}
}
