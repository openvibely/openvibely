package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupProjectTestHandler(t *testing.T) (*Handler, *service.ProjectService) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, nil, nil, nil, nil)

	return h, projectSvc
}

func TestGetCurrentProjectID_WithValidID(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project"}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != project.ID {
		t.Errorf("got %q, want %q", got, project.ID)
	}
}

func TestGetCurrentProjectID_DefaultFallsBackToFirst(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	// Get the first project from list (migrations may seed one)
	projects, _ := projectSvc.List(ctx)
	firstID := ""
	if len(projects) > 0 {
		firstID = projects[0].ID
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=default", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != firstID {
		t.Errorf("got %q, want %q", got, firstID)
	}
}

func TestGetCurrentProjectID_EmptyFallsBackToFirst(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	// Get the first project from list (migrations may seed one)
	projects, _ := projectSvc.List(ctx)
	firstID := ""
	if len(projects) > 0 {
		firstID = projects[0].ID
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != firstID {
		t.Errorf("got %q, want %q", got, firstID)
	}
}

func TestGetCurrentProjectID_InvalidIDFallsBackToFirst(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	// Get the first project from list (migrations may seed one)
	projects, _ := projectSvc.List(ctx)
	firstID := ""
	if len(projects) > 0 {
		firstID = projects[0].ID
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=nonexistent-id", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != firstID {
		t.Errorf("got %q, want %q", got, firstID)
	}
}

func TestGetCurrentProjectID_SkipsListWhenIDValid(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	// Create a second project (migration seeds a default one)
	project := &models.Project{Name: "Second Project"}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	e := echo.New()
	// Query with the second project's ID directly
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return the explicitly requested project, not the default first one
	if got != project.ID {
		t.Errorf("got %q, want %q", got, project.ID)
	}
}
