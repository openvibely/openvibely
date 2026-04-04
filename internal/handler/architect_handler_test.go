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
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupArchitectTestHandler(t *testing.T) (*Handler, *echo.Echo, *repository.ProjectRepo, *service.ArchitectService) {
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
	architectRepo := repository.NewArchitectRepo(db)

	alertRepo := repository.NewAlertRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, nil)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	alertSvc := service.NewAlertService(alertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	architectSvc := service.NewArchitectService(architectRepo, taskRepo, projectRepo, llmConfigRepo)

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, alertSvc, upcomingSvc, nil, nil, nil, architectSvc, nil, nil, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, nil, nil, nil, nil)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, projectRepo, architectSvc
}

func TestArchitectHandler_CreateSession(t *testing.T) {
	_, e, projectRepo, _ := setupArchitectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test Project", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	form := url.Values{}
	form.Set("title", "My Vision")
	form.Set("description", "Building an amazing app")

	req := httptest.NewRequest(http.MethodPost, "/architect/sessions?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "My Vision") {
		t.Error("expected response to contain session title 'My Vision'")
	}
	// Should render the wizard view with phase steps
	if !strings.Contains(body, "Vision") {
		t.Error("expected response to contain phase 'Vision'")
	}
}

func TestArchitectHandler_CreateSession_MissingProjectID(t *testing.T) {
	_, e, _, _ := setupArchitectTestHandler(t)

	form := url.Values{}
	form.Set("title", "My Vision")

	req := httptest.NewRequest(http.MethodPost, "/architect/sessions", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestArchitectHandler_CreateSession_WithTemplate(t *testing.T) {
	_, e, projectRepo, architectSvc := setupArchitectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Get seeded templates
	templates, err := architectSvc.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("expected seeded templates")
	}

	form := url.Values{}
	form.Set("title", "From Template")
	form.Set("template_id", templates[0].ID)

	req := httptest.NewRequest(http.MethodPost, "/architect/sessions?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "From Template") {
		t.Error("expected response to contain session title 'From Template'")
	}
}

func TestArchitectHandler_GetDashboard(t *testing.T) {
	_, e, projectRepo, architectSvc := setupArchitectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a session
	_, err := architectSvc.CreateSession(ctx, project.ID, "Test Vision", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/architect?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Vision") {
		t.Error("expected dashboard to contain session 'Test Vision'")
	}
	if !strings.Contains(body, "New Vision") {
		t.Error("expected dashboard to contain 'New Vision' button")
	}
}

func TestArchitectHandler_DeleteSession(t *testing.T) {
	_, e, projectRepo, architectSvc := setupArchitectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := architectSvc.CreateSession(ctx, project.ID, "To Delete", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/architect/sessions/"+session.ID+"?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "To Delete") {
		t.Error("expected deleted session to not appear in dashboard")
	}
}
