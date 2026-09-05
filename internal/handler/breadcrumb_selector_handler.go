package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

const breadcrumbSelectorLimit = 20

// GetBreadcrumbSelectorResults renders one bounded, project-scoped resource search.
func (h *Handler) GetBreadcrumbSelectorResults(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" || h.projectSvc == nil {
		return echo.NewHTTPError(http.StatusNotFound, "selector unavailable")
	}
	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusNotFound, "selector unavailable")
	}

	currentID := strings.TrimSpace(c.QueryParam("current_id"))
	search := strings.TrimSpace(c.QueryParam("search"))
	scheduleOnly := c.QueryParam("from") == "schedule"
	var kind string
	var items []models.BreadcrumbSelectorItem
	switch c.Param("resource") {
	case "tasks":
		kind = "Task"
		if h.taskSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "selector unavailable")
		}
		items, err = h.taskSvc.ListBreadcrumbSelector(c.Request().Context(), projectID, search, currentID, scheduleOnly, breadcrumbSelectorLimit+1)
	case "automations":
		kind = "Automation"
		if h.automationGraphSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "selector unavailable")
		}
		items, err = h.automationGraphSvc.ListBreadcrumbSelector(c.Request().Context(), projectID, search, currentID, breadcrumbSelectorLimit+1)
	default:
		return echo.NewHTTPError(http.StatusNotFound, "selector unavailable")
	}
	if err != nil {
		return err
	}
	hasMore := len(items) > breadcrumbSelectorLimit
	if hasMore {
		items = items[:breadcrumbSelectorLimit]
	}
	for i := range items {
		items[i].URL = breadcrumbSelectorItemURL(c.Param("resource"), items[i].ID, projectID, c.QueryParam("tab"), c.QueryParam("view"), c.QueryParam("from"))
	}
	return render(c, http.StatusOK, components.BreadcrumbSelectorResults(kind, currentID, items, hasMore))
}

func breadcrumbSelectorItemURL(resource, id, projectID, tab, view, from string) string {
	values := url.Values{"project_id": {projectID}}
	path := "/automations/" + url.PathEscape(id)
	if resource == "tasks" {
		path = "/tasks/" + url.PathEscape(id)
		switch tab {
		case "details", "chat", "changes", "schedules", "chaining", "attachments", "lifecycle":
			values.Set("tab", tab)
		}
		if from == "schedule" {
			values.Set("from", "schedule")
		}
	} else if view == "edit" {
		path += "/builder"
	}
	return path + "?" + values.Encode()
}
