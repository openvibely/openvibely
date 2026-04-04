package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAPIGetProjects_Success(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := service.NewProjectService(projectRepo)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{
		Name:        "Test Project 1",
		Description: "First test project",
		RepoPath:    "/test/path/1",
	}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}

	project2 := &models.Project{
		Name:        "Test Project 2",
		Description: "Second test project",
		RepoPath:    "/test/path/2",
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create handler
	h := &Handler{
		projectSvc: projectSvc,
	}

	// Create request
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute handler
	if err := h.APIGetProjects(c); err != nil {
		t.Fatalf("APIGetProjects returned error: %v", err)
	}

	// Check status code
	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	// Parse response
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Check projects array exists
	projectsData, ok := response["projects"]
	if !ok {
		t.Fatal("response missing 'projects' field")
	}

	projects, ok := projectsData.([]interface{})
	if !ok {
		t.Fatalf("projects is not an array: %T", projectsData)
	}

	// Should have 3 projects (2 created + 1 default from migrations)
	if len(projects) < 2 {
		t.Errorf("expected at least 2 projects, got %d", len(projects))
	}

	// Verify project structure
	firstProject := projects[0].(map[string]interface{})
	requiredFields := []string{"id", "name", "path", "created_at"}
	for _, field := range requiredFields {
		if _, ok := firstProject[field]; !ok {
			t.Errorf("project missing required field: %s", field)
		}
	}

	// Verify one of our created projects is in the response
	foundProject1 := false
	for _, p := range projects {
		project := p.(map[string]interface{})
		if project["name"] == "Test Project 1" {
			foundProject1 = true
			if project["path"] != "/test/path/1" {
				t.Errorf("expected path '/test/path/1', got %v", project["path"])
			}
		}
	}
	if !foundProject1 {
		t.Error("Test Project 1 not found in response")
	}
}

func TestAPIGetProjects_EmptyList(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	// Don't create any additional projects (only default from migration exists)

	h := &Handler{
		projectSvc: projectSvc,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.APIGetProjects(c); err != nil {
		t.Fatalf("APIGetProjects returned error: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	projectsData, ok := response["projects"]
	if !ok {
		t.Fatal("response missing 'projects' field")
	}

	projects := projectsData.([]interface{})
	// Should have at least the default project from migrations
	if len(projects) < 1 {
		t.Errorf("expected at least 1 project (default), got %d", len(projects))
	}
}

func TestAPIGetProjects_JSONFormat(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := service.NewProjectService(projectRepo)
	ctx := context.Background()

	project := &models.Project{
		Name:        "API Test Project",
		Description: "Testing JSON format",
		RepoPath:    "/api/test/path",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	h := &Handler{
		projectSvc: projectSvc,
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.APIGetProjects(c); err != nil {
		t.Fatalf("APIGetProjects returned error: %v", err)
	}

	// Verify Content-Type is application/json
	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/json" && contentType != "application/json; charset=UTF-8" {
		t.Errorf("expected Content-Type 'application/json', got %q", contentType)
	}

	// Verify valid JSON structure matches specification
	var response struct {
		Projects []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Path      string `json:"path"`
			CreatedAt string `json:"created_at"`
		} `json:"projects"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to parse response as expected structure: %v", err)
	}

	if len(response.Projects) == 0 {
		t.Fatal("expected at least one project in response")
	}

	// Find our created project
	var found bool
	for _, p := range response.Projects {
		if p.Name == "API Test Project" {
			found = true
			if p.Path != "/api/test/path" {
				t.Errorf("expected path '/api/test/path', got %q", p.Path)
			}
			if p.ID == "" {
				t.Error("project ID is empty")
			}
			if p.CreatedAt == "" {
				t.Error("created_at is empty")
			}
		}
	}

	if !found {
		t.Error("created project not found in response")
	}
}
