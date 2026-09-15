package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskPullRequestRepoGetByIssueNumber(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskPullRequestRepo(db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES ('proj-pr-issue', 'PR Issue Project', '', '/tmp/repo', 'https://github.com/openvibely/openvibely')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, title, category, status) VALUES ('task-pr-issue', 'proj-pr-issue', 'Task', 'active', 'pending')`); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	issueNumber := 123
	record := &models.TaskPullRequest{
		TaskID:      "task-pr-issue",
		PRNumber:    456,
		PRURL:       "https://github.com/openvibely/openvibely/pull/456",
		PRState:     "open",
		IssueNumber: &issueNumber,
		IssueURL:    "https://github.com/openvibely/openvibely/issues/123",
	}
	if err := repo.Upsert(ctx, record); err != nil {
		t.Fatalf("upsert task pull request: %v", err)
	}

	got, err := repo.GetByIssueNumber(ctx, issueNumber)
	if err != nil {
		t.Fatalf("get by issue number: %v", err)
	}
	if got == nil {
		t.Fatal("expected task pull request for issue number")
	}
	if got.TaskID != "task-pr-issue" || got.PRNumber != 456 {
		t.Fatalf("unexpected record: %#v", got)
	}
	if got.IssueNumber == nil || *got.IssueNumber != issueNumber {
		t.Fatalf("expected issue number %d, got %#v", issueNumber, got.IssueNumber)
	}
	if got.IssueURL != "https://github.com/openvibely/openvibely/issues/123" {
		t.Fatalf("unexpected issue URL: %q", got.IssueURL)
	}
}

func TestTaskPullRequestRepoSetNeedsRepublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskPullRequestRepo(db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES ('proj-pr-publish', 'PR Publish Project', '', '/tmp/repo', 'https://github.com/openvibely/openvibely')`); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, title, category, status) VALUES ('task-pr-publish', 'proj-pr-publish', 'Task', 'active', 'pending')`); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	record := &models.TaskPullRequest{TaskID: "task-pr-publish", PRNumber: 456, PRURL: "https://github.com/openvibely/openvibely/pull/456", PRState: "open"}
	if err := repo.Upsert(ctx, record); err != nil {
		t.Fatalf("upsert task pull request: %v", err)
	}
	initialUpdatedAt := record.UpdatedAt
	if err := repo.SetNeedsRepublish(ctx, record.TaskID, true); err != nil {
		t.Fatalf("set publication requirement: %v", err)
	}
	got, err := repo.GetByTaskID(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("get task pull request: %v", err)
	}
	if got == nil || !got.NeedsRepublish {
		t.Fatalf("expected durable publication requirement, got %#v", got)
	}
	if !got.UpdatedAt.Equal(initialUpdatedAt) {
		t.Fatalf("publication marker changed external-state refresh timestamp: got %s want %s", got.UpdatedAt, initialUpdatedAt)
	}

	// A stale writer must not erase a publication requirement it did not own.
	if err := repo.Upsert(ctx, record); err != nil {
		t.Fatalf("upsert stale task pull request: %v", err)
	}
	got, err = repo.GetByTaskID(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("get task pull request after stale upsert: %v", err)
	}
	if got == nil || !got.NeedsRepublish {
		t.Fatalf("stale upsert erased publication requirement: %#v", got)
	}
	if err := repo.SetNeedsRepublish(ctx, record.TaskID, false); err != nil {
		t.Fatalf("clear publication requirement: %v", err)
	}
	got, err = repo.GetByTaskID(ctx, record.TaskID)
	if err != nil {
		t.Fatalf("get task pull request after clear: %v", err)
	}
	if got == nil || got.NeedsRepublish {
		t.Fatalf("expected explicit publication clear, got %#v", got)
	}
}
