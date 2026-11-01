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
		workspaceEntry := path == "/chat" || path == "/tasks" || path == "/tasks/new" || path == "/analytics" || path == "/automations" || path == "/upcoming" || path == "/history" || path == "/alerts" || path == "/schedule" || path == "/insights" || path == "/skills" || path == "/agents" || path == "/channels"
		if c.Request().Method == http.MethodGet && !isHTMX(c) && workspaceEntry && c.QueryParam("project_id") == "" && h.settingsRepo != nil {
			selected, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
			if err == nil && selected == models.AllProjectsID {
				if path == "/chat" {
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
				query.Set("project_id", models.AllProjectsID)
				c.Request().URL.RawQuery = query.Encode()
			}
		}
		if c.QueryParam("project_id") != models.AllProjectsID {
			return next(c)
		}
		if path == "/chat" || strings.HasPrefix(path, "/chat/") {
			return next(c)
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
			if project == nil || project.ID == models.AllProjectsID {
				return echo.NewHTTPError(http.StatusBadRequest, "Unknown destination project.")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
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
			if definition == nil {
				return echo.NewHTTPError(http.StatusNotFound, "automation not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
			q := c.QueryParams()
			q.Set("project_id", definition.Automation.ProjectID)
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
			if task == nil {
				return echo.NewHTTPError(http.StatusNotFound, "task not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
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
			if task == nil {
				return echo.NewHTTPError(http.StatusNotFound, "task not found")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
			q := c.QueryParams()
			q.Set("project_id", task.ProjectID)
			c.Request().URL.RawQuery = q.Encode()
			return next(c)
		}
		// Aggregate pages use the same handlers and templates as a project.
		c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
		return next(c)
	}
}

// workspaceListReturnURL only accepts a relative list URL for this workspace.
func workspaceListReturnURL(c echo.Context, path, fallback string) string {
	if layout.WorkspaceProjectID(c.Request().Context(), "") != models.AllProjectsID {
		return fallback
	}
	target, err := url.Parse(c.QueryParam("return_list"))
	if err != nil || target.IsAbs() || target.Host != "" || target.Path != path || target.Query().Get("project_id") != models.AllProjectsID {
		return fallback
	}
	return target.String()
}
