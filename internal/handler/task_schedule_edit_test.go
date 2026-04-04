package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestHandler_GetTask_FromSchedulePage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Test Schedule Task",
		Category:    models.CategoryScheduled,
		Status:      models.StatusPending,
		Prompt:      "Original prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// HTMX request returns full page task detail content (not legacy dialog)
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify full page task detail content is returned
	if !strings.Contains(body, `id="task-detail-content"`) {
		t.Error("expected task-detail-content in response")
	}

	// Verify no legacy dialog references
	if strings.Contains(body, `task-dialog-container`) {
		t.Error("response should not reference legacy task-dialog-container")
	}
}

func TestHandler_UpdateTask_FromSchedulePage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Test Schedule Task",
		Category:    models.CategoryScheduled,
		Status:      models.StatusPending,
		Prompt:      "Original prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Updating task via HTMX now returns the full task detail content
	form := url.Values{}
	form.Add("title", "Updated Title")
	form.Add("prompt", "Updated prompt")
	form.Add("category", string(models.CategoryScheduled))
	form.Add("priority", "0")
	form.Add("tag", "")

	req := httptest.NewRequest(http.MethodPut, "/tasks/"+task.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 200 with task detail content
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify the task was actually updated
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}

	if updatedTask.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updatedTask.Title)
	}

	if updatedTask.Prompt != "Updated prompt" {
		t.Errorf("expected prompt 'Updated prompt', got %q", updatedTask.Prompt)
	}
}

func TestHandler_CreateTask_FromSchedulePage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a scheduled task via the schedule page (from=schedule)
	form := url.Values{}
	form.Add("title", "New Scheduled Task")
	form.Add("prompt", "Do something on schedule")
	form.Add("category", "scheduled")
	form.Add("priority", "2")
	form.Add("run_at", "2026-03-15T10:00")
	form.Add("repeat_type", "daily")
	form.Add("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id=default&from=schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// When from=schedule, should return schedule content (not kanban board)
	if !strings.Contains(body, "schedule-content") {
		t.Error("expected response to contain schedule-content when from=schedule")
	}
	if strings.Contains(body, "kanban-board") {
		t.Error("should NOT return kanban-board when from=schedule")
	}

	// Verify the task was created with scheduled category
	tasks, err := h.taskSvc.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	var found *models.Task
	for i := range tasks {
		if tasks[i].Title == "New Scheduled Task" {
			found = &tasks[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected scheduled task to be created")
	}
	if found.Category != models.CategoryScheduled {
		t.Errorf("expected category scheduled, got %s", found.Category)
	}

	// Verify a schedule was created for the task
	schedules, err := h.scheduleRepo.ListByTask(ctx, found.ID)
	if err != nil {
		t.Fatalf("failed to list schedules: %v", err)
	}
	if len(schedules) == 0 {
		t.Fatal("expected a schedule to be created for the task")
	}
	if schedules[0].RepeatType != models.RepeatDaily {
		t.Errorf("expected repeat type daily, got %s", schedules[0].RepeatType)
	}
}

func TestHandler_CreateTask_FromSchedulePage_NotInKanbanBoard(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a scheduled task
	form := url.Values{}
	form.Add("title", "Hidden Scheduled Task")
	form.Add("prompt", "This should not show in kanban")
	form.Add("category", "scheduled")
	form.Add("priority", "2")
	form.Add("run_at", "2026-03-15T10:00")
	form.Add("repeat_type", "once")
	form.Add("repeat_interval", "1")

	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id=default", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Request the kanban board - scheduled tasks should NOT appear
	req2 := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req2.Header.Set("HX-Request", "true")
	req2.Header.Set("HX-Target", "kanban-board")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec2.Code)
	}

	body := rec2.Body.String()

	// The scheduled task should NOT appear in the kanban board since scheduled category
	// is not in AllCategories
	if strings.Contains(body, "Hidden Scheduled Task") {
		t.Error("scheduled task should NOT appear in the kanban board")
	}

	// But the task should still exist in the DB
	tasks, err := h.taskSvc.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	found := false
	for _, task := range tasks {
		if task.Title == "Hidden Scheduled Task" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected scheduled task to exist in DB even though not in kanban")
	}
}

func TestHandler_UpdateTask_FromTasksPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Test Task",
		Category:    models.CategoryActive,
		Status:      models.StatusPending,
		Prompt:      "Original prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Simulate updating task from tasks page
	form := url.Values{}
	form.Add("title", "Updated Title")
	form.Add("description", "Updated description")
	form.Add("prompt", "Updated prompt")
	form.Add("category", string(models.CategoryActive))
	form.Add("priority", "0")
	form.Add("tag", "")

	req := httptest.NewRequest(http.MethodPut, "/tasks/"+task.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 200 with task detail content (full page, not dialog)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	// Verify the task was actually updated
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}

	if updatedTask.Title != "Updated Title" {
		t.Errorf("expected title 'Updated Title', got %q", updatedTask.Title)
	}
}
