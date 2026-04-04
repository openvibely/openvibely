package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestReviewCommentRepo_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewReviewCommentRepo(db)
	taskRepo := NewTaskRepo(db, events.NewBroadcaster())
	ctx := context.Background()

	// Create a task to attach comments to
	task := &models.Task{
		ProjectID: "default",
		Title:     "Review Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// List should be empty initially
	comments, err := repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}

	// Count should be 0
	count, err := repo.CountByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountByTask: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}

	// Create a comment
	c1 := &models.ReviewComment{
		TaskID:      task.ID,
		FilePath:    "main.go",
		LineNumber:  42,
		LineType:    "new",
		CommentText: "This function should handle errors",
		ReviewedBy:  "user",
	}
	if err := repo.Create(ctx, c1); err != nil {
		t.Fatalf("Create comment: %v", err)
	}
	if c1.ID == "" {
		t.Error("expected ID to be populated")
	}
	if c1.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be populated")
	}

	// Create another comment on a different file
	c2 := &models.ReviewComment{
		TaskID:      task.ID,
		FilePath:    "handler.go",
		LineNumber:  10,
		LineType:    "old",
		CommentText: "Remove deprecated code",
		ReviewedBy:  "user",
	}
	if err := repo.Create(ctx, c2); err != nil {
		t.Fatalf("Create comment 2: %v", err)
	}

	// Create a third comment on the same file as c1 but different line
	c3 := &models.ReviewComment{
		TaskID:      task.ID,
		FilePath:    "main.go",
		LineNumber:  50,
		LineType:    "new",
		CommentText: "Add logging here",
		ReviewedBy:  "user",
	}
	if err := repo.Create(ctx, c3); err != nil {
		t.Fatalf("Create comment 3: %v", err)
	}

	// List should return all 3, ordered by file_path ASC, line_number ASC
	comments, err = repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(comments) != 3 {
		t.Fatalf("expected 3 comments, got %d", len(comments))
	}
	// handler.go should come before main.go
	if comments[0].FilePath != "handler.go" {
		t.Errorf("expected first comment on handler.go, got %s", comments[0].FilePath)
	}
	if comments[1].FilePath != "main.go" || comments[1].LineNumber != 42 {
		t.Errorf("expected second comment on main.go:42, got %s:%d", comments[1].FilePath, comments[1].LineNumber)
	}
	if comments[2].FilePath != "main.go" || comments[2].LineNumber != 50 {
		t.Errorf("expected third comment on main.go:50, got %s:%d", comments[2].FilePath, comments[2].LineNumber)
	}

	// Count should be 3
	count, err = repo.CountByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountByTask: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}

	// Update comment text
	if err := repo.UpdateText(ctx, c1.ID, "Updated comment text"); err != nil {
		t.Fatalf("UpdateText: %v", err)
	}
	comments, err = repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask after update: %v", err)
	}
	if comments[1].CommentText != "Updated comment text" {
		t.Errorf("expected updated comment text, got %q", comments[1].CommentText)
	}

	// Delete a single comment
	if err := repo.Delete(ctx, c2.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	comments, err = repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask after delete: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments after delete, got %d", len(comments))
	}

	// Update non-existent should error
	if err := repo.UpdateText(ctx, "nonexistent", "nope"); err == nil {
		t.Error("expected error when updating non-existent comment")
	}

	// Delete non-existent should error
	if err := repo.Delete(ctx, "nonexistent"); err == nil {
		t.Error("expected error when deleting non-existent comment")
	}

	// Delete all by task
	if err := repo.DeleteByTask(ctx, task.ID); err != nil {
		t.Fatalf("DeleteByTask: %v", err)
	}
	count, err = repo.CountByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("CountByTask after DeleteByTask: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0 after DeleteByTask, got %d", count)
	}
}

func TestReviewCommentRepo_MultipleLineSameFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewReviewCommentRepo(db)
	taskRepo := NewTaskRepo(db, events.NewBroadcaster())
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Multi-Comment Test",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	// Add two comments to the same line (threading)
	for _, text := range []string{"First comment", "Second comment"} {
		c := &models.ReviewComment{
			TaskID:      task.ID,
			FilePath:    "app.go",
			LineNumber:  15,
			LineType:    "new",
			CommentText: text,
			ReviewedBy:  "user",
		}
		if err := repo.Create(ctx, c); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	comments, err := repo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments on same line, got %d", len(comments))
	}
	if comments[0].CommentText != "First comment" {
		t.Errorf("expected first comment first (by created_at), got %q", comments[0].CommentText)
	}
}
