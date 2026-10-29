package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// allProjectsScope handles the virtual UI scope before project-specific handlers
// can resolve a default project. The scope has no database row or write authority.
func (h *Handler) allProjectsScope(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Method == http.MethodGet && c.Request().URL.Path == "/chat" && c.QueryParam("project_id") == "" && h.settingsRepo != nil {
			selected, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
			if err == nil && selected == models.AllProjectsID {
				return c.Redirect(http.StatusSeeOther, "/tasks?project_id="+models.AllProjectsID)
			}
		}
		if c.QueryParam("project_id") != models.AllProjectsID {
			return next(c)
		}
		path := c.Request().URL.Path
		// These controls create or configure the shell itself, not a scoped item.
		if path == "/projects/new" || path == "/projects/pick-folder" || path == "/projects" || strings.HasPrefix(path, "/ui/") {
			return next(c)
		}
		if c.Request().Method != http.MethodGet {
			return echo.NewHTTPError(http.StatusBadRequest, "Choose a project before making changes.")
		}
		switch path {
		case "/tasks", "/automations", "/analytics":
			return h.AllProjects(c)
		case "/alerts/unread-count", "/tasks/sidebar":
			return c.NoContent(http.StatusOK)
		case "/events":
			// Aggregate pages refresh their own content without opening a stream
			// against a nonexistent project.
			return c.NoContent(http.StatusNoContent)
		}
		// A direct link to a real item may inherit the shell's scope. Resolve the
		// item's owner, rather than interpreting the virtual ID as a project.
		if strings.HasPrefix(path, "/tasks/") && c.Param("taskId") != "" {
			task, err := h.taskSvc.GetByID(c.Request().Context(), c.Param("taskId"))
			if err != nil {
				return err
			}
			if task != nil {
				q := c.QueryParams()
				q.Set("project_id", task.ProjectID)
				return c.Redirect(http.StatusSeeOther, path+"?"+q.Encode())
			}
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
			return echo.NewHTTPError(http.StatusBadRequest, "Unknown project filter")
		}
		projectID = data.FilterProject
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
		data.Automations, data.HasMore = cardPageItems(cards, page.PageSize)
	case "analytics":
		if h.usageAnalyticsSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "Analytics unavailable")
		}
		filter := parseUsageFilter(c)
		filter.ProjectID = data.FilterProject // Empty is the existing global aggregate query.
		filter.EvidencePeriod, filter.EvidenceProvider, filter.EvidenceModel = "", "", ""
		filter.EvidenceLimit = 0
		data.Usage, err = h.usageAnalyticsSvc.BuildLocalAnalyticsUsage(ctx, filter)
		if err != nil {
			return err
		}
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
