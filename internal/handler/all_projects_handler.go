package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// allProjectsScope handles the virtual UI scope before project-specific handlers
// can resolve a default project. Only Chat may write to its reserved history owner.
func (h *Handler) allProjectsScope(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Resolve the saved virtual selection before real-project fallback.
		// Only workspace entry pages inherit it; item/API requests keep their
		// existing ownership and scope resolution.
		path := c.Request().URL.Path
		if instanceSettingsPath(path) || instanceAccountProjection(c) {
			return next(c)
		}
		workspaceEntry := path == "/chat" || path == "/tasks" || path == "/tasks/new" || path == "/analytics" || path == "/automations" || path == "/upcoming" || path == "/history" || path == "/alerts" || path == "/schedule" || path == "/skills" || path == "/agents" || path == "/channels"
		if c.Request().Method == http.MethodGet && !isHTMX(c) && workspaceEntry && c.QueryParam("project_id") == "" && h.settingsRepo != nil {
			selected, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
			if err == nil && models.IsAggregateProject(selected) {
				if path == "/chat" && selected == models.AllProjectsID {
					if h.desktopMode && c.Request().URL.RawQuery == "" {
						// Keep the launch URL intact so the shell can restore
						// saved locations and replay any newer preference outbox.
						query := c.QueryParams()
						query.Set("project_id", models.AllProjectsID)
						c.Request().URL.RawQuery = query.Encode()
						return h.Chat(c)
					}
				}
				query := c.QueryParams()
				query.Set("project_id", selected)
				c.Request().URL.RawQuery = query.Encode()
			}
		}
		scope := c.QueryParam("project_id")
		if !models.IsAggregateProject(scope) {
			return next(c)
		}
		var shared *layout.SharedProject
		if scope != models.AllProjectsID {
			shared = h.sharedProject(c.Request().Context(), scope)
			if shared == nil {
				return h.sharedProjectMissing(c)
			}
			if !shared.Combined() {
				q := c.QueryParams()
				q.Set("project_id", shared.PageID())
				c.Request().URL.RawQuery = q.Encode()
				if c.Request().Method == http.MethodGet && !isHTMX(c) {
					return c.Redirect(http.StatusFound, c.Request().URL.RequestURI())
				}
				return next(c)
			}
			c.SetRequest(c.Request().WithContext(models.WithScopeProjects(c.Request().Context(), sharedMemberIDs(shared))))
		}
		member := func(projectID string) bool { return shared == nil || sharedHasMember(shared, projectID) }
		if path == "/chat" || strings.HasPrefix(path, "/chat/") {
			if shared == nil {
				return next(c)
			}
			// Chat history belongs to one machine; the shared page only picks which copy to chat in.
			if path == "/chat" && c.Request().Method == http.MethodGet {
				return h.sharedProjectChat(c, shared)
			}
			return echo.NewHTTPError(http.StatusBadRequest, "Open a project on one machine to chat.")
		}
		// These controls create or configure the shell itself, not a scoped item.
		if path == "/projects/new" || path == "/projects/pick-folder" || path == "/projects" || strings.HasPrefix(path, "/ui/") {
			return next(c)
		}
		if strings.HasPrefix(path, "/api/analytics/") && c.Request().Method == http.MethodGet {
			return next(c)
		}
		if strings.HasPrefix(path, "/breadcrumb-selectors/") && c.Request().Method == http.MethodGet {
			return next(c)
		}
		if path == "/tasks/new" || path == "/automations/new" || ((path == "/tasks" || path == "/automations/builder") && c.Request().Method == http.MethodPost) {
			destination := c.QueryParam("destination_project")
			if (path == "/tasks" || path == "/automations/builder") && c.Request().Method == http.MethodPost {
				destination = c.FormValue("destination_project")
				if _, supplied := c.Request().PostForm["destination_project"]; !supplied {
					destination = c.QueryParam("destination_project")
				}
			}
			if path == "/tasks/new" && destination == "" && c.Request().Method == http.MethodGet {
				return h.NewTask(c)
			}
			if destination == "" && (path == "/automations/new" || (path == "/automations/builder" && !automationBuilderSaveRequested(c))) {
				return next(c)
			}
			if destination == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "Choose a destination project.")
			}
			project, err := h.projectSvc.GetByID(c.Request().Context(), destination)
			if err != nil {
				return err
			}
			if project == nil || models.IsAggregateProject(project.ID) || !member(project.ID) {
				return echo.NewHTTPError(http.StatusBadRequest, "Unknown destination project.")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
			q := c.QueryParams()
			q.Set("project_id", project.ID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		if path == "/automations/yaml/parse" {
			return next(c)
		}
		if strings.HasPrefix(path, "/automations/") && c.Param("automationId") != "" {
			if h.automationGraphSvc == nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "Automations unavailable")
			}
			definition, _, err := h.automationGraphSvc.GetDefinition(c.Request().Context(), models.AllProjectsID, c.Param("automationId"))
			if err != nil {
				return err
			}
			if definition == nil || !member(definition.Automation.ProjectID) {
				return echo.NewHTTPError(http.StatusNotFound, "automation not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
			q := c.QueryParams()
			q.Set("project_id", definition.Automation.ProjectID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		if strings.HasPrefix(path, "/p/") && c.Param("instanceId") != "" {
			instance, err := actPluginInstance(h, c.Request().Context(), PluginRefArgs{InstanceID: c.Param("instanceId")})
			if err != nil {
				return err
			}
			if instance == nil || !member(instance.ProjectID) {
				return pluginNotFound()
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
			q := c.QueryParams()
			q.Set("project_id", instance.ProjectID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		// Task routes resolve their real owner while retaining the virtual
		// workspace for rendering. Never substitute the task's stored ProjectID.
		if strings.HasPrefix(path, "/tasks/") && c.Param("taskId") != "" {
			task, err := h.taskSvc.GetByID(c.Request().Context(), c.Param("taskId"))
			if err != nil {
				return err
			}
			if task == nil || !member(task.ProjectID) {
				return echo.NewHTTPError(http.StatusNotFound, "task not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
			q := c.QueryParams()
			q.Set("project_id", task.ProjectID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		if strings.HasPrefix(path, "/schedules/") && c.Param("id") != "" && c.FormValue("schedule_ids") == "" {
			schedule, err := h.scheduleRepo.GetByID(c.Request().Context(), c.Param("id"))
			if err != nil {
				return err
			}
			if schedule == nil {
				return echo.NewHTTPError(http.StatusNotFound, "schedule not found")
			}
			task, err := h.taskSvc.GetByID(c.Request().Context(), schedule.TaskID)
			if err != nil {
				return err
			}
			if task == nil || !member(task.ProjectID) {
				return echo.NewHTTPError(http.StatusNotFound, "task not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
			q := c.QueryParams()
			q.Set("project_id", task.ProjectID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		// Aggregate pages use the same handlers and templates as a project.
		c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), scope)))
		return next(c)
	}
}

// workspaceListReturnURL only accepts a relative list URL for this workspace.
func workspaceListReturnURL(c echo.Context, path, fallback string) string {
	scope := layout.WorkspaceProjectID(c.Request().Context(), "")
	if !models.IsAggregateProject(scope) {
		return fallback
	}
	target, err := url.Parse(c.QueryParam("return_list"))
	if err != nil || target.IsAbs() || target.Host != "" || target.Path != path || target.Query().Get("project_id") != scope {
		return fallback
	}
	return target.String()
}

// sharedProjectMissing shows a pinned or last open repository tab that waits for
// disconnected saved servers, since its only copies may be on them; otherwise
// it is not found.
func (h *Handler) sharedProjectMissing(c echo.Context) error {
	r := c.Request()
	page := r.Method == http.MethodGet && (isHTMX(c) || strings.Contains(r.Header.Get("Accept"), "text/html"))
	if h.repositoryLookupsPending(r.Context()) {
		return h.repositoryWaiting(c)
	}
	// Only a remembered tab is worth waiting for; other links name nothing reachable.
	if page && h.projectRemembered(r.Context(), c.QueryParam("project_id")) {
		waiting, err := h.waitingServers(r.Context(), h.remoteServerGroups(r.Context()))
		if err != nil {
			return err
		}
		// Only servers that reconnect on their own are worth waiting for.
		userID := principalUserID(r.Context())
		restoring := h.restoring()
		var names []string
		for id, name := range waiting {
			if s := h.remoteSessions.get(userID, id); restoring || s != nil && !s.expired {
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			name := "your servers"
			if len(names) == 1 && names[0] != "" {
				name = names[0]
			}
			return h.serverWaitingNamed(c, name)
		}
	}
	return echo.NewHTTPError(http.StatusNotFound, "project not found")
}

func (h *Handler) repositoryWaiting(c echo.Context) error {
	if c.Request().Method != http.MethodGet {
		return echo.NewHTTPError(http.StatusConflict, "Repository information is still loading; retry shortly.")
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Retry-After", "1")
	if isHTMX(c) {
		return render(c, http.StatusOK, layout.RepositoryWaitingContent())
	}
	if !strings.Contains(c.Request().Header.Get("Accept"), "text/html") {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "Repository information is still loading")
	}
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	current := c.QueryParam("project_id")
	projects = append(projects, models.Project{ID: current, Name: "Loading project"})
	return render(c, http.StatusOK, layout.RepositoryWaiting(projects, current))
}
