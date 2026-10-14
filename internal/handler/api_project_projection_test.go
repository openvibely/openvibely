package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestAPIGetProjectsUsesCompactProjectionAndPreservesContract(t *testing.T) {
	env := teststores.New(t, teststores.WithIOCounting())
	counter := env.IO
	repo := env.Projects
	ctx := context.Background()

	description := strings.Repeat("large project description ", 4096)
	repoPath := "/workspace/" + strings.Repeat("nested/repository/", 64)
	repoURL := "https://github.example.test/" + strings.Repeat("organization/repository/", 64)
	seedAPIProject(t, env.Fixtures, "api-default", "Zebra Default", true, description, repoPath, repoURL)
	seedAPIProject(t, env.Fixtures, "api-alpha", "Alpha", false, description, "/alpha/path", repoURL)
	seedAPIProject(t, env.Fixtures, "api-same-b", "Same", false, description, "/same-b/path", repoURL)
	seedAPIProject(t, env.Fixtures, "api-same-a", "Same", false, description, "/same-a/path", repoURL)

	if err := env.Fixtures.SetDefaultProject(ctx, "api-default"); err != nil {
		t.Fatal(err)
	}

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
	counter.SetEnabled(false)
	observed := counter.Snapshot()
	if observed.Requests != 1 || observed.ReadTextBytes > 16*1024 {
		t.Fatalf("unexpected physical project IO: %+v", observed)
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

func seedAPIProject(t *testing.T, fixtures storefixture.Fixtures, id, name string, isDefault bool, description, repoPath, repoURL string) {
	t.Helper()
	if err := fixtures.SeedProjects(context.Background(), []models.Project{{ID: id, Name: name, IsDefault: isDefault, Description: description, RepoPath: repoPath, RepoURL: repoURL}}); err != nil {
		t.Fatal(err)
	}
}
