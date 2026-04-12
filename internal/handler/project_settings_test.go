package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestProjectSettings_EndToEnd tests the complete flow of editing project settings
// including default AI model and max concurrent workers
func TestProjectSettings_EndToEnd(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	// Setup broadcaster
	broadcaster := events.NewBroadcaster()

	// Setup repositories
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)

	// Setup services (need to setup in correct order due to dependencies)
	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, projectRepo) // 0 workers for testing
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// Create handler
	h := New(
		projectSvc,
		taskSvc,
		llmSvc,
		workerSvc,
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		taskRepo,
		scheduleRepo,
		execRepo,
		workerRepo,
		attachmentRepo,
		chatAttachmentRepo,
		nil, // projectRepo
		nil, // settingsRepo
		broadcaster,
		nil, // telegramSvc
	)

	e := echo.New()

	// Create a project
	project := &models.Project{
		Name:        "Test Project",
		Description: "Test project for settings",
		RepoPath:    "/Users/dubee/go/src/github.com/claude-code",
	}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create two LLM configs to test selection
	config1 := &models.LLMConfig{
		Name:      "Opus Config",
		Provider:  models.ProviderTest,
		Model:     "claude-opus-4",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, config1); err != nil {
		t.Fatalf("failed to create config1: %v", err)
	}

	config2 := &models.LLMConfig{
		Name:      "Sonnet Config",
		Provider:  models.ProviderTest,
		Model:     "claude-sonnet-3.5",
		IsDefault: false,
	}
	if err := llmConfigRepo.Create(ctx, config2); err != nil {
		t.Fatalf("failed to create config2: %v", err)
	}

	// Test 1: Get the edit dialog
	t.Run("GetEditDialog", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/projects/"+project.ID+"/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		body := rec.Body.String()
		// Check that the dialog contains the model selector
		if !strings.Contains(body, "Default Model") {
			t.Error("dialog should contain 'Default Model' label")
		}
		// Check that the dialog contains max workers selector
		if !strings.Contains(body, "Max Concurrent Workers") {
			t.Error("dialog should contain 'Max Concurrent Workers' label")
		}
		// Check that both configs are in the dropdown
		if !strings.Contains(body, "Opus Config") {
			t.Error("dialog should contain 'Opus Config' option")
		}
		if !strings.Contains(body, "Sonnet Config") {
			t.Error("dialog should contain 'Sonnet Config' option")
		}
		// Regression: directory selection UI should use folder-selection wording, not upload
		if !strings.Contains(body, "Choose Folder") {
			t.Error("dialog should contain 'Choose Folder' action for repo path")
		}
		if strings.Contains(body, ">Upload<") {
			t.Error("dialog should not contain upload action text for repo path picker")
		}
		// Regression: local picker should use native endpoint wiring
		if !strings.Contains(body, `value="/Users/dubee/go/src/github.com/claude-code"`) {
			t.Error("dialog should render saved absolute repo path exactly")
		}
		if !strings.Contains(body, "function chooseDirectoryNative()") {
			t.Error("dialog should include native folder picker function")
		}
		if !strings.Contains(body, "fetch('/projects/pick-folder'") {
			t.Error("dialog should call /projects/pick-folder for native folder selection")
		}
		if !strings.Contains(body, "Native folder picker is unavailable on this system. Paste an absolute path manually.") {
			t.Error("dialog should show cross-platform native picker unavailable guidance")
		}
		if !strings.Contains(body, "function isAbsolutePath(pathValue)") {
			t.Error("dialog should guard repo path picker assignments to absolute paths only")
		}
		if !strings.Contains(body, "if (!input || !isAbsolutePath(selectedPath)) return;") {
			t.Error("dialog should ignore non-absolute selected paths")
		}
		if strings.Contains(body, "function chooseDirectoryWithChromeFileSystem()") {
			t.Error("dialog should not use legacy chrome.fileSystem picker path")
		}
		if !strings.Contains(body, "Use native folder picker when available.") {
			t.Error("dialog should include native picker helper text")
		}
	})

	// Test 2: Update project settings with default model and max workers
	t.Run("UpdateProjectSettings", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Updated Project")
		form.Set("description", "Updated description")
		form.Set("repo_path", "/tmp/updated")
		form.Set("default_agent_config_id", config2.ID) // Set Sonnet as default
		form.Set("max_workers", "5")                    // Set max workers to 5

		req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true") // Simulate HTMX request
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.UpdateProject(c); err != nil {
			t.Fatalf("UpdateProject failed: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		// Verify the project was updated in the database
		updated, err := projectRepo.GetByID(ctx, project.ID)
		if err != nil {
			t.Fatalf("failed to get updated project: %v", err)
		}

		if updated.Name != "Updated Project" {
			t.Errorf("expected name 'Updated Project', got %q", updated.Name)
		}

		if updated.RepoPath != "/tmp/updated" {
			t.Errorf("expected repo path '/tmp/updated', got %q", updated.RepoPath)
		}

		if updated.DefaultAgentConfigID == nil {
			t.Fatal("expected default_agent_config_id to be set")
		}
		if *updated.DefaultAgentConfigID != config2.ID {
			t.Errorf("expected default_agent_config_id %q, got %q", config2.ID, *updated.DefaultAgentConfigID)
		}

		if updated.MaxWorkers == nil {
			t.Fatal("expected max_workers to be set")
		}
		if *updated.MaxWorkers != 5 {
			t.Errorf("expected max_workers 5, got %d", *updated.MaxWorkers)
		}
	})

	// Test 3: Clear settings (set to nil)
	t.Run("ClearProjectSettings", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Updated Project")
		form.Set("description", "Updated description")
		form.Set("repo_path", "/tmp/updated")
		// Don't set default_agent_config_id or max_workers - should clear them

		req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.UpdateProject(c); err != nil {
			t.Fatalf("UpdateProject failed: %v", err)
		}

		// Verify settings were cleared
		updated, err := projectRepo.GetByID(ctx, project.ID)
		if err != nil {
			t.Fatalf("failed to get updated project: %v", err)
		}

		if updated.DefaultAgentConfigID != nil {
			t.Errorf("expected default_agent_config_id to be nil, got %q", *updated.DefaultAgentConfigID)
		}

		if updated.MaxWorkers != nil {
			t.Errorf("expected max_workers to be nil, got %d", *updated.MaxWorkers)
		}
	})

	// Test 4: Set max_workers to 0 (should clear it, not set to 0)
	t.Run("SetMaxWorkersToZero", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Updated Project")
		form.Set("description", "Updated description")
		form.Set("repo_path", "/tmp/updated")
		form.Set("max_workers", "0") // 0 should clear the limit

		req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.UpdateProject(c); err != nil {
			t.Fatalf("UpdateProject failed: %v", err)
		}

		// Verify max_workers is nil (no limit)
		updated, err := projectRepo.GetByID(ctx, project.ID)
		if err != nil {
			t.Fatalf("failed to get updated project: %v", err)
		}

		if updated.MaxWorkers != nil {
			t.Errorf("expected max_workers to be nil when set to 0, got %d", *updated.MaxWorkers)
		}
	})
}

// TestNewProjectDialog tests the new project creation dialog endpoint
func TestNewProjectDialog(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	e := echo.New()
	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		nil, // settingsRepo
		nil, // broadcaster
		nil, // telegramSvc
	)

	// Create LLM configs
	config1 := &models.LLMConfig{
		Name:      "Opus Config",
		Provider:  models.ProviderTest,
		Model:     "claude-opus-4",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, config1); err != nil {
		t.Fatalf("failed to create config1: %v", err)
	}
	config2 := &models.LLMConfig{
		Name:      "Sonnet Config",
		Provider:  models.ProviderTest,
		Model:     "claude-sonnet-3.5",
		IsDefault: false,
	}
	if err := llmConfigRepo.Create(ctx, config2); err != nil {
		t.Fatalf("failed to create config2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/projects/new", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.NewProjectDialog(c); err != nil {
		t.Fatalf("NewProjectDialog failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	// Check that the dialog contains the model selector
	if !strings.Contains(body, "Default Model") {
		t.Error("dialog should contain 'Default Model' label")
	}
	// Check that the dialog contains max workers selector
	if !strings.Contains(body, "Max Concurrent Workers") {
		t.Error("dialog should contain 'Max Concurrent Workers' label")
	}
	// Check that both configs are in the dropdown
	if !strings.Contains(body, "Opus Config") {
		t.Error("dialog should contain 'Opus Config' option")
	}
	if !strings.Contains(body, "Sonnet Config") {
		t.Error("dialog should contain 'Sonnet Config' option")
	}
	// Check it has the form fields for name and repo path
	if !strings.Contains(body, `name="name"`) {
		t.Error("dialog should contain name input")
	}
	if !strings.Contains(body, `name="repo_path"`) {
		t.Error("dialog should contain repo_path input")
	}
	// Check that "Use global default" option exists
	if !strings.Contains(body, "Use global default") {
		t.Error("dialog should contain 'Use global default' option")
	}
	// Check that "No limit (use global)" option exists
	if !strings.Contains(body, "No limit (use global)") {
		t.Error("dialog should contain 'No limit (use global)' option")
	}
	// Regression: directory selection UI should use folder-selection wording, not upload
	if !strings.Contains(body, "Choose Folder") {
		t.Error("new project dialog should contain 'Choose Folder' action for repo path")
	}
	if strings.Contains(body, ">Upload<") {
		t.Error("new project dialog should not contain upload action text for repo path picker")
	}
	// Regression: local picker should use native endpoint wiring
	if !strings.Contains(body, "function chooseDirectoryNative()") {
		t.Error("new project dialog should include native folder picker function")
	}
	if !strings.Contains(body, "fetch('/projects/pick-folder'") {
		t.Error("new project dialog should call /projects/pick-folder for native folder selection")
	}
	if !strings.Contains(body, "Native folder picker is unavailable on this system. Paste an absolute path manually.") {
		t.Error("new project dialog should show cross-platform native picker unavailable guidance")
	}
	if !strings.Contains(body, "function isAbsolutePath(pathValue)") {
		t.Error("new project dialog should guard repo path picker assignments to absolute paths only")
	}
	if !strings.Contains(body, "if (!repoPathInput || !isAbsolutePath(selectedPath)) return;") {
		t.Error("new project dialog should ignore non-absolute selected paths")
	}
	if strings.Contains(body, "function chooseDirectoryWithChromeFileSystem()") {
		t.Error("new project dialog should not use legacy chrome.fileSystem picker path")
	}
	if !strings.Contains(body, "Use native folder picker when available.") {
		t.Error("new project dialog should include native picker helper text")
	}
}

func TestProjectDialogs_GitHubOnlyModeHidesLocalSourceOption(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	t.Setenv("ENVIRONMENT", "production")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	e := echo.New()
	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		nil, // settingsRepo
		nil, // broadcaster
		nil, // telegramSvc
	)

	t.Run("new dialog github only", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/projects/new", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.NewProjectDialog(c); err != nil {
			t.Fatalf("NewProjectDialog failed: %v", err)
		}
		body := rec.Body.String()
		if strings.Contains(body, `<option value="local"`) {
			t.Fatal("new project dialog should not render Local Path option when local mode is disabled")
		}
		if strings.Contains(body, "Choose Folder") {
			t.Fatal("new project dialog should not render Choose Folder button when local mode is disabled")
		}
		if !strings.Contains(body, "Local repository paths are disabled in this environment.") {
			t.Fatal("new project dialog should explain local path mode is disabled")
		}
	})

	t.Run("edit legacy local project shows source selector with github option", func(t *testing.T) {
		project := &models.Project{
			Name:        "Legacy Local",
			Description: "legacy",
			RepoPath:    "/tmp/legacy",
			RepoURL:     "",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/projects/"+project.ID+"/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}
		body := rec.Body.String()
		// Should show a repo source selector (not locked to local)
		if !strings.Contains(body, `<option value="github"`) {
			t.Fatal("edit dialog for legacy local project should offer GitHub URL option when local mode is disabled")
		}
		if !strings.Contains(body, `<option value="local"`) {
			t.Fatal("edit dialog for legacy local project should keep Local Path option as current selection")
		}
		// Should NOT show Choose Folder (local editing is disabled)
		if strings.Contains(body, "Choose Folder") {
			t.Fatal("edit dialog should not render Choose Folder button when local mode is disabled")
		}
		// Should show the current local path as read-only
		if !strings.Contains(body, "/tmp/legacy") {
			t.Fatal("edit dialog should display existing local path")
		}
		// Should have GitHub URL input (hidden initially)
		if !strings.Contains(body, `id="edit-project-repo-url"`) {
			t.Fatal("edit dialog should contain GitHub URL input for switching")
		}
		// Should have guidance about switching
		if !strings.Contains(body, "Switch to GitHub URL") {
			t.Fatal("edit dialog should explain GitHub URL switching option")
		}
	})

	t.Run("edit github project shows github url when local disabled", func(t *testing.T) {
		project := &models.Project{
			Name:        "GitHub Project",
			Description: "github",
			RepoPath:    "/repos/github-project",
			RepoURL:     "https://github.com/owner/repo",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/projects/"+project.ID+"/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}
		body := rec.Body.String()
		// Should show GitHub URL input directly (no selector needed)
		if !strings.Contains(body, `value="https://github.com/owner/repo"`) {
			t.Fatal("edit dialog should pre-fill existing GitHub URL")
		}
		// Should NOT have a local path option
		if strings.Contains(body, `<option value="local"`) {
			t.Fatal("edit dialog for github project should not show local path option when local mode is disabled")
		}
	})
}

// TestUpdateProject_SwitchLocalToGitHub_LocalDisabled tests that a legacy local project
// can be switched to GitHub URL source even when local repo path mode is disabled.
func TestUpdateProject_SwitchLocalToGitHub_LocalDisabled(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	e := echo.New()
	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		nil, // settingsRepo
		nil, // broadcaster
		nil, // telegramSvc
	)

	// A legacy local project can submit repo_source=local and preserve local path
	t.Run("legacy local project preserves local path on save", func(t *testing.T) {
		project := &models.Project{
			Name:     "Legacy Local Save",
			RepoPath: "/tmp/legacy-save",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		form := url.Values{}
		form.Set("name", "Legacy Local Save Updated")
		form.Set("description", "updated")
		form.Set("repo_source", "local")
		form.Set("repo_path", "/tmp/legacy-save")

		req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.UpdateProject(c); err != nil {
			t.Fatalf("UpdateProject failed: %v", err)
		}

		updated, err := projectRepo.GetByID(ctx, project.ID)
		if err != nil {
			t.Fatalf("failed to get updated project: %v", err)
		}
		if updated.RepoPath != "/tmp/legacy-save" {
			t.Errorf("expected preserved RepoPath, got %q", updated.RepoPath)
		}
		if updated.RepoURL != "" {
			t.Errorf("expected empty RepoURL, got %q", updated.RepoURL)
		}
	})

	// Switching to github requires a github service; without it we get an error,
	// but the handler should NOT reject the request for the source switch itself.
	t.Run("switch to github source accepted by handler", func(t *testing.T) {
		project := &models.Project{
			Name:     "Switch To GitHub",
			RepoPath: "/tmp/switch-to-github",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		form := url.Values{}
		form.Set("name", "Switch To GitHub")
		form.Set("description", "switching")
		form.Set("repo_source", "github")
		form.Set("repo_url", "https://github.com/owner/repo")

		req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		// This should fail because githubSvc is nil, but the failure should be
		// "GitHub integration is not configured", NOT "Local repository paths are disabled"
		err := h.UpdateProject(c)
		_ = err // HTMX path returns via toast, check response
		body := rec.Body.String()
		_ = body
		// The handler should reach the github clone path (not the local-disabled rejection).
		// With nil githubSvc it returns a toast error about GitHub integration, not about local paths.
		// Check that we didn't get the local-disabled error
		toastHeader := rec.Header().Get("HX-Trigger")
		if strings.Contains(toastHeader, "Local repository paths are disabled") {
			t.Fatal("handler should not reject github source switch with local-disabled error")
		}
		if !strings.Contains(toastHeader, "GitHub integration is not configured") {
			t.Fatalf("expected GitHub integration error, got HX-Trigger: %s", toastHeader)
		}
	})
}

// TestProjectCreate_WithDefaultSettings tests creating a project without specifying model/workers
func TestProjectCreate_WithDefaultSettings(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	e := echo.New()
	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		nil, // settingsRepo
		nil, // broadcaster
		nil, // telegramSvc
	)

	t.Run("AbsolutePathPreservedNoCwdPrefix", func(t *testing.T) {
		const specifiedPath = "/Users/dubee/go/src/github.com/claude-code"

		form := url.Values{}
		form.Set("name", "Absolute Path Project")
		form.Set("description", "Project with exact absolute path")
		form.Set("repo_path", specifiedPath)

		req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.CreateProject(c); err != nil {
			t.Fatalf("CreateProject failed: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect status, got %d", rec.Code)
		}

		projects, err := projectRepo.List(ctx)
		if err != nil {
			t.Fatalf("failed to list projects: %v", err)
		}

		var created *models.Project
		for i := range projects {
			if projects[i].Name == "Absolute Path Project" {
				created = &projects[i]
				break
			}
		}
		if created == nil {
			t.Fatal("project was not created")
		}
		if created.RepoPath != specifiedPath {
			t.Fatalf("expected RepoPath=%q, got %q", specifiedPath, created.RepoPath)
		}
		if strings.Contains(created.RepoPath, "/openvibely/openvibely/") {
			t.Fatalf("repo path should not be prefixed with current repository root, got %q", created.RepoPath)
		}
	})

	t.Run("TildePathExpandedOnCreate", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("home dir unavailable")
		}
		form := url.Values{}
		form.Set("name", "Tilde Path Project")
		form.Set("description", "Project with tilde path")
		form.Set("repo_path", "~/go/src/github.com/claude-code")

		req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.CreateProject(c); err != nil {
			t.Fatalf("CreateProject failed: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect status, got %d", rec.Code)
		}

		projects, err := projectRepo.List(ctx)
		if err != nil {
			t.Fatalf("failed to list projects: %v", err)
		}
		var created *models.Project
		for i := range projects {
			if projects[i].Name == "Tilde Path Project" {
				created = &projects[i]
				break
			}
		}
		if created == nil {
			t.Fatal("project was not created")
		}
		want := filepath.Join(home, "go", "src", "github.com", "claude-code")
		if created.RepoPath != want {
			t.Fatalf("expected RepoPath=%q, got %q", want, created.RepoPath)
		}
	})

	t.Run("RelativePathRemainsRelative", func(t *testing.T) {
		const relativePath = "claude-code"

		form := url.Values{}
		form.Set("name", "Relative Path Project")
		form.Set("description", "Project with relative path")
		form.Set("repo_path", relativePath)

		req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.CreateProject(c); err != nil {
			t.Fatalf("CreateProject failed: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect status, got %d", rec.Code)
		}

		projects, err := projectRepo.List(ctx)
		if err != nil {
			t.Fatalf("failed to list projects: %v", err)
		}

		var created *models.Project
		for i := range projects {
			if projects[i].Name == "Relative Path Project" {
				created = &projects[i]
				break
			}
		}
		if created == nil {
			t.Fatal("project was not created")
		}
		if created.RepoPath != relativePath {
			t.Fatalf("expected relative RepoPath=%q, got %q", relativePath, created.RepoPath)
		}
	})

	t.Run("TildePathExpandedOnUpdate", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			t.Skip("home dir unavailable")
		}

		projectToUpdate := &models.Project{Name: "Update Tilde", RepoPath: "/tmp/original"}
		if err := projectRepo.Create(ctx, projectToUpdate); err != nil {
			t.Fatalf("failed to create update target project: %v", err)
		}

		form := url.Values{}
		form.Set("name", "Update Tilde")
		form.Set("description", "Updated with tilde")
		form.Set("repo_path", "~/go/src/github.com/claude-code")

		req := httptest.NewRequest(http.MethodPut, "/projects/"+projectToUpdate.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(projectToUpdate.ID)

		if err := h.UpdateProject(c); err != nil {
			t.Fatalf("UpdateProject failed: %v", err)
		}
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect status, got %d", rec.Code)
		}

		updated, err := projectRepo.GetByID(ctx, projectToUpdate.ID)
		if err != nil {
			t.Fatalf("failed to fetch updated project: %v", err)
		}
		want := filepath.Join(home, "go", "src", "github.com", "claude-code")
		if updated.RepoPath != want {
			t.Fatalf("expected updated RepoPath=%q, got %q", want, updated.RepoPath)
		}
	})

	t.Run("CreateWithDefaultsStillLeavesOptionalFieldsNil", func(t *testing.T) {
		form := url.Values{}
		form.Set("name", "Default Settings Project")
		form.Set("description", "Project with default settings")
		form.Set("repo_path", "/tmp/default")

		req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.CreateProject(c); err != nil {
			t.Fatalf("CreateProject failed: %v", err)
		}

		projects, err := projectRepo.List(ctx)
		if err != nil {
			t.Fatalf("failed to list projects: %v", err)
		}

		var created *models.Project
		for i := range projects {
			if projects[i].Name == "Default Settings Project" {
				created = &projects[i]
				break
			}
		}
		if created == nil {
			t.Fatal("project was not created")
		}

		if created.DefaultAgentConfigID != nil {
			t.Errorf("expected default_agent_config_id to be nil, got %q", *created.DefaultAgentConfigID)
		}
		if created.MaxWorkers != nil {
			t.Errorf("expected max_workers to be nil, got %d", *created.MaxWorkers)
		}
	})
}

// TestProjectCreate_WithSettings tests creating a project with initial settings
func TestProjectCreate_WithSettings(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	e := echo.New()
	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		nil, // settingsRepo
		nil, // broadcaster
		nil, // telegramSvc
	)

	// Create an LLM config
	config := &models.LLMConfig{
		Name:      "Test Config",
		Provider:  models.ProviderTest,
		Model:     "claude-sonnet-3.5",
		IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, config); err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	// Create project with settings
	form := url.Values{}
	form.Set("name", "New Project")
	form.Set("description", "New project description")
	form.Set("repo_path", "/tmp/new")
	form.Set("default_agent_config_id", config.ID)
	form.Set("max_workers", "3")

	req := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.CreateProject(c); err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// Find the created project
	projects, err := projectRepo.List(ctx)
	if err != nil {
		t.Fatalf("failed to list projects: %v", err)
	}

	var created *models.Project
	for i := range projects {
		if projects[i].Name == "New Project" {
			created = &projects[i]
			break
		}
	}

	if created == nil {
		t.Fatal("project was not created")
	}

	// Verify settings
	if created.DefaultAgentConfigID == nil {
		t.Fatal("expected default_agent_config_id to be set")
	}
	if *created.DefaultAgentConfigID != config.ID {
		t.Errorf("expected default_agent_config_id %q, got %q", config.ID, *created.DefaultAgentConfigID)
	}

	if created.MaxWorkers == nil {
		t.Fatal("expected max_workers to be set")
	}
	if *created.MaxWorkers != 3 {
		t.Errorf("expected max_workers 3, got %d", *created.MaxWorkers)
	}
}

func TestDeleteProject(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := service.NewWorkerService(llmSvc, 0, projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)

	h := New(
		projectSvc,
		taskSvc,
		llmSvc,
		workerSvc,
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		taskRepo,
		scheduleRepo,
		execRepo,
		workerRepo,
		attachmentRepo,
		chatAttachmentRepo,
		nil, // projectRepo
		nil, // settingsRepo
		broadcaster,
		nil, // telegramSvc
	)

	e := echo.New()

	t.Run("DeleteNonDefaultProject", func(t *testing.T) {
		// Create a non-default project
		project := &models.Project{
			Name:        "To Delete",
			Description: "Will be deleted",
			RepoPath:    "/tmp/delete-me",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		// Create a task in the project so we verify cascade
		_, err := db.ExecContext(ctx,
			`INSERT INTO tasks (id, project_id, title, prompt) VALUES ('task-del-1', ?, 'Test Task', 'do something')`,
			project.ID)
		if err != nil {
			t.Fatalf("failed to create task: %v", err)
		}

		// Delete the project via HTMX request
		req := httptest.NewRequest(http.MethodDelete, "/projects/"+project.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.DeleteProject(c); err != nil {
			t.Fatalf("DeleteProject failed: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		// Verify HX-Redirect header is set
		redirect := rec.Header().Get("HX-Redirect")
		if redirect == "" {
			t.Error("expected HX-Redirect header to be set")
		}

		// Verify project is gone
		deleted, err := projectRepo.GetByID(ctx, project.ID)
		if err != nil {
			t.Fatalf("error checking deleted project: %v", err)
		}
		if deleted != nil {
			t.Error("expected project to be deleted but it still exists")
		}

		// Verify task was cascade-deleted
		task, err := taskRepo.GetByID(ctx, "task-del-1")
		if err != nil {
			t.Fatalf("error checking deleted task: %v", err)
		}
		if task != nil {
			t.Error("expected task to be cascade-deleted but it still exists")
		}
	})

	t.Run("CannotDeleteDefaultProject", func(t *testing.T) {
		// The default project has id 'default' from migrations
		req := httptest.NewRequest(http.MethodDelete, "/projects/default", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("default")

		err := h.DeleteProject(c)
		if err == nil {
			t.Fatal("expected error when deleting default project")
		}

		// Should return 400 Bad Request
		httpErr, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected echo.HTTPError, got %T", err)
		}
		if httpErr.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", httpErr.Code)
		}

		// Verify default project still exists
		defaultProject, err := projectRepo.GetByID(ctx, "default")
		if err != nil {
			t.Fatalf("error checking default project: %v", err)
		}
		if defaultProject == nil {
			t.Error("default project should still exist")
		}
	})

	t.Run("DeleteNonExistentProject", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/projects/nonexistent", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("nonexistent")

		err := h.DeleteProject(c)
		if err == nil {
			t.Fatal("expected error when deleting non-existent project")
		}

		httpErr, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected echo.HTTPError, got %T", err)
		}
		if httpErr.Code != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", httpErr.Code)
		}
	})

	t.Run("DeleteProjectNonHTMX", func(t *testing.T) {
		// Create another project to delete
		project := &models.Project{
			Name:        "To Delete Non-HTMX",
			Description: "Will be deleted via non-HTMX",
			RepoPath:    "/tmp/delete-non-htmx",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		req := httptest.NewRequest(http.MethodDelete, "/projects/"+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.DeleteProject(c); err != nil {
			t.Fatalf("DeleteProject failed: %v", err)
		}

		// Non-HTMX should redirect
		if rec.Code != http.StatusSeeOther {
			t.Errorf("expected status 303, got %d", rec.Code)
		}

		location := rec.Header().Get("Location")
		if !strings.Contains(location, "/tasks?project_id=") {
			t.Errorf("expected redirect to /tasks?project_id=..., got %q", location)
		}
	})

	t.Run("EditDialogShowsDeleteForNonDefault", func(t *testing.T) {
		project := &models.Project{
			Name:        "Editable Project",
			Description: "Has delete button",
			RepoPath:    "/tmp/editable",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/projects/"+project.ID+"/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Delete Project") {
			t.Error("edit dialog for non-default project should contain 'Delete Project' button")
		}
		if !strings.Contains(body, "Delete Permanently") {
			t.Error("edit dialog for non-default project should contain confirmation modal")
		}
	})

	t.Run("EditDialogHidesDeleteForDefault", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/projects/default/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("default")

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}

		body := rec.Body.String()
		if strings.Contains(body, "Delete Project") {
			t.Error("edit dialog for default project should NOT contain 'Delete Project' button")
		}
	})
}

// TestGlobalPersonality tests saving, updating, and clearing the global personality setting
func TestGlobalPersonality(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")

	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)

	h := New(
		projectSvc,
		nil, // taskSvc
		nil, // llmSvc
		nil, // workerSvc
		nil, // schedulerSvc
		nil, // alertSvc
		nil, // upcomingSvc
		nil, // workflowSvc
		nil, // collisionSvc
		nil, // insightsSvc
		nil, // architectSvc
		nil, // backlogSvc
		nil, // autonomousTriggerSvc
		nil, // trendSvc
		nil, // templateSvc
		nil, // patternSvc
		llmConfigRepo,
		nil, // taskRepo
		nil, // scheduleRepo
		nil, // execRepo
		nil, // workerRepo
		nil, // attachmentRepo
		nil, // chatAttachmentRepo
		nil, // projectRepo
		settingsRepo,
		nil, // broadcaster
		nil, // telegramSvc
	)

	e := echo.New()

	// Test 1: Default personality is empty
	t.Run("DefaultPersonalityEmpty", func(t *testing.T) {
		val, err := settingsRepo.Get(ctx, "personality")
		if err != nil {
			t.Fatalf("failed to get personality: %v", err)
		}
		if val != "" {
			t.Errorf("expected default personality to be empty, got %q", val)
		}
	})

	// Test 2: Save personality via handlePersonalitySave (non-HTMX)
	t.Run("SavePersonality", func(t *testing.T) {
		form := url.Values{}
		form.Set("personality", "pirate_captain")

		req := httptest.NewRequest(http.MethodPost, "/channels/personality", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.handlePersonalitySave(c); err != nil {
			t.Fatalf("handlePersonalitySave failed: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		// Verify personality was saved to app_settings
		val, err := settingsRepo.Get(ctx, "personality")
		if err != nil {
			t.Fatalf("failed to get personality: %v", err)
		}
		if val != "pirate_captain" {
			t.Errorf("expected personality 'pirate_captain', got %q", val)
		}
	})

	// Test 2b: Save personality via HTMX returns re-rendered section
	t.Run("SavePersonalityHTMX", func(t *testing.T) {
		form := url.Values{}
		form.Set("personality", "zen_debugger")

		req := httptest.NewRequest(http.MethodPost, "/personality/save", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.handlePersonalitySave(c); err != nil {
			t.Fatalf("handlePersonalitySave HTMX failed: %v", err)
		}

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", rec.Code)
		}

		body := rec.Body.String()
		// HTMX response should contain the personality section with cards
		if !strings.Contains(body, "personality-section") {
			t.Error("HTMX personality save should return personality section")
		}

		// Verify setting was saved
		val, err := settingsRepo.Get(ctx, "personality")
		if err != nil {
			t.Fatalf("failed to get personality: %v", err)
		}
		if val != "zen_debugger" {
			t.Errorf("expected personality 'zen_debugger', got %q", val)
		}
	})

	// Test 3: Change personality to different value
	t.Run("ChangePersonality", func(t *testing.T) {
		form := url.Values{}
		form.Set("personality", "zen_debugger")

		req := httptest.NewRequest(http.MethodPost, "/channels/personality", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.handlePersonalitySave(c); err != nil {
			t.Fatalf("handlePersonalitySave failed: %v", err)
		}

		val, err := settingsRepo.Get(ctx, "personality")
		if err != nil {
			t.Fatalf("failed to get personality: %v", err)
		}
		if val != "zen_debugger" {
			t.Errorf("expected personality 'zen_debugger', got %q", val)
		}
	})

	// Test 4: Clear personality (set to default)
	t.Run("ClearPersonality", func(t *testing.T) {
		form := url.Values{}
		form.Set("personality", "") // Empty = default

		req := httptest.NewRequest(http.MethodPost, "/channels/personality", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.handlePersonalitySave(c); err != nil {
			t.Fatalf("handlePersonalitySave failed: %v", err)
		}

		val, err := settingsRepo.Get(ctx, "personality")
		if err != nil {
			t.Fatalf("failed to get personality: %v", err)
		}
		if val != "" {
			t.Errorf("expected personality to be empty, got %q", val)
		}
	})

	// Test 5: getPersonalityContext reads from global settings
	t.Run("GetPersonalityContextFromGlobal", func(t *testing.T) {
		// Set personality in global settings
		if err := settingsRepo.Set(ctx, "personality", "sarcastic_engineer"); err != nil {
			t.Fatalf("failed to set personality: %v", err)
		}

		result := h.getPersonalityContext(ctx, "any-project-id")
		if result == "" {
			t.Error("expected non-empty personality context")
		}
		if !strings.Contains(result, "Communication Style") {
			t.Error("expected personality context to contain 'Communication Style' header")
		}
		if !strings.Contains(result, "sarcastic") {
			t.Error("expected personality context to contain sarcastic engineer prompt")
		}
	})

	// Test 6: getPersonalityContext returns empty for no personality set
	t.Run("GetPersonalityContextEmpty", func(t *testing.T) {
		if err := settingsRepo.Set(ctx, "personality", ""); err != nil {
			t.Fatalf("failed to clear personality: %v", err)
		}

		result := h.getPersonalityContext(ctx, "any-project-id")
		if result != "" {
			t.Errorf("expected empty personality context, got %q", result)
		}
	})

	// Test 7: Settings page shows personality cards
	t.Run("SettingsPageShowsPersonalityCards", func(t *testing.T) {
		// Set a personality to verify it shows as active
		if err := settingsRepo.Set(ctx, "personality", "pirate_captain"); err != nil {
			t.Fatalf("failed to set personality: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/personality", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.handleAppSettings(c); err != nil {
			t.Fatalf("handleAppSettings failed: %v", err)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Personality") {
			t.Error("page should contain 'Personality' heading")
		}
		if !strings.Contains(body, "+ Add Personality") {
			t.Error("page should contain '+ Add Personality' button")
		}
		if !strings.Contains(body, "Sarcastic Engineer") {
			t.Error("page should contain 'Sarcastic Engineer' card")
		}
		if !strings.Contains(body, "Pirate Captain") {
			t.Error("page should contain 'Pirate Captain' card")
		}
		if !strings.Contains(body, "Base") {
			t.Error("page should contain 'Base' card")
		}
		if !strings.Contains(body, "No personality prompt applied.") {
			t.Error("page should contain default card placeholder text")
		}
		// Pirate Captain is active — should have Active badge
		if !strings.Contains(body, "Active") {
			t.Error("page should contain Active badge for selected personality")
		}
		// Should have prompt snippet preview
		if !strings.Contains(body, "pirate") {
			t.Error("page should contain prompt snippet preview for pirate personality")
		}
		// Default card should not be clickable (no onclick="editPersonalityFromData")
		// We check that the default card doesn't have the edit onclick
		if !strings.Contains(body, `data-personality-is-default-card="true"`) {
			t.Error("page should mark default card as non-clickable")
		}
		// Should contain the modal for create/edit
		if !strings.Contains(body, "personality_modal") {
			t.Error("page should contain personality create/edit modal")
		}
		if !strings.Contains(body, "System Prompt") {
			t.Error("page should show system prompt field in modal")
		}
	})

	// Test 8: Edit dialog does NOT show personality dropdown
	t.Run("EditDialogNoPersonality", func(t *testing.T) {
		// Create a project for the edit dialog
		project := &models.Project{
			Name:        "Test Project",
			Description: "Test",
			RepoPath:    "/tmp/test",
		}
		if err := projectSvc.Create(ctx, project); err != nil {
			t.Fatalf("failed to create project: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/projects/"+project.ID+"/edit", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(project.ID)

		if err := h.EditProjectDialog(c); err != nil {
			t.Fatalf("EditProjectDialog failed: %v", err)
		}

		body := rec.Body.String()
		if strings.Contains(body, "Chat Personality") {
			t.Error("edit dialog should NOT contain 'Chat Personality' — it's now a global setting")
		}
	})
}
