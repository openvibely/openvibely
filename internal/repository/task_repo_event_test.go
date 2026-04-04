package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTaskRepo_ClaimTask_PublishesEvent(t *testing.T) {
	db := testutil.NewTestDB(t)
	broadcaster := events.NewBroadcaster()
	repo := NewTaskRepo(db, broadcaster)
	ctx := context.Background()

	// Subscribe to events before claiming
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Create a pending task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Event Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Claim the task (should trigger event)
	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if !claimed {
		t.Error("expected claim to succeed")
	}

	// Wait for event with timeout
	select {
	case event := <-sub:
		if event.Type != events.TaskStatusChanged {
			t.Errorf("expected TaskStatusChanged event, got %s", event.Type)
		}
		if event.TaskID != task.ID {
			t.Errorf("expected task_id %s, got %s", task.ID, event.TaskID)
		}
		if event.TaskName != "Event Test" {
			t.Errorf("expected task_name 'Event Test', got %s", event.TaskName)
		}
		if event.ProjectID != "default" {
			t.Errorf("expected project_id 'default', got %s", event.ProjectID)
		}
		if event.Status != string(models.StatusRunning) {
			t.Errorf("expected status 'running', got %s", event.Status)
		}
		if event.OldStatus != string(models.StatusPending) {
			t.Errorf("expected old_status 'pending', got %s", event.OldStatus)
		}
		if event.Category != string(models.CategoryActive) {
			t.Errorf("expected category 'active', got %s", event.Category)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event - event was not published")
	}
}

func TestTaskRepo_UpdateStatus_PublishesEvent(t *testing.T) {
	db := testutil.NewTestDB(t)
	broadcaster := events.NewBroadcaster()
	repo := NewTaskRepo(db, broadcaster)
	ctx := context.Background()

	// Subscribe to events
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Create a running task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Status Update Test",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update status to completed (should trigger event)
	if err := repo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Wait for event with timeout
	select {
	case event := <-sub:
		if event.Type != events.TaskStatusChanged {
			t.Errorf("expected TaskStatusChanged event, got %s", event.Type)
		}
		if event.TaskID != task.ID {
			t.Errorf("expected task_id %s, got %s", task.ID, event.TaskID)
		}
		if event.TaskName != "Status Update Test" {
			t.Errorf("expected task_name 'Status Update Test', got %s", event.TaskName)
		}
		if event.ProjectID != "default" {
			t.Errorf("expected project_id 'default', got %s", event.ProjectID)
		}
		if event.Status != string(models.StatusCompleted) {
			t.Errorf("expected status 'completed', got %s", event.Status)
		}
		if event.OldStatus != string(models.StatusRunning) {
			t.Errorf("expected old_status 'running', got %s", event.OldStatus)
		}
		if event.Category != string(models.CategoryActive) {
			t.Errorf("expected category 'active', got %s", event.Category)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event - event was not published")
	}
}

func TestTaskRepo_UpdateStatus_ChatTaskIncludesCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	broadcaster := events.NewBroadcaster()
	repo := NewTaskRepo(db, broadcaster)
	ctx := context.Background()

	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Create a chat task (these should not trigger toast notifications)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat Message Test",
		Category:  models.CategoryChat,
		Status:    models.StatusRunning,
		Prompt:    "test chat",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update status to completed
	if err := repo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// Verify event includes "chat" category so frontend can filter it
	select {
	case event := <-sub:
		if event.Type != events.TaskStatusChanged {
			t.Errorf("expected TaskStatusChanged event, got %s", event.Type)
		}
		if event.Category != string(models.CategoryChat) {
			t.Errorf("expected category 'chat', got %q — frontend relies on this to suppress toast notifications for chat messages", event.Category)
		}
		if event.Status != string(models.StatusCompleted) {
			t.Errorf("expected status 'completed', got %s", event.Status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event")
	}
}

func TestTaskRepo_UpdateCategory_PublishesEvent(t *testing.T) {
	db := testutil.NewTestDB(t)
	broadcaster := events.NewBroadcaster()
	repo := NewTaskRepo(db, broadcaster)
	ctx := context.Background()

	// Subscribe to events
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	// Create an active task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Category Update Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update category to backlog (should trigger event)
	if err := repo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	// Wait for event with timeout
	select {
	case event := <-sub:
		if event.Type != events.TaskCategoryChanged {
			t.Errorf("expected TaskCategoryChanged event, got %s", event.Type)
		}
		if event.TaskID != task.ID {
			t.Errorf("expected task_id %s, got %s", task.ID, event.TaskID)
		}
		if event.TaskName != "Category Update Test" {
			t.Errorf("expected task_name 'Category Update Test', got %s", event.TaskName)
		}
		if event.ProjectID != "default" {
			t.Errorf("expected project_id 'default', got %s", event.ProjectID)
		}
		if event.Category != string(models.CategoryBacklog) {
			t.Errorf("expected category 'backlog', got %s", event.Category)
		}
		if event.OldCategory != string(models.CategoryActive) {
			t.Errorf("expected old_category 'active', got %s", event.OldCategory)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout waiting for event - event was not published")
	}
}
