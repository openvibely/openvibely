package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func decodeListTasksResult(t *testing.T, raw string) taskDiscoveryResult {
	t.Helper()
	var result taskDiscoveryResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal list_tasks result: %v (raw=%s)", err, raw)
	}
	return result
}

func TestExecuteListTasksTool(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	other := &models.Project{Name: "List Tasks Other Project"}
	if err := projectRepo.Create(ctx, other); err != nil {
		t.Fatalf("create other project: %v", err)
	}

	mk := func(projectID, title string, category models.TaskCategory, status models.TaskStatus) *models.Task {
		task := &models.Task{ProjectID: projectID, Title: title, Category: category, Status: status, Prompt: "p"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		if err := taskRepo.UpdateStatus(ctx, task.ID, status); err != nil {
			t.Fatalf("status %q: %v", title, err)
		}
		return task
	}

	target := mk("default", "Implement issue 25", models.CategoryActive, models.StatusPending)
	mk("default", "Implement issue 25 followup", models.CategoryBacklog, models.StatusPending)
	mk("default", "Unrelated task", models.CategoryActive, models.StatusRunning)
	mk("default", "Implement issue 25 chat", models.CategoryChat, models.StatusCompleted)
	mk(other.ID, "Implement issue 25 elsewhere", models.CategoryActive, models.StatusPending)

	// Missing project context fails loudly.
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for empty project context")
	}

	// Partial title query returns compact identity sufficient for exact-target actions.
	out, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"query":"issue 25"}`))
	if err != nil {
		t.Fatalf("list_tasks query: %v", err)
	}
	result := decodeListTasksResult(t, out)
	if !result.OK {
		t.Fatal("expected ok=true")
	}
	if result.Total != 2 || result.Count != 2 {
		t.Fatalf("expected 2 matches (chat + other project excluded), got total=%d count=%d", result.Total, result.Count)
	}
	var found bool
	for _, task := range result.Tasks {
		if task.Category == string(models.CategoryChat) {
			t.Fatalf("chat row leaked: %q", task.Title)
		}
		if task.TaskID == target.ID {
			found = true
			if task.Title != target.Title || task.Category != string(models.CategoryActive) || task.Status != string(models.StatusPending) {
				t.Fatalf("compact identity mismatch: %+v", task)
			}
			if task.UpdatedAt == "" {
				t.Fatal("expected updated_at in compact summary")
			}
		}
	}
	if !found {
		t.Fatalf("expected target task %q in results", target.ID)
	}

	// Category + status filters.
	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"category":"active","status":"running"}`))
	if err != nil {
		t.Fatalf("list_tasks filter: %v", err)
	}
	filtered := decodeListTasksResult(t, out)
	if filtered.Total != 1 || len(filtered.Tasks) != 1 || filtered.Tasks[0].Title != "Unrelated task" {
		t.Fatalf("expected single running active task, got %+v", filtered)
	}

	// Invalid category/status are rejected.
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"category":"bogus"}`)); err == nil {
		t.Fatal("expected error for invalid category")
	}
	if _, err := ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"status":"bogus"}`)); err == nil {
		t.Fatal("expected error for invalid status")
	}

	// Pagination contract: limit + offset + has_more.
	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":1,"offset":0}`))
	if err != nil {
		t.Fatalf("list_tasks page1: %v", err)
	}
	page1 := decodeListTasksResult(t, out)
	if page1.Total != 3 || page1.Count != 1 || page1.Limit != 1 || !page1.HasMore {
		t.Fatalf("unexpected page1 contract: %+v", page1)
	}

	out, err = ExecuteListTasksTool(ctx, taskRepo, "default", json.RawMessage(`{"limit":1,"offset":2}`))
	if err != nil {
		t.Fatalf("list_tasks page3: %v", err)
	}
	page3 := decodeListTasksResult(t, out)
	if page3.Offset != 2 || page3.Count != 1 || page3.HasMore {
		t.Fatalf("unexpected final page contract: %+v", page3)
	}
}
