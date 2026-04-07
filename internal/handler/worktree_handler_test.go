package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
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

func TestHandler_MergeTaskBranch_CommitFailure_ReturnsHTMXErrorWithToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}
	mustRun("git", "init")
	mustRun("git", "config", "user.email", "test@example.com")
	mustRun("git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "README.md")
	mustRun("git", "commit", "-m", "initial")

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: repoDir, IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))

	worktreePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merge Failure",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreePath:      worktreePath,
		WorktreeBranch:    "task/fail-merge",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Local merge failed") {
		t.Fatalf("expected merge failure body, got %s", rec.Body.String())
	}
	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger toast, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "Local merge failed") {
		t.Fatalf("expected toast message to include merge failure, got %q", hxTrigger)
	}
}

func TestHandler_MergeTaskBranch_Conflict_ReturnsHTMXToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}
	mustRunOutput := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	mustRun("git", "init")
	mustRun("git", "config", "user.email", "test@example.com")
	mustRun("git", "config", "user.name", "Test User")
	defaultBranch := mustRunOutput("git", "branch", "--show-current")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	conflictFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("shared line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "base")

	worktreeBranch := "task/conflict-merge"
	mustRun("git", "checkout", "-b", worktreeBranch)
	if err := os.WriteFile(conflictFile, []byte("branch change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "branch change")

	mustRun("git", "checkout", defaultBranch)
	if err := os.WriteFile(conflictFile, []byte("target change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "target change")

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: repoDir, IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merge Conflict",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreeBranch:    worktreeBranch,
		MergeTargetBranch: defaultBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected conflict toast trigger, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "Local merge has conflicts") {
		t.Fatalf("expected conflict toast message, got %q", hxTrigger)
	}
	if !strings.Contains(rec.Body.String(), "Conflicts") {
		t.Fatalf("expected conflict badge in worktree panel, got %s", rec.Body.String())
	}

	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected merge_status=conflict, got %s", updated.MergeStatus)
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
