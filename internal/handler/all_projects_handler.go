package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// allProjectsScope handles the virtual UI scope before project-specific handlers
// can resolve a default project. Only Chat may write to its reserved history owner.
func (h *Handler) allProjectsScope(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Resolve the saved virtual selection before real-project fallback.
		// Only workspace entry pages inherit it; item/API requests keep their
		// existing ownership and scope resolution.
		path := c.Request().URL.Path
		workspaceEntry := path == "/chat" || path == "/tasks" || path == "/tasks/new" || path == "/analytics" || path == "/automations"
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
			if path == "/tasks" && c.Request().Method == http.MethodPost {
				destination = c.FormValue("destination_project")
				if destination == "" {
					destination = c.QueryParam("destination_project")
				}
			}
			if path == "/tasks/new" && destination == "" && c.Request().Method == http.MethodGet {
				return h.NewTask(c)
			}
			if destination == "" {
				if c.Request().Method == http.MethodGet {
					return h.AllProjectsChooseProject(c)
				}
				return echo.NewHTTPError(http.StatusBadRequest, "Choose a destination project.")
			}
			project, err := h.projectSvc.GetByID(c.Request().Context(), destination)
			if err != nil {
				return err
			}
			if project == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "Unknown destination project.")
			}
			c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(c.Request().Context(), models.AllProjectsID)))
			q := c.QueryParams()
			q.Set("project_id", project.ID)
			c.Request().URL.RawQuery = q.Encode()
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
		if c.Request().Method != http.MethodGet {
			return echo.NewHTTPError(http.StatusBadRequest, "Choose a project before making changes.")
		}
		switch path {
		case "/tasks", "/automations", "/analytics":
			return h.AllProjects(c)
		case "/tasks/sidebar":
			return h.TaskListPanel(c)
		case "/alerts/unread-count":
			return c.NoContent(http.StatusOK)
		case "/events":
			// Legacy SSE endpoint; notifications use the shared WebSocket transport.
			return c.NoContent(http.StatusNoContent)
		}

		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/events") {
			return echo.NewHTTPError(http.StatusBadRequest, "Choose a project for this operation.")
		}
		return h.AllProjectsChooseProject(c)
	}
}

func (h *Handler) AllProjects(c echo.Context) error {
	ctx := c.Request().Context()
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	data := pages.AllProjectsData{Projects: projects, Section: strings.TrimPrefix(c.Request().URL.Path, "/"), Search: strings.TrimSpace(c.QueryParam("search")), FilterProject: c.QueryParam("filter_project"), Range: c.QueryParam("range")}
	if data.Section == "chat" {
		// Desktop launch bootstrap; the shell restores the remembered page.
		data.Section = "tasks"
	}
	projectID := models.AllProjectsID
	if data.FilterProject != "" {
		found := false
		for _, project := range projects {
			if project.ID == data.FilterProject {
				found = true
				break
			}
		}
		if !found {
			// A remembered filter can outlive its project. Clear the stale
			// filter and page boundary rather than preventing tab navigation.
			query := c.QueryParams()
			for _, key := range []string{"filter_project", "offset", "page", "card_page"} {
				query.Del(key)
			}
			location := c.Request().URL.Path + "?" + query.Encode()
			if !isHTMX(c) {
				return c.Redirect(http.StatusSeeOther, location)
			}
			if c.Request().Header.Get("HX-Target") == "all-projects-results" {
				// A poll cannot retarget itself into the main page: the shell
				// must start a fresh navigation after checking it is still current.
				c.Response().Header().Set("X-OpenVibely-All-Projects-Location", location)
				return c.NoContent(http.StatusOK)
			}
			c.Request().URL.RawQuery = query.Encode()
			data.FilterProject = ""
			// Main-page requests can replace their URL and filter controls directly.
			c.Request().Header.Set("HX-Target", "main-content")
			c.Response().Header().Set("HX-Replace-Url", location)
			c.Response().Header().Set("HX-Retarget", "#main-content")
			c.Response().Header().Set("HX-Reswap", "innerHTML")
		} else {
			projectID = data.FilterProject
		}
	}
	switch data.Section {
	case "tasks":
		tasks, err := h.taskSvc.ListBoardByProjectWithCategorySorts(ctx, projectID, "", "", "")
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if task.Category == models.CategoryChat {
				continue
			}
			if data.Search != "" && !strings.Contains(strings.ToLower(task.Title), strings.ToLower(data.Search)) {
				continue
			}
			data.Tasks = append(data.Tasks, task)
		}
	case "automations":
		if h.automationGraphSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "Automations unavailable")
		}
		page := parseCardPageRequest(c)
		data.Offset = page.Offset
		data.PageSize = page.PageSize
		cards, err := h.automationGraphSvc.ListPageFiltered(ctx, projectID, page.PageSize+1, page.Offset, persistence.AutomationCardListFilter{Search: data.Search, Sort: "name_asc"})
		if err != nil {
			return err
		}
		if len(cards) == 0 && page.Offset > 0 {
			// Find the last occupied page without walking every stale page.
			filter := persistence.AutomationCardListFilter{Search: data.Search, Sort: "name_asc"}
			low, high, last := 0, (page.Offset-1)/page.PageSize, 0
			for low <= high {
				mid := low + (high-low)/2
				probe, err := h.automationGraphSvc.ListPageFiltered(ctx, projectID, 1, mid*page.PageSize, filter)
				if err != nil {
					return err
				}
				if len(probe) > 0 {
					last = mid
					low = mid + 1
				} else {
					high = mid - 1
				}
			}
			data.Offset = last * page.PageSize
			query := c.QueryParams()
			query.Del("page")
			query.Set("card_page", "1")
			query.Set("page_size", strconv.Itoa(page.PageSize))
			query.Set("offset", strconv.Itoa(data.Offset))
			location := "/automations?" + query.Encode()
			if !isHTMX(c) {
				return c.Redirect(http.StatusSeeOther, location)
			}
			if c.Request().Header.Get("HX-Target") == "all-projects-results" {
				c.Response().Header().Set("X-OpenVibely-All-Projects-Location", location)
				return c.NoContent(http.StatusOK)
			}
			c.Response().Header().Set("HX-Replace-Url", location)
			cards, err = h.automationGraphSvc.ListPageFiltered(ctx, projectID, page.PageSize+1, data.Offset, filter)
			if err != nil {
				return err
			}
		}
		data.Automations, data.HasMore = cardPageItems(cards, page.PageSize)
	case "analytics":
		data.AnalyticsFilters = c.QueryParams()
		data.AnalyticsProject = &models.Project{ID: projectID, Name: "All projects"}
		for _, project := range projects {
			if project.ID == projectID {
				data.AnalyticsProject.Name = project.Name
			}
		}
		c.SetRequest(c.Request().WithContext(layout.WithWorkspaceProject(ctx, models.AllProjectsID)))
	}
	if isHTMX(c) && c.Request().Header.Get("HX-Target") == "all-projects-results" {
		if data.Section == "tasks" {
			return render(c, http.StatusOK, pages.AllProjectsTaskResults(data))
		}
		if data.Section == "automations" {
			return render(c, http.StatusOK, pages.AllProjectsAutomationResults(data))
		}
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AllProjectsContent(data))
	}
	return render(c, http.StatusOK, pages.AllProjects(data))
}

func (h *Handler) AllProjectsChooseProject(c echo.Context) error {
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	path := c.Request().URL.Path
	if strings.HasPrefix(path, "/projects/") {
		path = "/tasks"
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AllProjectsChooseContent(projects, path))
	}
	return render(c, http.StatusOK, pages.AllProjectsChoose(projects, path))
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
