package handler

import (
	"context"
	"database/sql"
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
	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	githubPATNotConfiguredErrorFragment = "github personal access token is not configured"
	githubPATSetupLinkURL               = "/channels"
	githubPATSetupLinkText              = "Open Channels"
	projectMaxWorkersMin                = 1
	projectCleanupWarningParam          = "project_cleanup_warning"
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
	GlobalMaxWorkers     int
}

func parseProjectFormSettings(c echo.Context, opts projectFormSettingsOptions) (projectFormSettings, error) {
	return parseProjectFormValues(c.FormValue, opts)
}
func parseProjectFormValues(value func(string) string, opts projectFormSettingsOptions) (projectFormSettings, error) {
	settings := projectFormSettings{
		Name:        strings.TrimSpace(value("name")),
		Description: value("description"),
		RepoSource:  normalizeRepoSource(value("repo_source"), value("repo_url")),
		RepoPath:    normalizeRepoPathInput(value("repo_path")),
		RepoURL:     strings.TrimSpace(value("repo_url")),
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
	if agentID := value("default_agent_config_id"); agentID != "" {
		settings.DefaultAgentConfigID = &agentID
	}
	maxWorkers, err := parseNonNegativeWorkerLimit(value("max_workers"))
	if err != nil {
		return settings, err
	}
	if maxWorkers >= projectMaxWorkersMin {
		if opts.GlobalMaxWorkers > 0 && maxWorkers > opts.GlobalMaxWorkers {
			return settings, fmt.Errorf("Max concurrent workers cannot exceed the global worker limit of %d", opts.GlobalMaxWorkers)
		}
		settings.MaxWorkers = &maxWorkers
	}
	return settings, nil
}

func (h *Handler) isLocalRepoPathEnabled() bool {
	if h.localRepoPathEnabled != nil {
		return *h.localRepoPathEnabled
	}
	return config.ResolveEnableLocalRepoPath(os.Getenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH"))
}

func (h *Handler) configuredGlobalWorkerLimit(ctx context.Context) (int, error) {
	if h.workerRepo != nil {
		maxWorkers, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			return 0, err
		}
		if err := models.ValidateGlobalWorkerLimit(maxWorkers); err != nil {
			return 0, err
		}
		return maxWorkers, nil
	}
	if h.workerSvc != nil {
		return h.workerSvc.NumWorkers(), nil
	}
	return 0, nil
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
	globalMaxWorkers, err := h.configuredGlobalWorkerLimit(c.Request().Context())
	if err != nil {
		return h.projectErrorResponse(c, http.StatusInternalServerError, "failed to read global worker limit")
	}
	settings, err := parseProjectFormSettings(c, projectFormSettingsOptions{
		LocalRepoPathEnabled: localRepoPathEnabled,
		GitHubSvc:            h.githubSvc,
		GlobalMaxWorkers:     globalMaxWorkers,
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
	applyProjectKnowledgeForm(c, p)
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
	if err := h.saveProjectComputerForm(c, p.ID); err != nil {
		return err
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

	form := projectEditArgs{ProjectID: projectID, AgentID: c.FormValue("default_agent_config_id"), Values: map[string]string{}}
	for _, key := range []string{"name", "description", "repo_source", "repo_url", "repo_path", "max_workers"} {
		form.Values[key] = c.FormValue(key)
	}
	knowledge := &models.Project{}
	applyProjectKnowledgeForm(c, knowledge)
	form.Knowledge = knowledge.SkillMemoryPermissions
	if c.FormValue("computer_setting_present") == "1" {
		enabled := c.FormValue("computer_enabled") == "on"
		form.Computer = &enabled
	}
	result, err := actUpdateProject(h, c.Request().Context(), form)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		if result.CloneError {
			return h.projectGitHubCloneErrorResponse(c, result.Code, errors.New(result.Message))
		}
		return h.projectErrorResponse(c, result.Code, result.Message)
	}

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
	globalMaxWorkers, err := h.configuredGlobalWorkerLimit(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.NewProjectDialog(agents, h.isLocalRepoPathEnabled(), globalMaxWorkers))
}

func (h *Handler) RecoverProjectRepo(c echo.Context) error {
	result, err := actRecoverProject(h, c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return c.JSON(result.Code, map[string]string{"error": result.Message})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "healthy"})
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
	globalMaxWorkers, err := h.configuredGlobalWorkerLimit(c.Request().Context())
	if err != nil {
		return err
	}

	repoPathHealth, err := actProjectPathHealth(h, c.Request().Context(), projectPathArgs{ProjectID: projectID})
	if err != nil {
		return err
	}
	enabled := false
	if _, remote := remoteid.ServerOf(projectID); remote {
		if enabled, err = actComputerEnabled(h, c.Request().Context(), ComputerArgs{ProjectID: projectID}); err != nil {
			return err
		}
	} else if h.computerSvc != nil {
		state, err := h.computerSvc.Get(c.Request().Context(), projectID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state != nil {
			enabled = state.Enabled
		}
	}
	_, remoteProject := remoteid.ServerOf(projectID)
	return render(c, http.StatusOK, pages.EditProjectDialog(p, agents, h.isLocalRepoPathEnabled() && !remoteProject, globalMaxWorkers, repoPathHealth, enabled))
}

func (h *Handler) DeleteProject(c echo.Context) error {
	projectID := c.Param("id")
	applog.Infof("[handler] DeleteProject id=%s", projectID)

	ctx := c.Request().Context()

	tab := h.sharedTabOf(ctx, projectID)
	cleanupWarning, err := actDeleteProject(h, ctx, projectID)
	if err != nil {
		return err
	}
	if server, ok := remoteid.ServerOf(projectID); ok {
		h.remoteSessions.serverChanged(principalUserID(ctx), server)
	}
	h.unpinDeletedProject(ctx, projectID, tab)

	// Find the default project to redirect to
	projects, _ := h.projectSvc.ListSelectorOptions(ctx)
	redirectID := ""
	if len(projects) > 0 {
		redirectID = projects[0].ID
	}

	redirectURL := "/tasks?project_id=" + url.QueryEscape(redirectID)
	if cleanupWarning {
		redirectURL += "&" + projectCleanupWarningParam + "=1"
	}
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", redirectURL)
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
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
	currentProject = h.virtualProject(c.Request().Context(), projectID)
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
	agents, _ := h.llmConfigRepo.ListBadgeOptions(c.Request().Context())
	agentDefs := h.listScheduleAgentOptions(c.Request().Context(), projectID)
	calendarState, err := h.scheduleRepo.CalendarState(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	// For HTMX requests, return just the schedule content
	if isHTMX {
		return render(c, http.StatusOK, pages.ScheduleContent(currentProject, tasks, weekOffset, agents, agentDefs, calendarState))
	}

	return render(c, http.StatusOK, pages.Schedule(projects, currentProject, tasks, weekOffset, agents, agentDefs, calendarState))
}

func render(c echo.Context, status int, component templ.Component) error {
	if s := cardcursor.From(c.Request().Context()); s != nil && s.Err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, s.Err.Error())
	}
	ctx := c.Request().Context()
	if h, ok := c.Get("handler").(*Handler); ok {
		ctx = templateui.WithMessageAuthors(ctx, h.authEnabled())
		machineCtx := ctx
		ctx = layout.WithTaskMachineLoader(ctx, func() map[string]layout.TaskMachine {
			machines := map[string]layout.TaskMachine{}
			saved, _ := h.loadSavedServers(machineCtx)
			for _, server := range saved {
				kind := "server"
				if conn := h.remoteHub().Get(principalUserID(machineCtx), server.ID); conn != nil {
					kind = conn.MachineType()
				}
				machines[server.ID] = layout.TaskMachine{Name: server.Name, Kind: kind, Icon: server.Icon}
			}
			return machines
		})

		var servers []layout.RemoteServer
		workspace := layout.WorkspaceProjectID(ctx, c.QueryParam("project_id"))
		if !isHTMX(c) || models.IsSharedProjectID(workspace) {
			servers = h.remoteServerGroups(ctx)
		}
		if workspace == models.AllProjectsID {
			projects, err := h.projectSvc.ListSelectorOptions(ctx)
			if err != nil {
				return err
			}
			ctx = templateui.WithProjectNames(ctx, projects)
		} else if models.IsSharedProjectID(workspace) {
			for _, shared := range h.repoGroups(ctx, servers) {
				if shared.ID == workspace {
					ctx = templateui.WithProjectNames(ctx, sharedMemberOptions(shared))
				}
			}
		}
		ctx = layout.WithDesktopMode(ctx, h.desktopMode)
		ctx = layout.WithBuildVersion(ctx, h.buildIdentity.Version)
		ctx = layout.WithNativeWindow(ctx, h.desktopMode && c.Request().Header.Get("X-OpenVibely-Native-Window") == "1")
		if !isHTMX(c) {
			prefs := h.uiPreferences(ctx)
			ctx = layout.WithUIPreferences(ctx, prefs)
			ctx = layout.WithRemoteServers(ctx, servers)
			ctx = layout.WithSharedProjects(ctx, h.repoGroups(ctx, servers))
		}
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(status)
	return component.Render(ctx, c.Response().Writer)
}

func (h *Handler) saveProjectComputerForm(c echo.Context, projectID string) error {
	if c.FormValue("computer_setting_present") != "1" {
		return nil
	}
	if h.computerSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Project computers unavailable")
	}
	if err := h.computerSvc.SetEnabled(c.Request().Context(), projectID, c.FormValue("computer_enabled") == "on"); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return nil
}

func applyProjectKnowledgeForm(c echo.Context, p *models.Project) {
	if c.FormValue("skill_memory_settings_present") != "true" {
		return
	}
	p.SkillMemoryPermissions = &models.SkillMemoryPermissions{
		AllowGlobalSkillWrites: c.FormValue("allow_global_skill_writes") == "on",
		ReadSkills:             c.FormValue("read_skills") == "on", WriteSkills: c.FormValue("write_skills") == "on",
		ReadMemories: c.FormValue("read_memories") == "on", WriteMemories: c.FormValue("write_memories") == "on",
	}
}
