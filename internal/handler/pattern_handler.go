package handler

import (
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// PatternsPage renders the pattern library dashboard
func (h *Handler) PatternsPage(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	projects, _ := h.projectSvc.List(c.Request().Context())
	if projectID == "" && len(projects) > 0 {
		projectID = projects[0].ID
	}

	var currentProject *models.Project
	var data *models.PatternDashboardData

	if projectID != "" && h.patternSvc != nil {
		for i := range projects {
			if projects[i].ID == projectID {
				currentProject = &projects[i]
				break
			}
		}
		var err error
		data, err = h.patternSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			data = &models.PatternDashboardData{}
		}
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.PatternsContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.PatternsPage(projects, currentProject, data))
}

// CreatePattern creates a new pattern
func (h *Handler) CreatePattern(c echo.Context) error {
	projectID := c.FormValue("project_id")
	if projectID == "" || h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	pattern := &models.PromptPattern{
		ProjectID:    projectID,
		Title:        c.FormValue("title"),
		Description:  c.FormValue("description"),
		TemplateText: c.FormValue("template_text"),
		Category:     models.PatternCategory(c.FormValue("category")),
		IsBuiltin:    false,
	}

	// Parse tags
	tagsStr := c.FormValue("tags")
	if tagsStr != "" {
		tags := strings.Split(tagsStr, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		if err := pattern.SetTags(tags); err != nil {
			return c.String(http.StatusBadRequest, "invalid tags")
		}
	} else {
		pattern.Tags = "[]"
	}

	if err := h.patternSvc.CreatePattern(c.Request().Context(), pattern); err != nil {
		return render(c, http.StatusOK, pages.PatternFormResult(nil, err))
	}

	return render(c, http.StatusOK, pages.PatternFormResult(pattern, nil))
}

// GetPattern returns a single pattern (for editing)
func (h *Handler) GetPattern(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	pattern, err := h.patternSvc.GetPattern(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}
	if pattern == nil {
		return c.String(http.StatusNotFound, "pattern not found")
	}

	return render(c, http.StatusOK, pages.PatternEditForm(pattern))
}

// UpdatePattern updates an existing pattern
func (h *Handler) UpdatePattern(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	pattern := &models.PromptPattern{
		ID:           id,
		Title:        c.FormValue("title"),
		Description:  c.FormValue("description"),
		TemplateText: c.FormValue("template_text"),
		Category:     models.PatternCategory(c.FormValue("category")),
	}

	// Parse tags
	tagsStr := c.FormValue("tags")
	if tagsStr != "" {
		tags := strings.Split(tagsStr, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		if err := pattern.SetTags(tags); err != nil {
			return c.String(http.StatusBadRequest, "invalid tags")
		}
	} else {
		pattern.Tags = "[]"
	}

	if err := h.patternSvc.UpdatePattern(c.Request().Context(), pattern); err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	projectID := c.FormValue("project_id")
	data, _ := h.patternSvc.GetDashboard(c.Request().Context(), projectID)

	var proj *models.Project
	projects, _ := h.projectSvc.List(c.Request().Context())
	for i := range projects {
		if projects[i].ID == projectID {
			proj = &projects[i]
			break
		}
	}

	return render(c, http.StatusOK, pages.PatternsContent(proj, data))
}

// DeletePattern removes a pattern
func (h *Handler) DeletePattern(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	if err := h.patternSvc.DeletePattern(c.Request().Context(), id); err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return c.String(http.StatusOK, "")
	}

	data, _ := h.patternSvc.GetDashboard(c.Request().Context(), projectID)

	var proj *models.Project
	projects, _ := h.projectSvc.List(c.Request().Context())
	for i := range projects {
		if projects[i].ID == projectID {
			proj = &projects[i]
			break
		}
	}

	return render(c, http.StatusOK, pages.PatternsContent(proj, data))
}

// ApplyPatternForm returns the form for applying a pattern
func (h *Handler) ApplyPatternForm(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	pattern, err := h.patternSvc.GetPattern(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}
	if pattern == nil {
		return c.String(http.StatusNotFound, "pattern not found")
	}

	// Get agents for dropdown
	agents, _ := h.llmConfigRepo.List(c.Request().Context())

	return render(c, http.StatusOK, pages.PatternApplyForm(pattern, agents))
}

// ApplyPattern applies a pattern and creates a task
func (h *Handler) ApplyPattern(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	pattern, err := h.patternSvc.GetPattern(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}
	if pattern == nil {
		return c.String(http.StatusNotFound, "pattern not found")
	}

	// Parse variable values
	variables, err := pattern.ParseVariables()
	if err != nil {
		return c.String(http.StatusBadRequest, "invalid pattern variables")
	}

	variableValues := make(map[string]string)
	for _, v := range variables {
		val := c.FormValue("var_" + v)
		variableValues[v] = val
	}

	// Parse agent ID
	var agentID *string
	agentIDStr := c.FormValue("agent_id")
	if agentIDStr != "" {
		agentID = &agentIDStr
	}

	// Parse category
	category := models.TaskCategory(c.FormValue("category"))
	if category == "" {
		category = models.CategoryBacklog
	}

	taskTitle := c.FormValue("task_title")
	if taskTitle == "" {
		taskTitle = pattern.Title
	}

	task, err := h.patternSvc.ApplyPattern(
		c.Request().Context(),
		id,
		variableValues,
		taskTitle,
		pattern.ProjectID,
		agentID,
		category,
	)
	if err != nil {
		return render(c, http.StatusOK, pages.PatternApplyResult(nil, err))
	}

	return render(c, http.StatusOK, pages.PatternApplyResult(task, nil))
}

// DuplicatePattern creates a copy of a pattern
func (h *Handler) DuplicatePattern(c echo.Context) error {
	id := c.Param("id")
	if h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "pattern service not available")
	}

	_, err := h.patternSvc.DuplicatePattern(c.Request().Context(), id)
	if err != nil {
		return c.String(http.StatusBadRequest, err.Error())
	}

	projectID := c.QueryParam("project_id")
	data, _ := h.patternSvc.GetDashboard(c.Request().Context(), projectID)

	var proj *models.Project
	projects, _ := h.projectSvc.List(c.Request().Context())
	for i := range projects {
		if projects[i].ID == projectID {
			proj = &projects[i]
			break
		}
	}

	return render(c, http.StatusOK, pages.PatternsContent(proj, data))
}

// SearchPatterns searches for patterns
func (h *Handler) SearchPatterns(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	query := c.QueryParam("q")

	if projectID == "" || h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	var patterns []models.PromptPattern
	var err error

	categoryStr := c.QueryParam("category")
	if query == "" && categoryStr != "" {
		patterns, err = h.patternSvc.ListByCategory(c.Request().Context(), projectID, models.PatternCategory(categoryStr))
	} else if query != "" {
		patterns, err = h.patternSvc.Search(c.Request().Context(), projectID, query)
	} else {
		// No filter - get all patterns from dashboard
		data, err := h.patternSvc.GetDashboard(c.Request().Context(), projectID)
		if err != nil {
			return c.String(http.StatusInternalServerError, err.Error())
		}
		return render(c, http.StatusOK, pages.PatternList(data.AllPatterns, projectID))
	}

	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.PatternList(patterns, projectID))
}

// ExportPatterns exports custom patterns as JSON
func (h *Handler) ExportPatterns(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	if projectID == "" || h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	data, err := h.patternSvc.ExportPatterns(c.Request().Context(), projectID)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	c.Response().Header().Set("Content-Disposition", "attachment; filename=patterns.json")
	return c.JSONBlob(http.StatusOK, data)
}

// ImportPatterns imports patterns from JSON file
func (h *Handler) ImportPatterns(c echo.Context) error {
	projectID := c.FormValue("project_id")
	if projectID == "" || h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	// Read uploaded file
	file, err := c.FormFile("file")
	if err != nil {
		return c.String(http.StatusBadRequest, "no file uploaded")
	}

	src, err := file.Open()
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to open file")
	}
	defer src.Close()

	data, err := io.ReadAll(src)
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to read file")
	}

	imported, errors, err := h.patternSvc.ImportPatterns(c.Request().Context(), projectID, data)
	if err != nil {
		return render(c, http.StatusOK, pages.PatternImportResult(0, nil, err))
	}

	return render(c, http.StatusOK, pages.PatternImportResult(imported, errors, nil))
}

// ListPatternsByCategory lists patterns in a category
func (h *Handler) ListPatternsByCategory(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	category := models.PatternCategory(c.Param("category"))

	if projectID == "" || h.patternSvc == nil {
		return c.String(http.StatusBadRequest, "missing project_id")
	}

	patterns, err := h.patternSvc.ListByCategory(c.Request().Context(), projectID, category)
	if err != nil {
		return c.String(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.PatternCategoryList(category, patterns))
}
