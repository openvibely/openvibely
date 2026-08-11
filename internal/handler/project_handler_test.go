package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

// ---- Home ----

func TestCreateProject_EnterpriseRepositoryRequiresGlobalAPIEndpoint(t *testing.T) {
	tc := NewTestContext(t)
	tc.handler.SetGitHubService(&fakeGitHubService{
		cloneFn: func(_ context.Context, projectID, repoURL string) (string, string, error) {
			// Simulate the error ConfigureGitHubRepoEndpoint returns when no global
			// endpoint is configured for a custom GitHub Enterprise host.
			return "", "", fmt.Errorf("custom repository host requires a configured GitHub API endpoint")
		},
	})
	before, err := tc.projectRepo.List(t.Context())
	if err != nil {
		t.Fatalf("list projects before create: %v", err)
	}

	rec := tc.HTTP().Post("/projects").WithForm(url.Values{
		"name":        {"Enterprise project"},
		"repo_source": {"github"},
		"repo_url":    {"https://github.example.com/acme/widgets.git"},
	}).Execute()

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "custom repository host requires a configured GitHub API endpoint") {
		t.Fatalf("expected endpoint requirement, body=%s", rec.Body.String())
	}
	after, err := tc.projectRepo.List(t.Context())
	if err != nil {
		t.Fatalf("list projects after create: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("project count changed from %d to %d", len(before), len(after))
	}
}

func TestHome_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/chat") {
		t.Errorf("expected redirect to /chat, got %q", loc)
	}
}

func TestHome_WithProjectID(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/?project_id=abc123").Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "project_id=abc123") {
		t.Errorf("expected redirect to include project_id, got %q", loc)
	}
}

// ---- Dashboard ----

func TestDashboard_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Dash Project").Build()
	rec := tc.HTTP().Get("/dashboard?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboard_NoProject(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/dashboard").Execute()
	// Should succeed even with no project_id — defaults to first project or empty
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- ListProjects ----

func TestListProjects(t *testing.T) {
	tc := NewTestContext(t)
	tc.CreateProject().WithName("Project Alpha").Build()
	tc.CreateProject().WithName("Project Beta").Build()

	rec := tc.HTTP().Get("/projects").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var projects []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&projects); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(projects) < 2 {
		t.Errorf("expected at least 2 projects, got %d", len(projects))
	}
}

// ---- CreateProject ----

func TestNewProjectDialog_CreateButtonShowsCloneProgress(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Get("/projects/new").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`id="new-project-form"`,
		`hx-indicator="#new-project-create-progress"`,
		`id="new-project-create-button"`,
		`id="new-project-create-progress"`,
		`loading loading-spinner loading-sm htmx-indicator`,
		`Cloning repository...`,
		`id="new-project-cancel-button"`,
		`setCreateBusy(true)`,
		`setCreateBusy(false)`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected new project dialog to contain %q", expected)
		}
	}
}

func TestCreateProject_Local_Success_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/projects").WithForm(url.Values{
		"name":        {"My New Project"},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	// Should redirect to /tasks?project_id=...
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/tasks?project_id=") {
		t.Errorf("expected redirect to /tasks?project_id=..., got %q", loc)
	}
}

func TestCreateProject_HTMX_Success(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTMX().Post("/projects").WithForm(url.Values{
		"name":        {"HTMX Project"},
		"repo_source": {"local"},
	}).Execute()
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	redirect := rec.Header().Get("HX-Redirect")
	if !strings.HasPrefix(redirect, "/tasks?project_id=") {
		t.Errorf("expected HX-Redirect to /tasks?project_id=..., got %q", redirect)
	}
}

func TestCreateProject_LocalPathDisabled(t *testing.T) {
	tc := NewTestContext(t)
	// Force local path to be disabled via env — we can test the normalizeRepoSource fallback
	// by submitting repo_source=local without setting the enable env var.
	// The handler checks isLocalRepoPathEnabled() which reads OPENVIBELY_ENABLE_LOCAL_REPO_PATH.
	// In test environments it defaults to the config default, which allows local paths.
	// This test verifies the github URL-required branch instead.
	rec := tc.HTMX().Post("/projects").WithForm(url.Values{
		"name":        {"GitHub Project"},
		"repo_source": {"github"},
		"repo_url":    {""},
	}).Execute()
	// GitHub source with no URL and no githubSvc → returns error toast (204 HTMX)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 HTMX error toast, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- UpdateProject ----

func TestUpdateProject_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Put("/projects/nonexistent-id").WithForm(url.Values{
		"name":        {"Updated"},
		"repo_source": {"local"},
	}).Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUpdateProject_Success_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Original Name").Build()

	rec := tc.HTMX().Put("/projects/" + project.ID).WithForm(url.Values{
		"name":        {"Renamed Project"},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Errorf("expected HX-Refresh header to be set")
	}
}

func TestUpdateProject_Success_Redirect(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Old Name").Build()

	rec := tc.HTTP().Put("/projects/" + project.ID).WithForm(url.Values{
		"name":        {"New Name"},
		"repo_source": {"local"},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- DeleteProject ----

func TestDeleteProject_DefaultProject_Rejected(t *testing.T) {
	tc := NewTestContext(t)
	// The default project ("default") is seeded in migrations; it has is_default=true.
	rec := tc.HTTP().Delete("/projects/default").Execute()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for deleting default project, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteProject_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Delete("/projects/no-such-project").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteProject_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Deletable").Build()

	rec := tc.HTTP().Delete("/projects/" + project.ID).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", rec.Code)
	}
}

func TestDeleteProject_Success_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("HTMX Deletable").Build()

	rec := tc.HTMX().Delete("/projects/" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("HX-Redirect"), "/tasks") {
		t.Errorf("expected HX-Redirect to /tasks, got %q", rec.Header().Get("HX-Redirect"))
	}
}

// ---- NewProjectDialog ----

func TestNewProjectDialog_ViaTestContext(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/projects/new").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- EditProjectDialog ----

func TestEditProjectDialog_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/projects/nonexistent/edit").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestEditProjectDialog_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Edit Me").Build()
	rec := tc.HTTP().Get("/projects/" + project.ID + "/edit").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- ViewSchedule ----

func TestViewSchedule_Success(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	rec := tc.HTTP().Get("/schedule?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestViewSchedule_NotFound_Project(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Get("/schedule?project_id=nonexistent").Execute()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestViewSchedule_HTMX(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	rec := tc.HTMX().Get("/schedule?project_id=" + project.ID).Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// ---- Unit tests for pure helper functions ----

func TestNormalizeRepoPathInput(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"/abs/path", "/abs/path"},
		{"  /spaced/path  ", "/spaced/path"},
	}
	for _, c := range cases {
		got := normalizeRepoPathInput(c.in)
		if got != c.want {
			t.Errorf("normalizeRepoPathInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeRepoSource(t *testing.T) {
	cases := []struct {
		source, repoURL, want string
	}{
		{"github", "", "github"},
		{"local", "", "local"},
		{"GITHUB", "", "github"},
		{"LOCAL", "", "local"},
		{"", "https://github.com/foo/bar", "github"},
		{"", "", "local"},
		{"unknown", "", "local"},
		{"unknown", "https://github.com/x/y", "github"},
	}
	for _, c := range cases {
		got := normalizeRepoSource(c.source, c.repoURL)
		if got != c.want {
			t.Errorf("normalizeRepoSource(%q, %q) = %q, want %q", c.source, c.repoURL, got, c.want)
		}
	}
}

func TestParseProjectFormSettings_NormalizesCommonFieldsAndSourceValidation(t *testing.T) {
	tc := NewTestContext(t)
	form := url.Values{}
	form.Set("name", "Shared Project")
	form.Set("description", "shared settings")
	form.Set("repo_source", "unknown")
	form.Set("repo_path", "  /tmp/shared-local  ")
	form.Set("repo_url", "  https://github.com/openvibely/openvibely  ")
	form.Set("default_agent_config_id", "agent-123")
	form.Set("max_workers", "3")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	settings, err := parseProjectFormSettings(tc.echo.NewContext(req, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: true,
		GitHubSvc:            &fakeGitHubService{},
	})
	if err != nil {
		t.Fatalf("parseProjectFormSettings returned error: %v", err)
	}
	if settings.Name != "Shared Project" || settings.Description != "shared settings" {
		t.Fatalf("unexpected common fields: %+v", settings)
	}
	if settings.RepoSource != "github" {
		t.Fatalf("expected repo source github from repo_url fallback, got %q", settings.RepoSource)
	}
	if settings.RepoPath != "/tmp/shared-local" {
		t.Fatalf("expected normalized repo path, got %q", settings.RepoPath)
	}
	if settings.RepoURL != "https://github.com/openvibely/openvibely" {
		t.Fatalf("expected trimmed repo URL, got %q", settings.RepoURL)
	}
	if settings.DefaultAgentConfigID == nil || *settings.DefaultAgentConfigID != "agent-123" {
		t.Fatalf("expected default agent ID to be parsed, got %v", settings.DefaultAgentConfigID)
	}
	if settings.MaxWorkers == nil || *settings.MaxWorkers != 3 {
		t.Fatalf("expected max workers 3, got %v", settings.MaxWorkers)
	}

	_, err = parseProjectFormSettings(tc.echo.NewContext(req, httptest.NewRecorder()), projectFormSettingsOptions{LocalRepoPathEnabled: true})
	if err == nil || !strings.Contains(err.Error(), "GitHub integration is not configured") {
		t.Fatalf("expected github integration validation error, got %v", err)
	}

	missingURLForm := url.Values{}
	missingURLForm.Set("name", "Missing URL")
	missingURLForm.Set("repo_source", "github")
	missingURLReq := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(missingURLForm.Encode()))
	missingURLReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = parseProjectFormSettings(tc.echo.NewContext(missingURLReq, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: true,
		GitHubSvc:            &fakeGitHubService{},
	})
	if err == nil || !strings.Contains(err.Error(), "GitHub URL is required") {
		t.Fatalf("expected github URL validation error, got %v", err)
	}

	localForm := url.Values{}
	localForm.Set("name", "Local Disabled")
	localForm.Set("repo_source", "local")
	localReq := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(localForm.Encode()))
	localReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = parseProjectFormSettings(tc.echo.NewContext(localReq, httptest.NewRecorder()), projectFormSettingsOptions{LocalRepoPathEnabled: false})
	if err == nil || !strings.Contains(err.Error(), "Local repository paths are disabled") {
		t.Fatalf("expected local-disabled validation error, got %v", err)
	}
	settings, err = parseProjectFormSettings(tc.echo.NewContext(localReq, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: false,
		CurrentProject:       &models.Project{RepoPath: "/tmp/legacy", RepoURL: ""},
	})
	if err != nil {
		t.Fatalf("expected legacy local preservation to pass, got %v", err)
	}
	if settings.RepoSource != "local" {
		t.Fatalf("expected local source with legacy allowance, got %q", settings.RepoSource)
	}
	if !settings.PreserveLegacyLocalProject {
		t.Fatal("expected legacy local preservation marker")
	}
}

func TestNormalizePickedProjectFolderPath_Empty(t *testing.T) {
	got, err := normalizePickedProjectFolderPath("   ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestNormalizePickedProjectFolderPath_Absolute(t *testing.T) {
	got, err := normalizePickedProjectFolderPath("/some/absolute/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/some/absolute/path" {
		t.Errorf("expected /some/absolute/path, got %q", got)
	}
}

func TestNormalizePickedProjectFolderPath_Relative_Error(t *testing.T) {
	_, err := normalizePickedProjectFolderPath("relative/path")
	if err == nil {
		t.Error("expected error for relative path, got nil")
	}
}

func TestIsGitHubPATNotConfiguredError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errorString("github personal access token is not configured"), true},
		{errorString("some other error"), false},
		{errorString("GITHUB PERSONAL ACCESS TOKEN IS NOT CONFIGURED"), true}, // case-insensitive
	}
	for _, c := range cases {
		got := isGitHubPATNotConfiguredError(c.err)
		if got != c.want {
			t.Errorf("isGitHubPATNotConfiguredError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestProjectFolderPickerCommandForGOOS_Windows(t *testing.T) {
	name, _, ok := projectFolderPickerCommandForGOOS("windows")
	if !ok {
		t.Error("expected windows to have a picker command")
	}
	if name != "powershell" {
		t.Errorf("expected powershell, got %q", name)
	}
}

func TestProjectFolderPickerCommandForGOOS_Unsupported(t *testing.T) {
	_, _, ok := projectFolderPickerCommandForGOOS("plan9")
	if ok {
		t.Error("expected unsupported OS to return ok=false")
	}
}

// errorString is a simple error type for test cases.
type errorString string

func (e errorString) Error() string { return string(e) }
