package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupReviewHandler(t *testing.T) (*Handler, *echo.Echo, *repository.ReviewCommentRepo, *repository.ExecutionRepo, *testutil.MockLLMCaller, string) {
	t.Helper()
	db := testutil.NewTestDB(t)

	broadcaster := events.NewBroadcaster()
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	reviewCommentRepo := repository.NewReviewCommentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	mockLLM := testutil.NewMockLLMCaller()
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(mockLLM)
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	workerSvc.SetProjectRepo(projectRepo)
	workerSvc.SetTaskRepo(taskRepo)
	workerSvc.SetLLMConfigRepo(llmConfigRepo)
	projectSvc := service.NewProjectService(projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	h := New(
		projectSvc,
		taskSvc,
		llmSvc,
		workerSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, nil, attachmentRepo, nil, projectRepo, settingsRepo, broadcaster, nil,
	)
	h.SetReviewCommentRepo(reviewCommentRepo)

	e := echo.New()
	h.RegisterRoutes(e)

	// Create a project and task
	ctx := context.Background()
	project := &models.Project{Name: "Review Test"}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := &models.LLMConfig{
		Name:       "Review Test Agent",
		Provider:   models.ProviderTest,
		Model:      "test-model",
		APIKey:     "test-key",
		IsDefault:  true,
		MaxWorkers: 0,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Review Test Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "test prompt",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	return h, e, reviewCommentRepo, execRepo, mockLLM, task.ID
}

func TestListReviewComments_Empty(t *testing.T) {
	_, e, _, _, _, taskID := setupReviewHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID+"/reviews", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "review-comments-list") {
		t.Error("expected review comments list container")
	}
	if !strings.Contains(body, `data-comment-count="0"`) {
		t.Error("expected 0 comments")
	}
}

func TestAddReviewComment(t *testing.T) {
	_, e, _, _, _, taskID := setupReviewHandler(t)

	form := url.Values{}
	form.Set("file_path", "main.go")
	form.Set("line_number", "42")
	form.Set("line_type", "new")
	form.Set("comment_text", "Needs error handling")

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Needs error handling") {
		t.Error("expected comment text in response")
	}
	if !strings.Contains(body, "main.go:42") {
		t.Error("expected file:line reference in response")
	}
}

func TestAddReviewComment_MissingFields(t *testing.T) {
	_, e, _, _, _, taskID := setupReviewHandler(t)

	// Missing comment_text
	form := url.Values{}
	form.Set("file_path", "main.go")
	form.Set("line_number", "42")

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateReviewComment(t *testing.T) {
	_, e, reviewRepo, _, _, taskID := setupReviewHandler(t)
	ctx := context.Background()

	comment := &models.ReviewComment{
		TaskID:      taskID,
		FilePath:    "main.go",
		LineNumber:  10,
		LineType:    "new",
		CommentText: "Original comment",
		ReviewedBy:  "user",
	}
	if err := reviewRepo.Create(ctx, comment); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	form := url.Values{}
	form.Set("comment_text", "Edited comment text")
	req := httptest.NewRequest(http.MethodPatch, "/reviews/"+comment.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	comments, err := reviewRepo.ListByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].CommentText != "Edited comment text" {
		t.Fatalf("expected updated text, got %q", comments[0].CommentText)
	}
}

func TestDeleteReviewComment(t *testing.T) {
	_, e, reviewRepo, _, _, taskID := setupReviewHandler(t)
	ctx := context.Background()

	// Create a comment first
	comment := &models.ReviewComment{
		TaskID:      taskID,
		FilePath:    "main.go",
		LineNumber:  10,
		LineType:    "new",
		CommentText: "To be deleted",
		ReviewedBy:  "user",
	}
	if err := reviewRepo.Create(ctx, comment); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/reviews/"+comment.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Verify comment is gone
	count, _ := reviewRepo.CountByTask(ctx, taskID)
	if count != 0 {
		t.Errorf("expected 0 comments after delete, got %d", count)
	}
}

func TestUpdateReviewComment_MissingText(t *testing.T) {
	_, e, reviewRepo, _, _, taskID := setupReviewHandler(t)
	ctx := context.Background()

	comment := &models.ReviewComment{
		TaskID:      taskID,
		FilePath:    "main.go",
		LineNumber:  10,
		LineType:    "new",
		CommentText: "Original",
		ReviewedBy:  "user",
	}
	if err := reviewRepo.Create(ctx, comment); err != nil {
		t.Fatalf("create comment: %v", err)
	}

	form := url.Values{}
	form.Set("comment_text", "   ")
	req := httptest.NewRequest(http.MethodPatch, "/reviews/"+comment.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestAddReviewComment_InvalidLineNumber(t *testing.T) {
	_, e, _, _, _, taskID := setupReviewHandler(t)

	form := url.Values{}
	form.Set("file_path", "main.go")
	form.Set("line_number", "not_a_number")
	form.Set("comment_text", "test")

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSubmitReview_NoComments(t *testing.T) {
	_, e, _, _, _, taskID := setupReviewHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews/submit", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when no comments, got %d", rec.Code)
	}
}

func TestAddMultipleComments_SameFile(t *testing.T) {
	_, e, reviewRepo, _, _, taskID := setupReviewHandler(t)
	ctx := context.Background()

	// Add two comments to different lines
	for _, lineNum := range []string{"10", "20"} {
		form := url.Values{}
		form.Set("file_path", "main.go")
		form.Set("line_number", lineNum)
		form.Set("line_type", "new")
		form.Set("comment_text", "Comment on line "+lineNum)

		req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for line %s, got %d", lineNum, rec.Code)
		}
	}

	// Verify both comments exist
	count, _ := reviewRepo.CountByTask(ctx, taskID)
	if count != 2 {
		t.Errorf("expected 2 comments, got %d", count)
	}
}

func TestSubmitReview_CreatesFollowupExecutionAndClearsComments(t *testing.T) {
	_, e, reviewRepo, execRepo, mockLLM, taskID := setupReviewHandler(t)
	ctx := context.Background()

	for _, input := range []struct {
		line string
		text string
	}{
		{line: "10", text: "Fix nil handling"},
		{line: "20", text: "Add test coverage"},
	} {
		form := url.Values{}
		form.Set("file_path", "main.go")
		form.Set("line_number", input.line)
		form.Set("line_type", "new")
		form.Set("comment_text", input.text)

		req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("add review comment line %s: expected 200, got %d", input.line, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/reviews/submit", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/tasks/"+taskID+"?tab=chat" {
		t.Fatalf("expected HX-Redirect to chat tab, got %q", got)
	}

	count, err := reviewRepo.CountByTask(ctx, taskID)
	if err != nil {
		t.Fatalf("CountByTask: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected comments to be cleared after submit, got %d", count)
	}

	execs, err := execRepo.ListByTaskChronological(ctx, taskID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("expected 1 follow-up execution, got %d", len(execs))
	}
	if !execs[0].IsFollowup {
		t.Fatal("expected review submission execution to be marked as follow-up")
	}
	if !strings.Contains(execs[0].PromptSent, "Fix nil handling") || !strings.Contains(execs[0].PromptSent, "Add test coverage") {
		t.Fatalf("expected submitted review prompt to include all comments, got %q", execs[0].PromptSent)
	}

	// Wait briefly for the background goroutine to acquire worker slots and call the LLM.
	// With IsTaskFollowup=true, the goroutine acquires project+model slots before calling.
	for i := 0; i < 50; i++ {
		if mockLLM.CallCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if mockLLM.CallCount() == 0 {
		t.Fatal("expected review submission to trigger background LLM processing")
	}
	lastCall := mockLLM.LastCall()
	if !strings.Contains(lastCall.Prompt, "Fix nil handling") || !strings.Contains(lastCall.Prompt, "Add test coverage") {
		t.Fatalf("expected LLM prompt to include all comments, got %q", lastCall.Prompt)
	}
}
