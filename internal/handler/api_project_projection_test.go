package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAPIGetProjectsUsesCompactProjectionAndPreservesContract(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewProjectRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `UPDATE projects SET is_default = 0 WHERE id = 'default'`); err != nil {
		t.Fatalf("clear default project flag: %v", err)
	}

	description := strings.Repeat("large project description ", 4096)
	repoPath := "/workspace/" + strings.Repeat("nested/repository/", 64)
	repoURL := "https://github.example.test/" + strings.Repeat("organization/repository/", 64)
	seedAPIProject(t, db, "api-default", "Zebra Default", true, description, repoPath, repoURL)
	seedAPIProject(t, db, "api-alpha", "Alpha", false, description, "/alpha/path", repoURL)
	seedAPIProject(t, db, "api-same-b", "Same", false, description, "/same-b/path", repoURL)
	seedAPIProject(t, db, "api-same-a", "Same", false, description, "/same-a/path", repoURL)

	h := &Handler{projectSvc: service.NewProjectService(repo)}
	e := echo.New()
	counter.SetEnabled(true)
	counter.Reset()
	rec := httptest.NewRecorder()
	if err := h.APIGetProjects(e.NewContext(httptest.NewRequest(http.MethodGet, "/api/projects", nil), rec)); err != nil {
		t.Fatalf("APIGetProjects: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("APIGetProjects statements = %d, want 1: %#v", len(statements), statements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if query != "select id, name, repo_path, created_at from projects order by is_default desc, name asc" {
		t.Fatalf("APIGetProjects query = %q", query)
	}
	for _, omitted := range []string{"description", "repo_url", "default_agent_config_id", "max_workers", "updated_at"} {
		if strings.Contains(query, omitted) {
			t.Errorf("APIGetProjects selected omitted column %q: %s", omitted, query)
		}
	}

	var response ProjectsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode API response: %v", err)
	}
	if len(response.Projects) != 5 {
		t.Fatalf("response projects = %d, want 5", len(response.Projects))
	}
	if response.Projects[0].ID != "api-default" {
		t.Fatalf("default project was not first: %q", response.Projects[0].ID)
	}
	for i := 2; i < len(response.Projects); i++ {
		if response.Projects[i-1].Name > response.Projects[i].Name {
			t.Fatalf("project names are not ascending at %d: %q > %q", i, response.Projects[i-1].Name, response.Projects[i].Name)
		}
	}
	var sameIDs []string
	for _, project := range response.Projects {
		if project.Name == "Same" {
			sameIDs = append(sameIDs, project.ID)
		}
		if project.ID == "api-default" {
			if project.Path != repoPath {
				t.Fatalf("default path = %q, want %q", project.Path, repoPath)
			}
			if project.CreatedAt == "" {
				t.Fatal("default created_at is empty")
			}
		}
	}
	if strings.Join(sameIDs, ",") != "api-same-a,api-same-b" {
		t.Fatalf("equal-name project ordering = %v", sameIDs)
	}

	full, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("full project List: %v", err)
	}
	fullProjectFound := false
	for _, project := range full {
		if project.ID != "api-default" {
			continue
		}
		fullProjectFound = true
		if project.Description != description || project.RepoPath != repoPath || project.RepoURL != repoURL {
			t.Fatal("full project List did not retain management/repository fields")
		}
		break
	}
	if !fullProjectFound {
		t.Fatal("full project List did not return api-default")
	}

	expectedBody, err := json.Marshal(map[string]interface{}{"projects": projectResponsesFromFullProjects(full)})
	if err != nil {
		t.Fatalf("marshal expected response: %v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(rec.Body.Bytes()), expectedBody) {
		t.Fatalf("API response bytes changed:\n got: %s\nwant: %s", rec.Body.Bytes(), expectedBody)
	}
}

func seedAPIProject(t *testing.T, db *sql.DB, id, name string, isDefault bool, description, repoPath, repoURL string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO projects (id, name, description, repo_path, repo_url, is_default) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, description, repoPath, repoURL, isDefault); err != nil {
		t.Fatalf("seed API project %q: %v", id, err)
	}
}
