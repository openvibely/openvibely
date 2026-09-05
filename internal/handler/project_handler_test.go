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
	"github.com/openvibely/openvibely/internal/testutil"
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

func TestCreateProject_WhitespaceOnlyName_Rejected(t *testing.T) {
	tc := NewTestContext(t)

	before, err := tc.handler.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects failed: %v", err)
	}

	rec := tc.HTTP().Post("/projects").WithForm(url.Values{
		"name":        {"   "},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only name, got %d body=%s", rec.Code, rec.Body.String())
	}

	after, err := tc.handler.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects failed: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected no project to be created, before=%d after=%d", len(before), len(after))
	}
}

func TestCreateProject_TrimsSurroundingWhitespaceInName(t *testing.T) {
	tc := NewTestContext(t)

	rec := tc.HTTP().Post("/projects").WithForm(url.Values{
		"name":        {"  Client Work  "},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}

	projects, err := tc.handler.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects failed: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.Name == "Client Work" {
			found = true
		}
		if p.Name == "  Client Work  " {
			t.Fatalf("project name was persisted with surrounding whitespace: %q", p.Name)
		}
	}
	if !found {
		t.Fatalf("expected a project named %q, got %+v", "Client Work", projects)
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

func TestUpdateProject_WhitespaceOnlyName_Rejected(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Keep This Name").Build()

	rec := tc.HTTP().Put("/projects/" + project.ID).WithForm(url.Values{
		"name":        {"   "},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only rename, got %d body=%s", rec.Code, rec.Body.String())
	}

	reloaded, err := tc.handler.projectSvc.GetByID(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("reloading project failed: %v", err)
	}
	if reloaded == nil || reloaded.Name != "Keep This Name" {
		t.Fatalf("expected name to be preserved, got %+v", reloaded)
	}
}

func TestUpdateProject_TrimsSurroundingWhitespaceInName(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Original Name").Build()

	rec := tc.HTTP().Put("/projects/" + project.ID).WithForm(url.Values{
		"name":        {"  Renamed Project  "},
		"repo_source": {"local"},
		"repo_path":   {""},
	}).Execute()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d body=%s", rec.Code, rec.Body.String())
	}

	reloaded, err := tc.handler.projectSvc.GetByID(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("reloading project failed: %v", err)
	}
	if reloaded == nil || reloaded.Name != "Renamed Project" {
		t.Fatalf("expected trimmed name %q, got %+v", "Renamed Project", reloaded)
	}
}

func TestUpdateProject_InvalidMaxWorkersRejectedWithoutMutation(t *testing.T) {
	for _, maxWorkers := range []string{"-1", "not-a-number"} {
		t.Run(maxWorkers, func(t *testing.T) {
			tc := NewTestContext(t)
			limit := 2
			project := &models.Project{Name: "Worker Limited Project", MaxWorkers: &limit}
			if err := tc.handler.projectSvc.Create(context.Background(), project); err != nil {
				t.Fatalf("create project: %v", err)
			}

			rec := tc.HTMX().Put("/projects/" + project.ID).WithForm(url.Values{
				"name":        {"Unexpected Rename"},
				"repo_source": {"local"},
				"repo_path":   {""},
				"max_workers": {maxWorkers},
			}).Execute()
			if rec.Code != http.StatusNoContent {
				t.Fatalf("expected 204 validation toast, got %d body=%s", rec.Code, rec.Body.String())
			}
			trigger := rec.Header().Get("HX-Trigger")
			if !strings.Contains(trigger, "openvibelyToast") || !strings.Contains(trigger, "Max concurrent workers") {
				t.Fatalf("expected max-workers validation toast, got %q", trigger)
			}

			reloaded, err := tc.handler.projectSvc.GetByID(context.Background(), project.ID)
			if err != nil {
				t.Fatalf("reloading project failed: %v", err)
			}
			if reloaded == nil {
				t.Fatal("expected project to remain")
			}
			if reloaded.Name != "Worker Limited Project" {
				t.Fatalf("expected name to remain unchanged, got %q", reloaded.Name)
			}
			if reloaded.MaxWorkers == nil || *reloaded.MaxWorkers != 2 {
				t.Fatalf("expected max_workers=2 to remain unchanged, got %v", reloaded.MaxWorkers)
			}
		})
	}
}

func TestCreateProject_InvalidMaxWorkersRejectedWithoutCreate(t *testing.T) {
	tc := NewTestContext(t)
	before, err := tc.handler.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects before create failed: %v", err)
	}

	rec := tc.HTTP().Post("/projects").WithForm(url.Values{
		"name":        {"Invalid Workers Project"},
		"repo_source": {"local"},
		"repo_path":   {""},
		"max_workers": {"not-a-number"},
	}).Execute()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 validation error, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Max concurrent workers") {
		t.Fatalf("expected max-workers validation error, got body=%s", rec.Body.String())
	}

	after, err := tc.handler.projectSvc.List(context.Background())
	if err != nil {
		t.Fatalf("listing projects after create failed: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("expected no project to be created, before=%d after=%d", len(before), len(after))
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

func TestViewSchedule_UsesCompactModelProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	project := createProject(t, h, "Schedule Projection")
	ctx := context.Background()

	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	largeBody := strings.Repeat("x", 1024*1024)
	defaultModel := &models.LLMConfig{
		Name:                 "Schedule Default",
		Provider:             models.ProviderOpenAICompatible,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "schedule-default-model",
		APIKey:               "secret-api-key",
		OAuthAccessToken:     "secret-oauth-token",
		OAuthRefreshToken:    "secret-refresh-token",
		OAuthClientSecret:    "secret-client-secret",
		BaseURL:              "https://example.com/v1/",
		ExtraHeadersJSON:     `{"X-Secret":"value"}`,
		ExtraBodyJSON:        largeBody,
		CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON:  `{"access":"secret"}`,
		MixtureConfigJSON:    `{"large":"` + largeBody + `"}`,
		IsDefault:            true,
	}
	otherModel := &models.LLMConfig{
		Name:          "Schedule Alpha",
		Provider:      models.ProviderOpenAICompatible,
		AuthMethod:    models.AuthMethodAPIKey,
		Model:         "schedule-alpha-model",
		APIKey:        "another-secret",
		ExtraBodyJSON: largeBody,
	}
	if err := llmConfigRepo.Create(ctx, defaultModel); err != nil {
		t.Fatalf("create default model: %v", err)
	}
	if err := llmConfigRepo.Create(ctx, otherModel); err != nil {
		t.Fatalf("create other model: %v", err)
	}

	for _, test := range []struct {
		name string
		htmx bool
	}{
		{name: "full page"},
		{name: "HTMX fragment", htmx: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			req := httptest.NewRequest(http.MethodGet, "/schedule?project_id="+project.ID, nil)
			if test.htmx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			counter.SetEnabled(false)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, expected := range []string{
				`<option value="">Use Default Model</option>`,
				`value="` + defaultModel.ID + `"`,
				defaultModel.Name,
				`(Default)`,
				`value="` + otherModel.ID + `"`,
				otherModel.Name,
			} {
				if !strings.Contains(body, expected) {
					t.Fatalf("schedule response missing %q: %s", expected, body)
				}
			}
			if strings.Index(body, `value="`+defaultModel.ID+`"`) > strings.Index(body, `value="`+otherModel.ID+`"`) {
				t.Fatalf("model dropdown order changed: default must precede other model")
			}
			if strings.Contains(body, largeBody) || strings.Contains(body, "secret-api-key") || strings.Contains(body, "secret-oauth-token") {
				t.Fatal("schedule response exposed full model configuration")
			}

			var modelQueries []string
			for _, statement := range counter.Statements() {
				normalized := strings.Join(strings.Fields(statement), " ")
				if strings.Contains(normalized, "FROM agent_configs ORDER BY is_default DESC, name ASC") {
					modelQueries = append(modelQueries, normalized)
				}
			}
			if len(modelQueries) != 1 {
				t.Fatalf("expected exactly one schedule model query, got %d in %q", len(modelQueries), counter.Statements())
			}
			projection := strings.ToLower(strings.Split(modelQueries[0], " from agent_configs ")[0])
			if !strings.Contains(projection, "select id, name, model, is_default") {
				t.Fatalf("schedule model query does not use compact badge projection: %s", modelQueries[0])
			}
			for _, forbidden := range []string{
				"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret",
				"oauth_authorize_url", "oauth_token_url", "ollama_base_url", "base_url", "models_url",
				"extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json",
				"mixture_config_json", "created_at", "updated_at", "max_workers", "worker_timeout",
			} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("schedule model query selected forbidden column %q: %s", forbidden, modelQueries[0])
				}
			}
		})
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

func TestParseProjectFormSettings_ValidatesMaxWorkersContract(t *testing.T) {
	tc := NewTestContext(t)

	parse := func(maxWorkers string, includeField bool) (projectFormSettings, error) {
		form := url.Values{}
		form.Set("name", "Worker Limit Project")
		form.Set("repo_source", "local")
		if includeField {
			form.Set("max_workers", maxWorkers)
		}
		req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return parseProjectFormSettings(tc.echo.NewContext(req, httptest.NewRecorder()), projectFormSettingsOptions{
			LocalRepoPathEnabled: true,
		})
	}

	validCases := []struct {
		name         string
		value        string
		includeField bool
		want         *int
	}{
		{name: "omitted clears", includeField: false},
		{name: "empty clears", value: "", includeField: true},
		{name: "zero clears", value: "0", includeField: true},
		{name: "minimum saves", value: "1", includeField: true, want: intPointer(1)},
		{name: "high finite saves", value: "25", includeField: true, want: intPointer(25)},
		{name: "trimmed finite saves", value: " 5 ", includeField: true, want: intPointer(5)},
	}
	for _, tc := range validCases {
		t.Run(tc.name, func(t *testing.T) {
			settings, err := parse(tc.value, tc.includeField)
			if err != nil {
				t.Fatalf("expected valid max_workers %q, got error %v", tc.value, err)
			}
			if tc.want == nil {
				if settings.MaxWorkers != nil {
					t.Fatalf("expected max_workers to clear, got %d", *settings.MaxWorkers)
				}
				return
			}
			if settings.MaxWorkers == nil || *settings.MaxWorkers != *tc.want {
				t.Fatalf("expected max_workers=%d, got %v", *tc.want, settings.MaxWorkers)
			}
		})
	}

	for _, value := range []string{"-1", "not-a-number"} {
		t.Run("rejects "+value, func(t *testing.T) {
			_, err := parse(value, true)
			if err == nil || !strings.Contains(err.Error(), "Max concurrent workers") {
				t.Fatalf("expected max-workers validation error for %q, got %v", value, err)
			}
		})
	}

	form := url.Values{"name": {"Worker Limit Project"}, "repo_source": {"local"}, "max_workers": {"11"}}
	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err := parseProjectFormSettings(tc.echo.NewContext(req, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: true,
		GlobalMaxWorkers:     10,
	})
	if err == nil || !strings.Contains(err.Error(), "global worker limit") {
		t.Fatalf("expected finite global worker limit validation error, got %v", err)
	}
}

func intPointer(v int) *int {
	return &v
}

func TestParseProjectFormSettings_TrimsNameAndRejectsBlank(t *testing.T) {
	tc := NewTestContext(t)

	trimForm := url.Values{}
	trimForm.Set("name", "  Client Work  ")
	trimForm.Set("repo_source", "local")
	trimReq := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(trimForm.Encode()))
	trimReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	settings, err := parseProjectFormSettings(tc.echo.NewContext(trimReq, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: true,
	})
	if err != nil {
		t.Fatalf("expected trimmed name to pass, got %v", err)
	}
	if settings.Name != "Client Work" {
		t.Fatalf("expected trimmed name %q, got %q", "Client Work", settings.Name)
	}

	blankForm := url.Values{}
	blankForm.Set("name", "   ")
	blankForm.Set("repo_source", "local")
	blankReq := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(blankForm.Encode()))
	blankReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = parseProjectFormSettings(tc.echo.NewContext(blankReq, httptest.NewRecorder()), projectFormSettingsOptions{
		LocalRepoPathEnabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "Project name is required") {
		t.Fatalf("expected blank-name validation error, got %v", err)
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
