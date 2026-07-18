package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) NewAutomationBuilder(c echo.Context) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationNewContent(projectID))
	}
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.AutomationNew(projects, projectID))
}

func (h *Handler) CreateAutomationDraftWeb(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	ctx := c.Request().Context()
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	source := strings.TrimSpace(c.FormValue("source"))
	templateKey := strings.TrimSpace(c.FormValue("template_key"))
	var candidate models.AutomationDraftCandidate
	switch source {
	case "template":
		candidate, err = h.automationDraftSvc.TemplateCandidate(templateKey)
	case "blank":
		candidate, err = h.automationDraftSvc.BlankCandidate(templateKey)
	case "describe":
		var preview *models.AutomationDraftResult
		preview, err = h.previewAutomationDescription(ctx, projectID, c.FormValue("description"))
		if err == nil {
			candidate = preview.Candidate
		}
	default:
		err = echo.NewHTTPError(http.StatusBadRequest, "source must be template, describe, or blank")
	}
	if err != nil {
		if source == "describe" {
			if isHTMX(c) {
				return render(c, http.StatusUnprocessableEntity, pages.AutomationNewFailureContent(projectID, err.Error()))
			}
			projects, _ := h.projectSvc.List(ctx)
			return render(c, http.StatusUnprocessableEntity, pages.AutomationNewFailure(projects, projectID, err.Error()))
		}
		return err
	}
	draftSource := "manual"
	if source == "template" {
		draftSource = "template"
	}
	result, err := h.automationDraftSvc.CreateDraft(ctx, service.AutomationDraftCreateRequest{ProjectID: projectID, Source: draftSource, CreatedVia: "web", Candidate: candidate})
	if err != nil {
		return err
	}
	h.setAutomationDraftPushURL(c, result, projectID)
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) CloneAutomationDraftWeb(c echo.Context) error {
	if h.automationDraftSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	result, err := h.automationDraftSvc.ClonePublishedVersion(c.Request().Context(), projectID, c.Param("automationId"))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	h.setAutomationDraftPushURL(c, result, projectID)
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) GetAutomationDraftWeb(c echo.Context) error {
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *result})
}

func (h *Handler) UpdateAutomationDraftWeb(c echo.Context) error {
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	candidate := result.Candidate
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		prefix := "node_" + node.Key + "_"
		if value := strings.TrimSpace(c.FormValue(prefix + "name")); value != "" {
			node.Name = value
		}
		if _, ok := node.Config["prompt"]; ok {
			node.Config["prompt"] = c.FormValue(prefix + "prompt")
			node.Config["category"] = c.FormValue(prefix + "category")
			if priority, parseErr := strconv.Atoi(c.FormValue(prefix + "priority")); parseErr == nil {
				node.Config["priority"] = priority
			}
		}
		if _, ok := node.Config["run_at"]; ok {
			node.Config["run_at"] = c.FormValue(prefix + "run_at")
			node.Config["repeat_type"] = c.FormValue(prefix + "repeat_type")
			if interval, parseErr := strconv.Atoi(c.FormValue(prefix + "repeat_interval")); parseErr == nil {
				node.Config["repeat_interval"] = interval
			}
			node.Config["enabled"] = c.FormValue(prefix+"enabled") == "true"
		}
	}
	for i := range candidate.Edges {
		candidate.Edges[i].Label = strings.TrimSpace(c.FormValue("edge_" + candidate.Edges[i].Key + "_label"))
	}
	projectID, _ := h.getCurrentProjectID(c)
	updated, err := h.automationDraftSvc.UpdateDraft(c.Request().Context(), c.Param("automationId"), c.Param("versionId"), projectID, candidate)
	if err != nil {
		return err
	}
	return h.renderAutomationBuilder(c, models.AutomationBuilderPage{Result: *updated})
}

func (h *Handler) PlanAutomationDraftWeb(c echo.Context) error {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation publication planning unavailable")
	}
	result, err := h.loadAutomationDraftWeb(c)
	if err != nil {
		return err
	}
	projectID, _ := h.getCurrentProjectID(c)
	plan, err := h.automationPlanner.Plan(c.Request().Context(), projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return err
	}
	page := models.AutomationBuilderPage{Result: *result, Plan: plan}
	if len(plan.Validation) == 0 {
		page.ConfirmationToken, err = h.automationConfirmationSvc.Issue(c.Request().Context(), service.AutomationConfirmationIssue{ProjectID: projectID, AutomationID: c.Param("automationId"), VersionID: c.Param("versionId"), PlanRevision: plan.PlanRevision, PrincipalID: h.authPrincipalID(c), ThreadID: "web:" + c.Param("automationId"), PlanMessageID: "web:" + repository.NewID()})
		if err != nil {
			return err
		}
	}
	return h.renderAutomationBuilder(c, page)
}

func (h *Handler) PublishAutomationDraftWeb(c echo.Context) error {
	if h.automationPlanner == nil || h.automationConfirmationSvc == nil || h.automationCompiler == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation publication unavailable")
	}
	ctx := c.Request().Context()
	projectID, _ := h.getCurrentProjectID(c)
	if _, err := h.loadAutomationDraftWeb(c); err != nil {
		return err
	}
	plan, err := h.automationPlanner.Plan(ctx, projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return err
	}
	if plan.PlanRevision != c.FormValue("plan_revision") {
		return echo.NewHTTPError(http.StatusConflict, "publication plan changed; preview again")
	}
	if _, err := h.automationConfirmationSvc.ConfirmWeb(ctx, c.FormValue("confirmation_token"), projectID, c.Param("automationId"), c.Param("versionId"), plan.PlanRevision, h.authPrincipalID(c), plan.Effects); err != nil {
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	}
	published, publishErr := h.automationCompiler.Publish(ctx, service.AutomationPublishRequest{ProjectID: projectID, AutomationID: c.Param("automationId"), VersionID: c.Param("versionId"), PlanRevision: plan.PlanRevision})
	if publishErr != nil {
		result, _ := h.loadAutomationDraftWeb(c)
		if result != nil {
			page := models.AutomationBuilderPage{Result: *result, Plan: plan, Error: publishErr.Error()}
			if published != nil {
				page.PublicationSteps = published.Resources
			}
			return h.renderAutomationBuilder(c, page)
		}
		return publishErr
	}
	url := "/automations/" + c.Param("automationId") + "?project_id=" + projectID
	if isHTMX(c) {
		c.Response().Header().Set("HX-Redirect", url)
		return c.NoContent(http.StatusNoContent)
	}
	return c.Redirect(http.StatusSeeOther, url)
}

func (h *Handler) PauseAutomation(c echo.Context) error {
	return h.changeAutomationLifecycle(c, "pause")
}
func (h *Handler) ResumeAutomation(c echo.Context) error {
	return h.changeAutomationLifecycle(c, "resume")
}
func (h *Handler) ArchiveAutomation(c echo.Context) error {
	return h.changeAutomationLifecycle(c, "archive")
}

func (h *Handler) changeAutomationLifecycle(c echo.Context, action string) error {
	if h.automationLifecycleSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "automation lifecycle unavailable")
	}
	projectID, _ := h.getCurrentProjectID(c)
	var err error
	switch action {
	case "pause":
		err = h.automationLifecycleSvc.Pause(c.Request().Context(), projectID, c.Param("automationId"))
	case "resume":
		err = h.automationLifecycleSvc.Resume(c.Request().Context(), projectID, c.Param("automationId"))
	case "archive":
		err = h.automationLifecycleSvc.Archive(c.Request().Context(), projectID, c.Param("automationId"))
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return echo.NewHTTPError(http.StatusNotFound, "automation not found")
		}
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusSeeOther, "/automations/"+c.Param("automationId")+"?project_id="+projectID)
}

func (h *Handler) loadAutomationDraftWeb(c echo.Context) (*models.AutomationDraftResult, error) {
	if h.automationDraftSvc == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "automation builder unavailable")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return nil, err
	}
	result, err := h.automationDraftSvc.GetDraft(c.Request().Context(), projectID, c.Param("automationId"), c.Param("versionId"))
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, echo.NewHTTPError(http.StatusNotFound, "automation draft not found")
	}
	return result, nil
}

func (h *Handler) setAutomationDraftPushURL(c echo.Context, result *models.AutomationDraftResult, projectID string) {
	if !isHTMX(c) || result == nil || result.Definition == nil {
		return
	}
	automationID := strings.TrimSpace(result.Definition.Automation.ID)
	versionID := strings.TrimSpace(result.Definition.Version.ID)
	if automationID == "" || versionID == "" {
		return
	}
	c.Response().Header().Set("HX-Push-Url", "/automations/"+automationID+"/drafts/"+versionID+"?project_id="+projectID)
}

func (h *Handler) renderAutomationBuilder(c echo.Context, page models.AutomationBuilderPage) error {
	projectID, _ := h.getCurrentProjectID(c)
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AutomationBuilderContent(page, projectID))
	}
	projects, _ := h.projectSvc.List(c.Request().Context())
	return render(c, http.StatusOK, pages.AutomationBuilder(projects, projectID, page))
}
