package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	githubPATNotConfiguredErrorFragment = "github personal access token is not configured"
	githubPATSetupLinkURL               = "/channels"
	githubPATSetupLinkText              = "Open Channels"
	projectMaxWorkersMin                = 1
	projectMaxWorkersMax                = 10
)

func (h *Handler) Home(c echo.Context) error {
	target := "/chat"
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" {
		target += "?project_id=" + url.QueryEscape(projectID)
	}
	return c.Redirect(http.StatusSeeOther, target)
}

func (h *Handler) ListProjects(c echo.Context) error {
	applog.Infof("[handler] ListProjects requested")
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] ListProjects error: %v", err)
		return err
	}
	applog.Infof("[handler] ListProjects returning %d projects", len(projects))
	return c.JSON(http.StatusOK, projects)
}

func normalizeRepoPathInput(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~\\") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			rest := strings.TrimPrefix(trimmed, "~")
			rest = strings.TrimLeft(rest, "/\\")
			sep := string(os.PathSeparator)
			if runtime.GOOS == "windows" {
				sep = "\\"
			}
			rest = strings.ReplaceAll(rest, "\\", sep)
			rest = strings.ReplaceAll(rest, "/", sep)
			if rest == "" {
				return home
			}
			return filepath.Join(home, rest)
		}
	}
	return trimmed
}

func normalizeRepoSource(repoSource string, repoURL string) string {
	source := strings.ToLower(strings.TrimSpace(repoSource))
	switch source {
	case "github", "local":
		return source
	}
	if strings.TrimSpace(repoURL) != "" {
		return "github"
	}
	return "local"
}

type projectFormSettings struct {
	Name                       string
	Description                string
	RepoSource                 string
	RepoPath                   string
	RepoURL                    string
	DefaultAgentConfigID       *string
	MaxWorkers                 *int
	PreserveLegacyLocalProject bool
}

type projectFormSettingsOptions struct {
	LocalRepoPathEnabled bool
	GitHubSvc            GitHubServiceProvider
	CurrentProject       *models.Project
}

func parseProjectFormSettings(c echo.Context, opts projectFormSettingsOptions) (projectFormSettings, error) {
	settings := projectFormSettings{
		Name:        strings.TrimSpace(c.FormValue("name")),
		Description: c.FormValue("description"),
		RepoSource:  normalizeRepoSource(c.FormValue("repo_source"), c.FormValue("repo_url")),
		RepoPath:    normalizeRepoPathInput(c.FormValue("repo_path")),
		RepoURL:     strings.TrimSpace(c.FormValue("repo_url")),
	}
	if settings.Name == "" {
		return settings, errors.New("Project name is required")
	}
	settings.PreserveLegacyLocalProject = !opts.LocalRepoPathEnabled && settings.RepoSource == "local" && opts.CurrentProject != nil && opts.CurrentProject.RepoURL == ""
	if settings.RepoSource == "local" && !opts.LocalRepoPathEnabled && !settings.PreserveLegacyLocalProject {
		return settings, errors.New("Local repository paths are disabled in this environment")
	}
	if settings.RepoSource == "github" {
		if settings.RepoURL == "" {
			return settings, errors.New("GitHub URL is required")
		}
		if opts.GitHubSvc == nil {
			return settings, errors.New("GitHub integration is not configured")
		}
	}
	if agentID := c.FormValue("default_agent_config_id"); agentID != "" {
		settings.DefaultAgentConfigID = &agentID
	}
	if mw := strings.TrimSpace(c.FormValue("max_workers")); mw != "" {
		v, err := strconv.Atoi(mw)
		if err != nil {
			return settings, fmt.Errorf("Max concurrent workers must be a number from %d to %d, or 0 for no project limit", projectMaxWorkersMin, projectMaxWorkersMax)
		}
		if v < 0 || v > projectMaxWorkersMax {
			return settings, fmt.Errorf("Max concurrent workers must be between %d and %d, or 0 for no project limit", projectMaxWorkersMin, projectMaxWorkersMax)
		}
		if v >= projectMaxWorkersMin {
			settings.MaxWorkers = &v
		}
	}
	return settings, nil
}

func (h *Handler) isLocalRepoPathEnabled() bool {
	if h.localRepoPathEnabled != nil {
		return *h.localRepoPathEnabled
	}
	return config.ResolveEnableLocalRepoPath(os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH"))
}

var errProjectFolderPickerUnavailable = errors.New("project folder picker is unavailable on this operating system")

func projectFolderPickerCommandForGOOS(goos string) (name string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return "osascript", []string{
			"-e", `set chosenFolder to choose folder with prompt "Select Project Repository Folder"`,
			"-e", `POSIX path of chosenFolder`,
		}, true
	case "linux":
		if _, err := exec.LookPath("zenity"); err == nil {
			return "zenity", []string{"--file-selection", "--directory", "--title=Select Project Repository Folder"}, true
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			return "kdialog", []string{"--getexistingdirectory", "", "Select Project Repository Folder"}, true
		}
		return "", nil, false
	case "windows":
		script := `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8` + "\n" +
			`Add-Type -AssemblyName System.Windows.Forms` + "\n" +
			`$dialog = New-Object System.Windows.Forms.FolderBrowserDialog` + "\n" +
			`$dialog.Description = "Select Project Repository Folder"` + "\n" +
			`$dialog.ShowNewFolderButton = $true` + "\n" +
			`$result = $dialog.ShowDialog()` + "\n" +
			`if ($result -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.WriteLine($dialog.SelectedPath) }`
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}, true
	default:
		return "", nil, false
	}
}

func normalizePickedProjectFolderPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	path = normalizeRepoPathInput(path)
	path = filepath.Clean(path)
	if runtime.GOOS != "windows" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("native folder picker returned a non-absolute path")
	}
	return path, nil
}

func pickProjectFolderNative(ctx context.Context) (string, bool, error) {
	cmdName, cmdArgs, ok := projectFolderPickerCommandForGOOS(runtime.GOOS)
	if !ok {
		return "", false, errProjectFolderPickerUnavailable
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		outText := strings.TrimSpace(string(out))
		lowerOut := strings.ToLower(outText)
		lowerErr := strings.ToLower(err.Error())
		if outText == "" {
			outText = err.Error()
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		isCancelExit := exitCode == 1 && (cmdName == "osascript" || cmdName == "zenity" || cmdName == "kdialog")
		if strings.Contains(lowerOut, "user canceled") || strings.Contains(lowerOut, "was cancelled") || strings.Contains(lowerOut, "cancelled") || strings.Contains(lowerOut, "canceled") || isCancelExit {
			return "", true, nil
		}
		if strings.Contains(lowerOut, "command not found") || strings.Contains(lowerErr, "executable file not found") {
			return "", false, errProjectFolderPickerUnavailable
		}
		return "", false, fmt.Errorf("native folder picker failed: %s", outText)
	}

	path, normErr := normalizePickedProjectFolderPath(string(out))
	if normErr != nil {
		return "", false, normErr
	}
	if strings.TrimSpace(path) == "" {
		return "", true, nil
	}
	return path, false, nil
}

func (h *Handler) PickProjectFolder(c echo.Context) error {
	if !h.isLocalRepoPathEnabled() {
		return echo.NewHTTPError(http.StatusForbidden, "Local repository paths are disabled in this environment")
	}

	picker := h.projectFolderPicker
	if picker == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "project folder picker is not configured")
	}

	path, canceled, err := picker(c.Request().Context())
	if err != nil {
		if errors.Is(err, errProjectFolderPickerUnavailable) {
			return c.JSON(http.StatusNotImplemented, map[string]any{
				"selected": false,
				"error":    "Native folder picker is unavailable on this system. Paste an absolute path manually.",
			})
		}
		applog.Infof("[handler] PickProjectFolder error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if canceled || strings.TrimSpace(path) == "" {
		return c.JSON(http.StatusOK, map[string]any{
			"selected": false,
			"canceled": true,
		})
	}

	normalizedPath, normErr := normalizePickedProjectFolderPath(path)
	if normErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, normErr.Error())
	}
	if strings.TrimSpace(normalizedPath) == "" {
		return c.JSON(http.StatusOK, map[string]any{
			"selected": false,
			"canceled": true,
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"selected": true,
		"path":     normalizedPath,
	})
}

func (h *Handler) CreateProject(c echo.Context) error {
	localRepoPathEnabled := h.isLocalRepoPathEnabled()
	settings, err := parseProjectFormSettings(c, projectFormSettingsOptions{
		LocalRepoPathEnabled: localRepoPathEnabled,
		GitHubSvc:            h.githubSvc,
	})
	if err != nil {
		return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
	}

	p := &models.Project{
		Name:                 settings.Name,
		Description:          settings.Description,
		RepoPath:             settings.RepoPath,
		RepoURL:              settings.RepoURL,
		DefaultAgentConfigID: settings.DefaultAgentConfigID,
		MaxWorkers:           settings.MaxWorkers,
	}
	if settings.RepoSource == "github" {
		p.RepoPath = ""
	}
	applog.Infof("[handler] CreateProject name=%q description=%q repo_source=%q repo_path=%q repo_url=%q default_agent=%v max_workers=%v local_repo_path_enabled=%v", p.Name, p.Description, settings.RepoSource, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, localRepoPathEnabled)

	if err := h.projectSvc.Create(c.Request().Context(), p); err != nil {
		applog.Infof("[handler] CreateProject error: %v", err)
		if isHTMX(c) {
			return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
		}
		return err
	}

	if settings.RepoSource == "github" {
		var clonedPath, normalizedURL string
		clonedPath, normalizedURL, err := h.githubSvc.CloneProjectRepo(c.Request().Context(), p.ID, p.RepoURL)
		if err != nil {
			_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
			return h.projectGitHubCloneErrorResponse(c, http.StatusBadRequest, err)
		}
		p.RepoPath = clonedPath
		p.RepoURL = normalizedURL
		if err := h.projectSvc.Update(c.Request().Context(), p); err != nil {
			_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
			return h.projectErrorResponse(c, http.StatusInternalServerError, "failed to save cloned repository settings")
		}
	} else {
		// Create directory if requested
		createDir := c.FormValue("create_directory") == "true"
		if createDir && p.RepoPath != "" {
			dirPath := filepath.Clean(p.RepoPath)
			if !filepath.IsAbs(dirPath) {
				errMsg := fmt.Sprintf("Repository path must be an absolute path: %s", dirPath)
				applog.Infof("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			if strings.Contains(dirPath, "..") {
				errMsg := "Repository path must not contain '..'"
				applog.Infof("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				errMsg := fmt.Sprintf("Failed to create directory %s: %v", dirPath, err)
				applog.Infof("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			applog.Infof("[handler] CreateProject created directory: %s", dirPath)
			p.RepoPath = dirPath
			if err := h.projectSvc.Update(c.Request().Context(), p); err != nil {
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusInternalServerError, "failed to update repository path")
			}
		}
	}
	if h.memorySvc != nil {
		if err := h.memorySvc.EnsureProject(c.Request().Context(), p.ID); err != nil {
			applog.Infof("[handler] CreateProject warning: memory setup failed project=%s: %v", p.ID, err)
		}
	}
	if h.agentLibraryMaintenanceSvc != nil {
		if err := h.agentLibraryMaintenanceSvc.EnsureProject(c.Request().Context(), p.ID); err != nil {
			applog.Infof("[handler] CreateProject warning: agent library maintenance setup failed project=%s: %v", p.ID, err)
		}
	}
	applog.Infof("[handler] CreateProject success id=%s, redirecting to tasks", p.ID)
	redirectURL := "/tasks?project_id=" + p.ID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", redirectURL)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *Handler) UpdateProject(c echo.Context) error {
	projectID := c.Param("id")
	applog.Infof("[handler] UpdateProject id=%s", projectID)

	p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] UpdateProject fetch error: %v", err)
		return err
	}
	if p == nil {
		applog.Infof("[handler] UpdateProject not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	localRepoPathEnabled := h.isLocalRepoPathEnabled()
	currentRepoPath := p.RepoPath
	settings, err := parseProjectFormSettings(c, projectFormSettingsOptions{
		LocalRepoPathEnabled: localRepoPathEnabled,
		GitHubSvc:            h.githubSvc,
		CurrentProject:       p,
	})
	if err != nil {
		return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
	}

	p.Name = settings.Name
	p.Description = settings.Description
	p.DefaultAgentConfigID = settings.DefaultAgentConfigID
	p.MaxWorkers = settings.MaxWorkers
	if settings.RepoSource == "github" {
		p.RepoURL = settings.RepoURL
		reclonedPath, normalizedURL, err := h.githubSvc.RecloneProjectRepo(c.Request().Context(), p.ID, currentRepoPath, p.RepoURL)
		if err != nil {
			return h.projectGitHubCloneErrorResponse(c, http.StatusBadRequest, err)
		}
		p.RepoPath = reclonedPath
		p.RepoURL = normalizedURL
	} else if settings.PreserveLegacyLocalProject {
		// Preserve existing local-path configuration for legacy projects when local paths
		// are disabled in this environment.
		p.RepoPath = currentRepoPath
		p.RepoURL = ""
	} else {
		p.RepoPath = settings.RepoPath
		p.RepoURL = ""
	}
	applog.Infof("[handler] UpdateProject id=%s name=%q repo_source=%q repo_path=%q repo_url=%q default_agent=%v max_workers=%v local_repo_path_enabled=%v legacy_local_project=%v", projectID, p.Name, settings.RepoSource, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, localRepoPathEnabled, settings.PreserveLegacyLocalProject)

	if err := h.projectSvc.Update(c.Request().Context(), p); err != nil {
		applog.Infof("[handler] UpdateProject error: %v", err)
		if isHTMX(c) {
			return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
		}
		return err
	}
	applog.Infof("[handler] UpdateProject success id=%s", projectID)

	// Return to current page
	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+projectID)
}

func (h *Handler) projectErrorResponse(c echo.Context, status int, message string) error {
	if isHTMX(c) {
		setHTMXToast(c, message, "failed")
		return c.NoContent(http.StatusNoContent)
	}
	return echo.NewHTTPError(status, message)
}

func (h *Handler) projectGitHubCloneErrorResponse(c echo.Context, status int, cloneErr error) error {
	message := fmt.Sprintf("failed to clone GitHub repository: %v", cloneErr)
	if isHTMX(c) && isGitHubPATNotConfiguredError(cloneErr) {
		setHTMXToastWithLink(c, message, "failed", githubPATSetupLinkURL, githubPATSetupLinkText)
		return c.NoContent(http.StatusNoContent)
	}
	return h.projectErrorResponse(c, status, message)
}

func isGitHubPATNotConfiguredError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), githubPATNotConfiguredErrorFragment)
}

func (h *Handler) NewProjectDialog(c echo.Context) error {
	applog.Infof("[handler] NewProjectDialog requested")
	agents, _ := h.llmConfigRepo.ListChatSelectionOptions(c.Request().Context())
	return render(c, http.StatusOK, pages.NewProjectDialog(agents, h.isLocalRepoPathEnabled()))
}

func (h *Handler) EditProjectDialog(c echo.Context) error {
	projectID := c.Param("id")
	applog.Infof("[handler] EditProjectDialog id=%s", projectID)

	p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] EditProjectDialog fetch error: %v", err)
		return err
	}
	if p == nil {
		applog.Infof("[handler] EditProjectDialog not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	agents, _ := h.llmConfigRepo.ListChatSelectionOptions(c.Request().Context())

	return render(c, http.StatusOK, pages.EditProjectDialog(p, agents, h.isLocalRepoPathEnabled()))
}

func (h *Handler) DeleteProject(c echo.Context) error {
	projectID := c.Param("id")
	applog.Infof("[handler] DeleteProject id=%s", projectID)

	ctx := c.Request().Context()

	p, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] DeleteProject fetch error: %v", err)
		return err
	}
	if p == nil {
		applog.Infof("[handler] DeleteProject not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	if p.IsDefault {
		applog.Infof("[handler] DeleteProject refused: cannot delete default project id=%s", projectID)
		return echo.NewHTTPError(http.StatusBadRequest, "cannot delete the default project")
	}

	if err := h.projectSvc.Delete(ctx, projectID); err != nil {
		applog.Infof("[handler] DeleteProject error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete project")
	}

	applog.Infof("[handler] DeleteProject success id=%s", projectID)

	// Find the default project to redirect to
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	redirectID := ""
	if len(projects) > 0 {
		redirectID = projects[0].ID
	}

	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", "/tasks?project_id="+redirectID)
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+redirectID)
}

func (h *Handler) ViewSchedule(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	isHTMX := isHTMX(c)
	applog.Infof("[handler] ViewSchedule requested for project_id=%s htmx=%v", projectID, isHTMX)

	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] ViewSchedule error listing projects: %v", err)
		return err
	}

	// Default to first project if not specified
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	for i := range projects {
		if projects[i].ID == projectID {
			currentProject = &projects[i]
			break
		}
	}

	if currentProject == nil {
		applog.Infof("[handler] ViewSchedule project not found: %s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	// Get week offset from query params (defaults to 0 = current week)
	weekOffset := 0
	if weekParam := c.QueryParam("week"); weekParam != "" {
		if w, err := strconv.Atoi(weekParam); err == nil {
			weekOffset = w
		}
	}

	applog.Infof("[handler] ViewSchedule loading week with offset %d", weekOffset)

	// Get tasks with schedules for this project
	tasks, err := h.taskSvc.GetTasksWithSchedulesByProject(c.Request().Context(), projectID)
	if err != nil {
		applog.Infof("[handler] ViewSchedule error fetching tasks with schedules: %v", err)
		return err
	}

	applog.Infof("[handler] ViewSchedule found %d tasks with schedules", len(tasks))

	// Keep model configurations and primary Agent definitions as separate choices.
	agents, _ := h.llmConfigRepo.List(c.Request().Context())
	agentDefs := h.listTaskFormAgentDefinitions(c.Request().Context(), projectID, nil)

	// For HTMX requests, return just the schedule content
	if isHTMX {
		return render(c, http.StatusOK, pages.ScheduleContent(currentProject, tasks, weekOffset, agents, agentDefs))
	}

	return render(c, http.StatusOK, pages.Schedule(projects, currentProject, tasks, weekOffset, agents, agentDefs))
}

func render(c echo.Context, status int, component templ.Component) error {
	ctx := c.Request().Context()
	if h, ok := c.Get("handler").(*Handler); ok {
		ctx = layout.WithDesktopMode(ctx, h.desktopMode)
		if !isHTMX(c) {
			ctx = layout.WithUIPreferences(ctx, h.uiPreferences(ctx))
		}
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(status)
	return component.Render(ctx, c.Response().Writer)
}
