package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

const (
	githubPATNotConfiguredErrorFragment = "github personal access token is not configured"
	githubPATSetupLinkURL               = "/channels"
	githubPATSetupLinkText              = "Open Channels"
)

func (h *Handler) Home(c echo.Context) error {
	target := "/chat"
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" {
		target += "?project_id=" + url.QueryEscape(projectID)
	}
	return c.Redirect(http.StatusSeeOther, target)
}

func (h *Handler) Dashboard(c echo.Context) error {
	log.Printf("[handler] Dashboard requested, project_id=%s", c.QueryParam("project_id"))

	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] Dashboard error listing projects: %v", err)
		return err
	}
	log.Printf("[handler] Dashboard found %d projects", len(projects))

	// Default to the first project
	projectID := c.QueryParam("project_id")
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

	counts, _ := h.taskSvc.CountByProjectAndCategory(c.Request().Context(), projectID)
	log.Printf("[handler] Dashboard rendering project=%s counts=%v", projectID, counts)

	return render(c, http.StatusOK, pages.Dashboard(projects, currentProject, counts))
}

func (h *Handler) DashboardMockup(c echo.Context) error {
	log.Printf("[handler] DashboardMockup requested, project_id=%s", c.QueryParam("project_id"))
	return h.renderDashboardMockup(c, "", "", false, "")
}

func (h *Handler) DashboardMockupAction(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		log.Printf("[handler] DashboardMockupAction project resolve error: %v", err)
		return err
	}
	if projectID == "" {
		return h.renderDashboardMockup(c, "No project selected yet. Choose a project first.", "warning", false, "")
	}

	action := strings.TrimSpace(c.FormValue("action"))
	switch action {
	case "power_moment":
		goal := strings.TrimSpace(c.FormValue("goal"))
		if goal == "" {
			return h.renderDashboardMockup(c, "Tell me your goal in one sentence to unlock the power moment.", "warning", false, "")
		}
		msg := "Power moment ready. Your workspace can now guide you from idea to execution."
		return h.renderDashboardMockup(c, msg, "success", true, goal)
	case "reset":
		return h.renderDashboardMockup(c, "Runway reset. Enter a new goal to start again.", "info", false, "")
	default:
		return h.renderDashboardMockup(c, "Unknown action. Please try again.", "warning", false, "")
	}
}

func (h *Handler) renderDashboardMockup(c echo.Context, actionMessage, actionTone string, showPowerMoment bool, goal string) error {
	ctx := c.Request().Context()
	projects, err := h.projectSvc.List(ctx)
	if err != nil {
		log.Printf("[handler] renderDashboardMockup error listing projects: %v", err)
		return err
	}

	projectID := c.QueryParam("project_id")
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

	counts := map[string]int{}
	if projectID != "" {
		counts, _ = h.taskSvc.CountByProjectAndCategory(ctx, projectID)
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.DashboardMockupContent(currentProject, counts, actionMessage, actionTone, showPowerMoment, goal))
	}
	return render(c, http.StatusOK, pages.DashboardMockupPage(projects, currentProject, counts))
}

func (h *Handler) ListProjects(c echo.Context) error {
	log.Printf("[handler] ListProjects requested")
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ListProjects error: %v", err)
		return err
	}
	log.Printf("[handler] ListProjects returning %d projects", len(projects))
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

func (h *Handler) isTaskChangesMergeOptionsEnabled() bool {
	if h.taskChangesMergeOptionsEnabled != nil {
		return *h.taskChangesMergeOptionsEnabled
	}
	return config.ResolveEnableTaskChangesMergeOptions(os.Getenv("OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS"))
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
		log.Printf("[handler] PickProjectFolder error: %v", err)
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
	repoSource := normalizeRepoSource(c.FormValue("repo_source"), c.FormValue("repo_url"))
	repoURL := strings.TrimSpace(c.FormValue("repo_url"))
	if repoSource == "local" && !localRepoPathEnabled {
		return h.projectErrorResponse(c, http.StatusBadRequest, "Local repository paths are disabled in this environment")
	}

	p := &models.Project{
		Name:        c.FormValue("name"),
		Description: c.FormValue("description"),
		RepoPath:    normalizeRepoPathInput(c.FormValue("repo_path")),
		RepoURL:     repoURL,
	}
	if repoSource == "github" {
		p.RepoPath = ""
	}
	if agentID := c.FormValue("default_agent_config_id"); agentID != "" {
		p.DefaultAgentConfigID = &agentID
	}
	if mw := c.FormValue("max_workers"); mw != "" {
		if v, err := strconv.Atoi(mw); err == nil && v > 0 {
			p.MaxWorkers = &v
		}
	}
	log.Printf("[handler] CreateProject name=%q description=%q repo_source=%q repo_path=%q repo_url=%q default_agent=%v max_workers=%v local_repo_path_enabled=%v", p.Name, p.Description, repoSource, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, localRepoPathEnabled)

	if err := h.projectSvc.Create(c.Request().Context(), p); err != nil {
		log.Printf("[handler] CreateProject error: %v", err)
		if isHTMX(c) {
			return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
		}
		return err
	}

	if repoSource == "github" {
		if strings.TrimSpace(p.RepoURL) == "" {
			_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
			return h.projectErrorResponse(c, http.StatusBadRequest, "GitHub URL is required")
		}
		if h.githubSvc == nil {
			_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
			return h.projectErrorResponse(c, http.StatusBadRequest, "GitHub integration is not configured")
		}

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
				log.Printf("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			if strings.Contains(dirPath, "..") {
				errMsg := "Repository path must not contain '..'"
				log.Printf("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				errMsg := fmt.Sprintf("Failed to create directory %s: %v", dirPath, err)
				log.Printf("[handler] CreateProject error: %s", errMsg)
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusBadRequest, errMsg)
			}
			log.Printf("[handler] CreateProject created directory: %s", dirPath)
			p.RepoPath = dirPath
			if err := h.projectSvc.Update(c.Request().Context(), p); err != nil {
				_ = h.projectSvc.Delete(c.Request().Context(), p.ID)
				return h.projectErrorResponse(c, http.StatusInternalServerError, "failed to update repository path")
			}
		}
	}
	log.Printf("[handler] CreateProject success id=%s, redirecting to tasks", p.ID)
	redirectURL := "/tasks?project_id=" + p.ID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", redirectURL)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, redirectURL)
}

func (h *Handler) UpdateProject(c echo.Context) error {
	projectID := c.Param("id")
	log.Printf("[handler] UpdateProject id=%s", projectID)

	p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] UpdateProject fetch error: %v", err)
		return err
	}
	if p == nil {
		log.Printf("[handler] UpdateProject not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	p.Name = c.FormValue("name")
	p.Description = c.FormValue("description")
	localRepoPathEnabled := h.isLocalRepoPathEnabled()
	repoSource := normalizeRepoSource(c.FormValue("repo_source"), c.FormValue("repo_url"))
	currentRepoPath := p.RepoPath
	currentRepoURL := p.RepoURL
	localRepoPath := normalizeRepoPathInput(c.FormValue("repo_path"))
	repoURL := strings.TrimSpace(c.FormValue("repo_url"))

	legacyLocalProject := !localRepoPathEnabled && repoSource == "local" && currentRepoURL == ""
	if repoSource == "local" && !localRepoPathEnabled && !legacyLocalProject {
		return h.projectErrorResponse(c, http.StatusBadRequest, "Local repository paths are disabled in this environment")
	}

	if repoSource == "github" {
		p.RepoURL = repoURL
		if p.RepoURL == "" {
			return h.projectErrorResponse(c, http.StatusBadRequest, "GitHub URL is required")
		}
		if h.githubSvc == nil {
			return h.projectErrorResponse(c, http.StatusBadRequest, "GitHub integration is not configured")
		}
		reclonedPath, normalizedURL, err := h.githubSvc.RecloneProjectRepo(c.Request().Context(), p.ID, currentRepoPath, p.RepoURL)
		if err != nil {
			return h.projectGitHubCloneErrorResponse(c, http.StatusBadRequest, err)
		}
		p.RepoPath = reclonedPath
		p.RepoURL = normalizedURL
	} else if legacyLocalProject {
		// Preserve existing local-path configuration for legacy projects when local paths
		// are disabled in this environment.
		p.RepoPath = currentRepoPath
		p.RepoURL = ""
	} else {
		p.RepoPath = localRepoPath
		p.RepoURL = ""
	}
	if agentID := c.FormValue("default_agent_config_id"); agentID != "" {
		p.DefaultAgentConfigID = &agentID
	} else {
		p.DefaultAgentConfigID = nil
	}
	if mw := c.FormValue("max_workers"); mw != "" {
		if v, err := strconv.Atoi(mw); err == nil && v > 0 {
			p.MaxWorkers = &v
		} else {
			p.MaxWorkers = nil
		}
	} else {
		p.MaxWorkers = nil
	}
	log.Printf("[handler] UpdateProject id=%s name=%q repo_source=%q repo_path=%q repo_url=%q default_agent=%v max_workers=%v local_repo_path_enabled=%v legacy_local_project=%v", projectID, p.Name, repoSource, p.RepoPath, p.RepoURL, p.DefaultAgentConfigID, p.MaxWorkers, localRepoPathEnabled, legacyLocalProject)

	if err := h.projectSvc.Update(c.Request().Context(), p); err != nil {
		log.Printf("[handler] UpdateProject error: %v", err)
		if isHTMX(c) {
			return h.projectErrorResponse(c, http.StatusBadRequest, err.Error())
		}
		return err
	}
	log.Printf("[handler] UpdateProject success id=%s", projectID)

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
	log.Printf("[handler] NewProjectDialog requested")
	agents, _ := h.llmConfigRepo.List(c.Request().Context())
	return render(c, http.StatusOK, pages.NewProjectDialog(agents, h.isLocalRepoPathEnabled()))
}

func (h *Handler) EditProjectDialog(c echo.Context) error {
	projectID := c.Param("id")
	log.Printf("[handler] EditProjectDialog id=%s", projectID)

	p, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] EditProjectDialog fetch error: %v", err)
		return err
	}
	if p == nil {
		log.Printf("[handler] EditProjectDialog not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	agents, _ := h.llmConfigRepo.List(c.Request().Context())

	return render(c, http.StatusOK, pages.EditProjectDialog(p, agents, h.isLocalRepoPathEnabled()))
}

func (h *Handler) DeleteProject(c echo.Context) error {
	projectID := c.Param("id")
	log.Printf("[handler] DeleteProject id=%s", projectID)

	ctx := c.Request().Context()

	p, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		log.Printf("[handler] DeleteProject fetch error: %v", err)
		return err
	}
	if p == nil {
		log.Printf("[handler] DeleteProject not found id=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	if p.IsDefault {
		log.Printf("[handler] DeleteProject refused: cannot delete default project id=%s", projectID)
		return echo.NewHTTPError(http.StatusBadRequest, "cannot delete the default project")
	}

	if err := h.projectSvc.Delete(ctx, projectID); err != nil {
		log.Printf("[handler] DeleteProject error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete project")
	}

	log.Printf("[handler] DeleteProject success id=%s", projectID)

	// Find the default project to redirect to
	projects, _ := h.projectSvc.List(ctx)
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
	log.Printf("[handler] ViewSchedule requested for project_id=%s htmx=%v", projectID, isHTMX)

	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		log.Printf("[handler] ViewSchedule error listing projects: %v", err)
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
		log.Printf("[handler] ViewSchedule project not found: %s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}

	// Get week offset from query params (defaults to 0 = current week)
	weekOffset := 0
	if weekParam := c.QueryParam("week"); weekParam != "" {
		if w, err := strconv.Atoi(weekParam); err == nil {
			weekOffset = w
		}
	}

	log.Printf("[handler] ViewSchedule loading week with offset %d", weekOffset)

	// Get tasks with schedules for this project
	tasks, err := h.taskSvc.GetTasksWithSchedulesByProject(c.Request().Context(), projectID)
	if err != nil {
		log.Printf("[handler] ViewSchedule error fetching tasks with schedules: %v", err)
		return err
	}

	log.Printf("[handler] ViewSchedule found %d tasks with schedules", len(tasks))

	// Get agents for the new scheduled task form
	agents, _ := h.llmConfigRepo.List(c.Request().Context())

	// For HTMX requests, return just the schedule content
	if isHTMX {
		return render(c, http.StatusOK, pages.ScheduleContent(currentProject, tasks, weekOffset, agents))
	}

	return render(c, http.StatusOK, pages.Schedule(projects, currentProject, tasks, weekOffset, agents))
}

func render(c echo.Context, status int, component templ.Component) error {
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().WriteHeader(status)
	return component.Render(c.Request().Context(), c.Response().Writer)
}
