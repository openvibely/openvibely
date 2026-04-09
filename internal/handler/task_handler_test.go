package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestHandler_CancelTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task in running status
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Running Task",
		Prompt:    "Test prompt",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Cancel the task
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the task was moved to backlog with pending status
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusCancelled {
		t.Errorf("expected task status to be %s, got %s", models.StatusCancelled, updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Errorf("expected task category to be %s, got %s", models.CategoryBacklog, updatedTask.Category)
	}
}

func TestHandler_CancelTask_NotRunning(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task in pending status (not running)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Pending Task",
		Prompt:    "Test prompt",
		Status:    models.StatusPending,
		Category:  models.CategoryActive,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Try to cancel the non-running task
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return bad request
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	// Verify the task status was NOT changed
	updatedTask, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusPending {
		t.Errorf("expected task status to remain %s, got %s", models.StatusPending, updatedTask.Status)
	}
}

func TestHandler_CancelTask_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	// Try to cancel a non-existent task
	req := httptest.NewRequest(http.MethodPost, "/tasks/nonexistent-id/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return not found
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestHandler_RunTask_NoModelsConfiguredHTMX(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agents, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("failed to list models: %v", err)
	}
	for _, agent := range agents {
		if err := llmConfigRepo.Delete(ctx, agent.ID); err != nil {
			t.Fatalf("failed to delete model %s: %v", agent.ID, err)
		}
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "No model run",
		Prompt:    "Try to run",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/run", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to contain openvibelyToast event, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredMessage) {
		t.Fatalf("expected HX-Trigger to contain no-models message, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkURL) {
		t.Fatalf("expected HX-Trigger to contain models link URL %q, got %q", noModelsConfiguredLinkURL, hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkText) {
		t.Fatalf("expected HX-Trigger to contain models link text %q, got %q", noModelsConfiguredLinkText, hxTrigger)
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if updatedTask == nil {
		t.Fatal("expected task to exist")
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Fatalf("expected task category to remain %s, got %s", models.CategoryBacklog, updatedTask.Category)
	}
	if updatedTask.Status != models.StatusPending {
		t.Fatalf("expected task status to remain %s, got %s", models.StatusPending, updatedTask.Status)
	}
}

func TestHandler_CreateTask_Active_NoModelsConfiguredHTMX(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agents, err := llmConfigRepo.List(ctx)
	if err != nil {
		t.Fatalf("failed to list models: %v", err)
	}
	for _, agent := range agents {
		if err := llmConfigRepo.Delete(ctx, agent.ID); err != nil {
			t.Fatalf("failed to delete model %s: %v", agent.ID, err)
		}
	}

	body := strings.NewReader("title=No+Model+Task&prompt=Try+to+create&category=active&priority=0")
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id=default", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to contain openvibelyToast event, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredMessage) {
		t.Fatalf("expected HX-Trigger to contain no-models message, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, noModelsConfiguredLinkURL) {
		t.Fatalf("expected HX-Trigger to contain models link URL %q, got %q", noModelsConfiguredLinkURL, hxTrigger)
	}

	tasks, err := h.taskSvc.ListByProjectWithCategorySorts(ctx, "default", "", "", "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	for _, task := range tasks {
		if task.Title == "No Model Task" {
			t.Fatalf("task should not be created when no models are configured")
		}
	}
}

func TestHandler_CreateTask_WithMultipleAttachments(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create multipart form with multiple files
	body, contentType, err := createMultipartFormWithFiles(t, map[string]string{
		"title":    "Task with Multiple Attachments",
		"prompt":   "Test prompt",
		"category": "active",
		"priority": "0",
	}, []string{"file1.txt", "file2.txt", "file3.txt"})
	if err != nil {
		t.Fatalf("failed to create multipart form: %v", err)
	}

	// Create task with attachments
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify task was created
	tasks, err := h.taskSvc.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("failed to list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]

	// Verify all 3 attachments were created
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to list attachments: %v", err)
	}

	if len(attachments) != 3 {
		t.Errorf("expected 3 attachments, got %d", len(attachments))
	}

	// Verify filenames
	expectedFiles := map[string]bool{
		"file1.txt": false,
		"file2.txt": false,
		"file3.txt": false,
	}

	for _, att := range attachments {
		if _, exists := expectedFiles[att.FileName]; exists {
			expectedFiles[att.FileName] = true
		} else {
			t.Errorf("unexpected attachment: %s", att.FileName)
		}
	}

	for file, found := range expectedFiles {
		if !found {
			t.Errorf("missing expected attachment: %s", file)
		}
	}
}

func TestHandler_GetTask_AfterCategoryChange(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test",
		RepoPath:    "/tmp/test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create a task in active category with running status
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Prompt:    "Test prompt",
		Status:    models.StatusRunning,
		Category:  models.CategoryActive,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get the task detail dialog (simulating opening the dialog while task is running)
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("active")) {
		t.Error("expected dialog to show 'active' category")
	}
	if !bytes.Contains([]byte(body), []byte("In Progress")) || !bytes.Contains([]byte(body), []byte("running")) {
		t.Error("expected dialog to show running status")
	}

	// Simulate task completion: update status to completed and category to completed
	err = h.taskSvc.UpdateStatus(ctx, task.ID, models.StatusCompleted)
	if err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}
	err = h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryCompleted)
	if err != nil {
		t.Fatalf("failed to update task category: %v", err)
	}

	// Get the task detail dialog again (simulating SSE refresh of open dialog)
	req = httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body = rec.Body.String()
	// Verify the dialog now shows completed category
	if !bytes.Contains([]byte(body), []byte("completed")) {
		t.Error("expected dialog to show 'completed' category after update")
	}
	// Verify the status badge is updated
	if !bytes.Contains([]byte(body), []byte("Completed")) {
		t.Error("expected dialog to show 'Completed' status label after update")
	}
	// Verify we no longer see running status
	if bytes.Contains([]byte(body), []byte("In Progress")) {
		t.Error("dialog should not show 'In Progress' after completion")
	}
}

func TestHandler_TaskThread_LightModeToolCallContrastStyles(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Thread Style Project",
		Description: "Test",
		RepoPath:    "/tmp/thread-style-test",
		IsDefault:   true,
	}
	err := h.projectSvc.Create(ctx, project)
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Thread style task",
		Prompt:    "Check thread styles",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	err = h.taskRepo.Create(ctx, task)
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab=chat", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `id="thread-content"`) {
		t.Fatal("expected lazy thread container in task detail response")
	}
	if !strings.Contains(body, `Thread is loading...`) {
		t.Fatal("expected lazy thread loading placeholder when chat tab is active")
	}
	if !strings.Contains(body, `function _loadThreadContent(taskId)`) {
		t.Fatal("expected thread lazy-load helper in task detail response")
	}
	if !strings.Contains(body, `--ov-link-color: #7480ff;`) {
		t.Error("expected shared link color token for thread/chat link styling")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-summary .tool-name-secondary`) {
		t.Error("expected light-theme tool secondary text style in thread view response")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body {`) {
		t.Error("expected light-theme tool outer body style in thread view response")
	}
	if !strings.Contains(body, `border: none;`) {
		t.Error("expected light-theme tool outer body to blend without border emphasis")
	}
	if !strings.Contains(body, `background: transparent;`) {
		t.Error("expected light-theme tool outer body to blend without background emphasis")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-row {`) {
		t.Error("expected light-theme tool row style in thread view response")
	}
	if !strings.Contains(body, `border-top-color: transparent;`) {
		t.Error("expected light-theme tool rows to avoid divider emphasis")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-content {`) {
		t.Error("expected light-theme tool content block style in thread view response")
	}
	if !strings.Contains(body, `background: transparent;`) {
		t.Error("expected light-theme tool outer content block to avoid extra card background")
	}
	if !strings.Contains(body, `border: none;`) {
		t.Error("expected light-theme tool outer content block to avoid extra border")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-content pre`) {
		t.Error("expected light-theme tool inner content style in thread view response")
	}
	if !strings.Contains(body, `border: none;`) {
		t.Error("expected light-theme tool inner content to render without border emphasis")
	}
	if !strings.Contains(body, `border-radius: 5px;`) {
		t.Error("expected light-theme tool inner content to retain rounded corners")
	}
	if !strings.Contains(body, `[data-theme="light"] .tool-status-done`) {
		t.Error("expected light-theme tool status icon style in thread view response")
	}
	if !strings.Contains(body, `[data-theme="light"] .stream-tool-body-content pre`) {
		t.Error("expected light-theme tool body content color style in thread view response")
	}
	if !strings.Contains(body, `.chat-markdown a {`) {
		t.Error("expected shared markdown link styling in thread view response")
	}
	if !strings.Contains(body, `.chat-markdown a:visited {`) {
		t.Error("expected markdown visited-link styling in thread view response")
	}
	if !strings.Contains(body, `.chat-markdown a:focus-visible {`) {
		t.Error("expected markdown focus-visible link styling in thread view response")
	}
}

// createMultipartFormWithFiles creates a multipart form with form fields and multiple files
func createMultipartFormWithFiles(t *testing.T, fields map[string]string, filenames []string) (io.Reader, string, error) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add form fields
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}

	// Add files
	for i, filename := range filenames {
		fileWriter, err := writer.CreateFormFile("files", filename)
		if err != nil {
			return nil, "", err
		}
		content := []byte("test file " + filename + " content")
		if _, err := fileWriter.Write(content); err != nil {
			return nil, "", err
		}
		t.Logf("Added file %d: %s", i+1, filename)
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return body, writer.FormDataContentType(), nil
}
