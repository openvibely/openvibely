package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// ArchitectMode shows the architect dashboard
func (h *Handler) ArchitectMode(c echo.Context) error {
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
		return render(c, http.StatusOK, pages.ArchitectMode(projects, nil, nil))
	}

	data, err := h.architectSvc.GetDashboard(ctx, currentProject.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load architect dashboard")
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ArchitectModeContent(currentProject, data))
	}
	return render(c, http.StatusOK, pages.ArchitectMode(projects, currentProject, data))
}

// CreateArchitectSession creates a new architect session
func (h *Handler) CreateArchitectSession(c echo.Context) error {
	ctx := c.Request().Context()
	projectID := c.QueryParam("project_id")
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	title := c.FormValue("title")
	description := c.FormValue("description")
	templateID := c.FormValue("template_id")

	var tmplPtr *string
	if templateID != "" {
		tmplPtr = &templateID
	}

	session, err := h.architectSvc.CreateSession(ctx, projectID, title, description, tmplPtr)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	detail, err := h.architectSvc.GetSessionDetail(ctx, session.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectWizard(detail))
}

// GetArchitectSession shows the wizard for an existing session
func (h *Handler) GetArchitectSession(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	detail, err := h.architectSvc.GetSessionDetail(ctx, sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ArchitectWizard(detail))
	}

	projects, _ := h.projectSvc.List(ctx)
	currentProject, _ := h.projectSvc.GetByID(ctx, detail.Session.ProjectID)
	return render(c, http.StatusOK, pages.ArchitectSessionPage(projects, currentProject, detail))
}

// SendArchitectMessage handles user messages in the multi-turn conversation
func (h *Handler) SendArchitectMessage(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")
	message := c.FormValue("message")

	if message == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "message required")
	}

	reply, err := h.architectSvc.SendMessage(ctx, sessionID, message)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectMessagePair(message, reply))
}

// AdvanceArchitectPhase advances to the next phase in the wizard
func (h *Handler) AdvanceArchitectPhase(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	session, err := h.architectSvc.AdvancePhase(ctx, sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	detail, err := h.architectSvc.GetSessionDetail(ctx, session.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectWizard(detail))
}

// GenerateArchitectArchitecture generates architecture recommendations
func (h *Handler) GenerateArchitectArchitecture(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	rec, err := h.architectSvc.GenerateArchitecture(ctx, sessionID)
	if err != nil {
		return render(c, http.StatusOK, pages.ArchitectError(err.Error()))
	}

	return render(c, http.StatusOK, pages.ArchitectArchResult(rec))
}

// GenerateArchitectRisks generates risk analysis
func (h *Handler) GenerateArchitectRisks(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	analysis, err := h.architectSvc.GenerateRiskAnalysis(ctx, sessionID)
	if err != nil {
		return render(c, http.StatusOK, pages.ArchitectError(err.Error()))
	}

	return render(c, http.StatusOK, pages.ArchitectRiskResult(analysis))
}

// GenerateArchitectTasks generates the task plan
func (h *Handler) GenerateArchitectTasks(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	tasks, err := h.architectSvc.GenerateTaskPlan(ctx, sessionID)
	if err != nil {
		return render(c, http.StatusOK, pages.ArchitectError(err.Error()))
	}

	session, _ := h.architectSvc.GetSessionDetail(ctx, sessionID)

	return render(c, http.StatusOK, pages.ArchitectTaskPlan(tasks, session))
}

// ActivateArchitectPhase activates tasks from a specific phase
func (h *Handler) ActivateArchitectPhase(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")
	phaseStr := c.FormValue("phase")
	projectID := c.QueryParam("project_id")

	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}

	phase := models.ArchitectTaskPhase(phaseStr)
	count, err := h.architectSvc.ActivatePhase(ctx, sessionID, phase, projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectActivateResult(count, phaseStr))
}

// DeleteArchitectSession deletes a session
func (h *Handler) DeleteArchitectSession(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")
	projectID := c.QueryParam("project_id")

	if err := h.architectSvc.DeleteSession(ctx, sessionID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	// Return updated dashboard
	if projectID == "" {
		return c.NoContent(http.StatusOK)
	}
	currentProject, _ := h.projectSvc.GetByID(ctx, projectID)
	data, _ := h.architectSvc.GetDashboard(ctx, projectID)
	return render(c, http.StatusOK, pages.ArchitectModeContent(currentProject, data))
}

// AbandonArchitectSession marks a session as abandoned
func (h *Handler) AbandonArchitectSession(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")

	if err := h.architectSvc.AbandonSession(ctx, sessionID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	detail, err := h.architectSvc.GetSessionDetail(ctx, sessionID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectWizard(detail))
}

// SaveArchitectTemplate saves the session as a template
func (h *Handler) SaveArchitectTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	sessionID := c.Param("id")
	name := c.FormValue("name")
	description := c.FormValue("description")
	category := c.FormValue("category")

	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "template name required")
	}
	if category == "" {
		category = "general"
	}

	tmpl, err := h.architectSvc.SaveAsTemplate(ctx, sessionID, name, description, category)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return render(c, http.StatusOK, pages.ArchitectTemplateCard(tmpl))
}

// DeleteArchitectTemplate deletes a template
func (h *Handler) DeleteArchitectTemplate(c echo.Context) error {
	ctx := c.Request().Context()
	templateID := c.Param("id")

	if err := h.architectSvc.DeleteTemplate(ctx, templateID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	templates, _ := h.architectSvc.ListTemplates(ctx)
	return render(c, http.StatusOK, pages.ArchitectTemplateList(templates))
}
