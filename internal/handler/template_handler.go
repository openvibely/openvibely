package handler

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// Templates shows the template library page
func (h *Handler) Templates(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")

	projects, err := h.projectSvc.List(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list projects")
	}

	var currentProject *models.Project
	if projectID != "" {
		currentProject, err = h.projectSvc.GetByID(ctx, projectID)
		if err != nil || currentProject == nil {
			if len(projects) > 0 {
				currentProject = &projects[0]
			}
		}
	} else if len(projects) > 0 {
		currentProject = &projects[0]
	}

	if currentProject == nil {
		return render(c, http.StatusOK, pages.Templates(projects, nil, nil))
	}

	data, err := h.templateSvc.GetDashboard(ctx, currentProject.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load templates")
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.TemplatesContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.Templates(projects, currentProject, data))
}

// CreateTemplate creates a new custom template
func (h *Handler) CreateTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	name := c.FormValue("name")
	description := c.FormValue("description")
	defaultPrompt := c.FormValue("default_prompt")
	category := models.TaskCategory(c.FormValue("category"))
	categoryFilter := c.FormValue("category_filter")
	priorityStr := c.FormValue("priority")
	tag := models.TaskTag(c.FormValue("tag"))
	agentID := c.FormValue("agent_id")

	priority, err := strconv.Atoi(priorityStr)
	if err != nil {
		priority = 2 // default
	}

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           name,
		Description:    description,
		DefaultPrompt:  defaultPrompt,
		Category:       category,
		Priority:       priority,
		Tag:            tag,
		TagsJSON:       "[]",
		CategoryFilter: categoryFilter,
		CreatedBy:      "user",
	}

	if agentID != "" {
		tmpl.SuggestedAgentID = &agentID
	}

	if err := h.templateSvc.CreateCustomTemplate(ctx, tmpl); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Return refreshed template list
	data, err := h.templateSvc.GetDashboard(ctx, projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to refresh templates")
	}

	currentProject, _ := h.projectSvc.GetByID(ctx, projectID)
	return render(c, http.StatusOK, pages.TemplatesContent(currentProject, data))
}

// CreateTaskFromTemplate creates a task from a template
func (h *Handler) CreateTaskFromTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	templateID := c.Param("id")
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	// Optional: user can override the prompt
	userPrompt := c.FormValue("prompt")

	task, err := h.templateSvc.CreateFromTemplate(ctx, templateID, projectID, userPrompt)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Redirect to tasks page or return success message
	c.Response().Header().Set("HX-Redirect", "/tasks?project_id="+projectID)
	return c.String(http.StatusOK, "Task created: "+task.Title)
}

// SaveTaskAsTemplate saves a task as a template
func (h *Handler) SaveTaskAsTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	taskID := c.Param("id")
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	name := c.FormValue("name")
	description := c.FormValue("description")
	categoryFilter := c.FormValue("category_filter")

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "name required")
	}

	_, err := h.templateSvc.SaveAsTemplate(ctx, taskID, projectID, name, description, categoryFilter, "user")
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.String(http.StatusOK, "Template created: "+name)
}

// UpdateTemplate updates a custom template
func (h *Handler) UpdateTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	templateID := c.Param("id")
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	name := c.FormValue("name")
	description := c.FormValue("description")
	defaultPrompt := c.FormValue("default_prompt")
	category := models.TaskCategory(c.FormValue("category"))
	categoryFilter := c.FormValue("category_filter")
	priorityStr := c.FormValue("priority")
	tag := models.TaskTag(c.FormValue("tag"))
	agentID := c.FormValue("agent_id")

	priority, err := strconv.Atoi(priorityStr)
	if err != nil {
		priority = 2
	}

	tmpl := &models.TaskTemplate{
		ID:             templateID,
		Name:           name,
		Description:    description,
		DefaultPrompt:  defaultPrompt,
		Category:       category,
		Priority:       priority,
		Tag:            tag,
		CategoryFilter: categoryFilter,
	}

	if agentID != "" {
		tmpl.SuggestedAgentID = &agentID
	}

	if err := h.templateSvc.UpdateTemplate(ctx, tmpl); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Return refreshed template list
	data, err := h.templateSvc.GetDashboard(ctx, projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to refresh templates")
	}

	currentProject, _ := h.projectSvc.GetByID(ctx, projectID)
	return render(c, http.StatusOK, pages.TemplatesContent(currentProject, data))
}

// DeleteTemplate deletes a custom template
func (h *Handler) DeleteTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	templateID := c.Param("id")
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	if err := h.templateSvc.DeleteTemplate(ctx, templateID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Return refreshed template list
	data, err := h.templateSvc.GetDashboard(ctx, projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to refresh templates")
	}

	currentProject, _ := h.projectSvc.GetByID(ctx, projectID)
	return render(c, http.StatusOK, pages.TemplatesContent(currentProject, data))
}

// ToggleFavoriteTemplate toggles favorite status
func (h *Handler) ToggleFavoriteTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	templateID := c.Param("id")
	projectID := c.QueryParam("project_id")
	isFavoriteStr := c.FormValue("is_favorite")

	isFavorite := isFavoriteStr == "true" || isFavoriteStr == "1"

	if err := h.templateSvc.ToggleFavorite(ctx, templateID, isFavorite); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Return refreshed template list
	data, err := h.templateSvc.GetDashboard(ctx, projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to refresh templates")
	}

	currentProject, _ := h.projectSvc.GetByID(ctx, projectID)
	return render(c, http.StatusOK, pages.TemplatesContent(currentProject, data))
}

// SearchTemplates searches templates
func (h *Handler) SearchTemplates(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	query := c.QueryParam("q")

	results, err := h.templateSvc.SearchTemplates(ctx, projectID, query)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.TemplateSearchResults(results))
}

// FilterTemplates filters templates by category
func (h *Handler) FilterTemplates(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	categoryStr := c.QueryParam("category")

	category := models.TaskTemplateCategory(categoryStr)
	results, err := h.templateSvc.FilterByCategory(ctx, projectID, category)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.TemplateFilterResults(results))
}
