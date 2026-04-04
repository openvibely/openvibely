package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

func worktreeFormRequest(method, path string, form url.Values) *http.Request {
	var body string
	if form != nil {
		body = form.Encode()
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req
}

func worktreeExecute(e *echo.Echo, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestHandler_UpdateTask_UnchecksAutoMerge(t *testing.T) {
	// Bug: unchecking auto_merge in the edit form didn't update the task
	// because unchecked checkboxes send no value and the handler skipped the update.
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create task with auto_merge enabled
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Auto Merge Test",
		Prompt:            "do something",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		AutoMerge:         true,
		MergeTargetBranch: "main",
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Verify auto_merge is true
	got, _ := h.taskSvc.GetByID(ctx, task.ID)
	if !got.AutoMerge {
		t.Fatal("expected auto_merge=true after create")
	}

	// Submit edit form WITHOUT auto_merge checked (simulates unchecking)
	// The hidden sentinel field auto_merge_present=1 tells the handler
	// that the edit form was submitted, so it should set auto_merge=false.
	form := url.Values{
		"title":              {"Auto Merge Test"},
		"prompt":             {"do something"},
		"category":           {"active"},
		"priority":           {"0"},
		"auto_merge_present": {"1"},
		// Note: no "auto_merge" key — this is what happens when checkbox is unchecked
	}

	req := worktreeFormRequest(http.MethodPut, "/tasks/"+task.ID, form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify auto_merge is now false
	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.AutoMerge {
		t.Error("expected auto_merge=false after unchecking, but got true")
	}
}

func TestHandler_UpdateTaskAutoMerge_Toggle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Worktree Auto-merge Toggle",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		AutoMerge:         false,
		MergeTargetBranch: "",
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Enable auto-merge via the worktree panel endpoint
	form := url.Values{
		"auto_merge":          {"on"},
		"merge_target_branch": {"develop"},
	}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/auto-merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if !updated.AutoMerge {
		t.Error("expected auto_merge=true after toggle on")
	}
	if updated.MergeTargetBranch != "develop" {
		t.Errorf("expected merge_target_branch=develop, got %q", updated.MergeTargetBranch)
	}
}

func TestHandler_GetTaskWorktreeInfo(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Worktree Info Task",
		Prompt:    "test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Task without worktree - should show "no worktree" message
	req := worktreeFormRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree", nil)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No worktree created") {
		t.Error("expected 'No worktree created' message for task without worktree")
	}

	// Set worktree info manually
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, "/tmp/wt", "task/abc-test"); err != nil {
		t.Fatal(err)
	}
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
		t.Fatal(err)
	}

	// Task with worktree - should show branch info
	req = worktreeFormRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree", nil)
	req.Header.Set("HX-Request", "true")
	rec = worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "task/abc-test") {
		t.Error("expected branch name in response")
	}
	if !strings.Contains(body, "Pending Merge") {
		t.Error("expected merge status badge")
	}
}

func TestHandler_CreateTask_WithAutoMerge(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"title":      {"Task with Auto Merge"},
		"prompt":     {"do work"},
		"category":   {"active"},
		"priority":   {"1"},
		"auto_merge": {"on"},
	}

	req := worktreeFormRequest(http.MethodPost, "/tasks?project_id="+project.ID, form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the created task has auto_merge enabled
	tasks, err := h.taskSvc.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var found *models.Task
	for i := range tasks {
		if tasks[i].Title == "Task with Auto Merge" {
			found = &tasks[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created task not found")
	}
	if !found.AutoMerge {
		t.Error("expected auto_merge=true on created task")
	}
}

func TestHandler_MergeTaskBranch_ChangesTabDisabled_ReturnsForbidden(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetTaskChangesMergeOptionsEnabled(false)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merge Disabled",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreePath:      "/tmp/.worktrees/task_disabled",
		WorktreeBranch:    "task/disabled-merge",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"merge_type":   {"merge"},
		"merge_source": {"changes_tab"},
	}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "task changes merge options are disabled") {
		t.Fatalf("expected disabled flag error, got %s", rec.Body.String())
	}
}
