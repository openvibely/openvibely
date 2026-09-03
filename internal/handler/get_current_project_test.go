package handler

import (
	"context"
	"database/sql"
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
	h, projectSvc, _ := setupProjectTestHandlerWithDB(t)
	return h, projectSvc
}

func setupProjectTestHandlerWithDB(t *testing.T) (*Handler, *service.ProjectService, *sql.DB) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
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

	h := New(projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, nil, nil, nil, llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, nil, nil)

	return h, projectSvc, db
}

func TestMutationProjectID(t *testing.T) {
	newContext := func(path string) echo.Context {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		return e.NewContext(req, httptest.NewRecorder())
	}

	t.Run("explicit project is trimmed and wins", func(t *testing.T) {
		h, _, _ := setupProjectTestHandlerWithDB(t)
		if err := h.settingsRepo.Set(context.Background(), uiPreferenceSelectedProjectIDKey, " saved-project "); err != nil {
			t.Fatalf("failed to save selected project preference: %v", err)
		}

		got := h.mutationProjectID(newContext("/?project_id=%20explicit-project%20"))
		if got != "explicit-project" {
			t.Fatalf("got %q, want trimmed explicit project", got)
		}
	})

	t.Run("saved project is trimmed", func(t *testing.T) {
		h, _, _ := setupProjectTestHandlerWithDB(t)
		if err := h.settingsRepo.Set(context.Background(), uiPreferenceSelectedProjectIDKey, " saved-project "); err != nil {
			t.Fatalf("failed to save selected project preference: %v", err)
		}

		got := h.mutationProjectID(newContext("/"))
		if got != "saved-project" {
			t.Fatalf("got %q, want trimmed saved project", got)
		}
	})

	t.Run("missing settings returns empty scope", func(t *testing.T) {
		h, _, _ := setupProjectTestHandlerWithDB(t)
		h.settingsRepo = nil

		if got := h.mutationProjectID(newContext("/")); got != "" {
			t.Fatalf("got %q, want empty scope", got)
		}
	})

	t.Run("preference read errors return empty scope", func(t *testing.T) {
		h, _, db := setupProjectTestHandlerWithDB(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close test database: %v", err)
		}

		if got := h.mutationProjectID(newContext("/")); got != "" {
			t.Fatalf("got %q, want empty scope", got)
		}
	})
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

func TestGetCurrentProjectID_EmptyFallbackUsesSelectorProjection(t *testing.T) {
	h, projectSvc, db := setupProjectTestHandlerWithDB(t)
	ctx := context.Background()

	projects, err := projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		t.Fatalf("list selector options: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("expected seeded default project")
	}
	firstID := projects[0].ID

	if _, err := db.ExecContext(ctx, `UPDATE projects SET created_at = 'not-a-timestamp', updated_at = 'not-a-timestamp' WHERE id = ?`, firstID); err != nil {
		t.Fatalf("poison full-detail-only timestamp columns: %v", err)
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

func TestGetCurrentProjectID_EmptyUsesSavedSelectedProject(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	project := &models.Project{Name: "Saved Project"}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, project.ID); err != nil {
		t.Fatalf("failed to save selected project preference: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != project.ID {
		t.Errorf("got %q, want saved project %q", got, project.ID)
	}
}

func TestGetCurrentProjectID_ExplicitProjectOverridesSavedSelectedProject(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	saved := &models.Project{Name: "Saved Project"}
	explicit := &models.Project{Name: "Explicit Project"}
	if err := projectSvc.Create(ctx, saved); err != nil {
		t.Fatalf("failed to create saved project: %v", err)
	}
	if err := projectSvc.Create(ctx, explicit); err != nil {
		t.Fatalf("failed to create explicit project: %v", err)
	}
	if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, saved.ID); err != nil {
		t.Fatalf("failed to save selected project preference: %v", err)
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+explicit.ID, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	got, err := h.getCurrentProjectID(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != explicit.ID {
		t.Errorf("got %q, want explicit project %q", got, explicit.ID)
	}
}

func TestGetCurrentProjectID_StaleSavedProjectFallsBackToFirst(t *testing.T) {
	h, projectSvc := setupProjectTestHandler(t)
	ctx := context.Background()

	projects, _ := projectSvc.List(ctx)
	firstID := ""
	if len(projects) > 0 {
		firstID = projects[0].ID
	}
	if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, "missing-project"); err != nil {
		t.Fatalf("failed to save stale selected project preference: %v", err)
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
		t.Errorf("got %q, want first project %q", got, firstID)
	}
}
