package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestCreateProject_GitHubImportSuccess(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	var cloneProjectID string
	h.SetGitHubService(&fakeGitHubService{
		cloneFn: func(_ context.Context, projectID, repoURL string) (string, string, error) {
			cloneProjectID = projectID
			if repoURL != "https://github.com/openvibely/openvibely" {
				t.Fatalf("unexpected repo URL: %s", repoURL)
			}
			return "/tmp/repos/" + projectID, "https://github.com/openvibely/openvibely", nil
		},
	})

	form := url.Values{}
	form.Set("name", "GitHub Import Project")
	form.Set("description", "Project from github")
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d (%s)", rec.Code, rec.Body.String())
	}

	projects, err := h.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects: %v", err)
	}
	var found *models.Project
	for i := range projects {
		if projects[i].Name == "GitHub Import Project" {
			found = &projects[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected created project")
	}
	if found.RepoURL != "https://github.com/openvibely/openvibely" {
		t.Fatalf("expected repo_url persisted, got %s", found.RepoURL)
	}
	if found.RepoPath == "" || !strings.Contains(found.RepoPath, found.ID) {
		t.Fatalf("expected managed clone path, got %s", found.RepoPath)
	}
	if cloneProjectID == "" {
		t.Fatal("expected clone to be invoked with project ID")
	}
}

func TestCreateProject_GitHubImportRequiresService(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	form := url.Values{}
	form.Set("name", "GitHub Import Missing Service")
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestUpdateProject_GitHubImportReclone(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	project := &models.Project{Name: "Reclone Project", Description: "desc", RepoPath: "/tmp/local/repo"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	var gotCurrentPath string
	h.SetGitHubService(&fakeGitHubService{
		recloneFn: func(_ context.Context, projectID, currentRepoPath, repoURL string) (string, string, error) {
			if projectID != project.ID {
				t.Fatalf("unexpected project id: %s", projectID)
			}
			gotCurrentPath = currentRepoPath
			return "/tmp/repos/" + projectID, "https://github.com/openvibely/openvibely", nil
		},
	})

	form := url.Values{}
	form.Set("name", project.Name)
	form.Set("description", project.Description)
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected status 303, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotCurrentPath != "/tmp/local/repo" {
		t.Fatalf("expected current path passed to reclone, got %s", gotCurrentPath)
	}

	updated, err := h.projectSvc.GetByID(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("fetch updated project: %v", err)
	}
	if updated.RepoPath != "/tmp/repos/"+project.ID {
		t.Fatalf("unexpected repo_path: %s", updated.RepoPath)
	}
	if updated.RepoURL != "https://github.com/openvibely/openvibely" {
		t.Fatalf("unexpected repo_url: %s", updated.RepoURL)
	}
}

func TestCreateProject_GitHubImportCloneFailureHTMXShowsToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.SetGitHubService(&fakeGitHubService{
		cloneFn: func(_ context.Context, _, _ string) (string, string, error) {
			return "", "", fmt.Errorf("github personal access token is not configured")
		},
	})

	form := url.Values{}
	form.Set("name", "GitHub Import Failure")
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to include openvibelyToast, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "failed to clone GitHub repository: github personal access token is not configured") {
		t.Fatalf("expected clone failure message in HX-Trigger, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, githubPATSetupLinkURL) {
		t.Fatalf("expected PAT setup link URL %q in HX-Trigger, got %q", githubPATSetupLinkURL, hxTrigger)
	}
	if !strings.Contains(hxTrigger, githubPATSetupLinkText) {
		t.Fatalf("expected PAT setup link text %q in HX-Trigger, got %q", githubPATSetupLinkText, hxTrigger)
	}

	projects, err := h.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects: %v", err)
	}
	for i := range projects {
		if projects[i].Name == "GitHub Import Failure" {
			t.Fatalf("project should be rolled back on clone failure, but found %q", projects[i].Name)
		}
	}
}

func TestCreateProject_GitHubImportSuccessHTMXRedirectHeader(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.SetGitHubService(&fakeGitHubService{
		cloneFn: func(_ context.Context, projectID, repoURL string) (string, string, error) {
			return "/tmp/repos/" + projectID, repoURL, nil
		},
	})

	form := url.Values{}
	form.Set("name", "GitHub Import HTMX Success")
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d (%s)", rec.Code, rec.Body.String())
	}
	if hxRedirect := rec.Header().Get("HX-Redirect"); !strings.HasPrefix(hxRedirect, "/tasks?project_id=") {
		t.Fatalf("expected HX-Redirect to tasks project URL, got %q", hxRedirect)
	}
}

func TestUpdateProject_GitHubImportRecloneFailureHTMXShowsToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	project := &models.Project{Name: "Reclone Failure Project", RepoPath: "/tmp/local/repo"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	h.SetGitHubService(&fakeGitHubService{
		recloneFn: func(_ context.Context, _, _, _ string) (string, string, error) {
			return "", "", fmt.Errorf("github personal access token is not configured")
		},
	})

	form := url.Values{}
	form.Set("name", project.Name)
	form.Set("description", project.Description)
	form.Set("repo_source", "github")
	form.Set("repo_url", "https://github.com/openvibely/openvibely")

	req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger to include openvibelyToast, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "failed to clone GitHub repository: github personal access token is not configured") {
		t.Fatalf("expected reclone failure message in HX-Trigger, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, githubPATSetupLinkURL) {
		t.Fatalf("expected PAT setup link URL %q in HX-Trigger, got %q", githubPATSetupLinkURL, hxTrigger)
	}
	if !strings.Contains(hxTrigger, githubPATSetupLinkText) {
		t.Fatalf("expected PAT setup link text %q in HX-Trigger, got %q", githubPATSetupLinkText, hxTrigger)
	}

	unchanged, err := h.projectSvc.GetByID(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("fetch unchanged project: %v", err)
	}
	if unchanged.RepoPath != "/tmp/local/repo" {
		t.Fatalf("expected repo_path unchanged on reclone failure, got %q", unchanged.RepoPath)
	}
}
