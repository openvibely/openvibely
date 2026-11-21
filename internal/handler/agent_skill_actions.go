package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type agentSkillOperationArgs struct {
	AgentID, ProjectID, Operation string
	Handle, Scope, Reason         string                `remote:"raw"`
	Save                          agentSkillSaveRequest `remote:"raw"`
}

var actAgentSkillOperation = defineAction("agent.skills", func(h *Handler, ctx context.Context, a agentSkillOperationArgs) (string, authz.Permission, error) {
	if err := h.requireActionAdmin(ctx); err != nil {
		return "", authz.Manage, err
	}
	agent, err := h.agentRepo.GetByID(ctx, a.AgentID)
	if err != nil {
		return "", authz.Manage, err
	}
	if agent == nil {
		return "", authz.Manage, statusErr(http.StatusNotFound, "agent not found")
	}
	if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) {
		if err := h.requireActionProject(ctx, a.ProjectID, authz.View); err != nil {
			return "", authz.View, err
		}
	}
	if agent.Scope == models.AgentScopeProject && agent.ProjectID != "" {
		if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) && a.ProjectID != agent.ProjectID {
			return "", authz.Manage, statusErr(http.StatusNotFound, "agent not found")
		}
		return agent.ProjectID, authz.Manage, nil
	}
	return instanceActionProject, authz.Manage, nil
}, func(h *Handler, ctx context.Context, a agentSkillOperationArgs) (agentSkillsResponse, error) {
	agent, err := h.agentRepo.GetByID(ctx, a.AgentID)
	if err != nil {
		return agentSkillsResponse{}, err
	}
	if agent == nil {
		return agentSkillsResponse{}, statusErr(http.StatusNotFound, "agent not found")
	}
	project := a.ProjectID
	if agent.Scope == models.AgentScopeProject && agent.ProjectID != "" {
		project = agent.ProjectID
	}
	root := ""
	if project != "" && !models.IsAggregateProject(project) {
		root = service.ProjectSkillRootForResolver(ctx, h.projectRepo, project)
	}
	if a.Operation == "list" {
		if err := h.migrateAgentSkillsOnOwner(ctx, agent, root); err != nil {
			return agentSkillsResponse{}, err
		}
		return h.agentSkillCatalog(agent, root)
	}
	if err := h.ensureAgentSkillManageAllowed(agent); err != nil {
		return agentSkillsResponse{}, err
	}
	scope := a.Scope
	if scope == "" {
		if agent.Scope == models.AgentScopeGlobal {
			scope = "global"
		} else {
			scope = "project"
		}
	}
	if !validDialogSkillKey(a.Handle) {
		return agentSkillsResponse{}, statusErr(http.StatusBadRequest, "invalid skill handle")
	}
	event := models.SkillEventEdited
	switch a.Operation {
	case "create", "update":
		if a.Save.Handle == "" {
			a.Save.Handle = a.Handle
		}
		if a.Save.Handle != a.Handle {
			return agentSkillsResponse{}, statusErr(http.StatusBadRequest, "skill handle mismatch")
		}
		result, err := h.writeAgentOwnedSkill(ctx, agent, a.Save, a.Operation == "update", scope, root)
		if err != nil {
			return agentSkillsResponse{}, err
		}
		event = skillWriteEventType(result)
	case "archive":
		if err := h.archiveAgentOwnedSkill(agent, a.Handle, a.Reason, scope, root); err != nil {
			return agentSkillsResponse{}, err
		}
	default:
		return agentSkillsResponse{}, statusErr(http.StatusBadRequest, "unknown skill operation")
	}
	if h.skillAnalyticsRepo != nil {
		_ = h.skillAnalyticsRepo.RecordEvent(ctx, &models.SkillAnalyticsEvent{ProjectID: project, AgentID: agent.ID, SkillScope: models.SkillScopeAgentOwned, SkillHandle: a.Handle, EventType: event, Source: models.SkillEventSourceManual, Surface: models.SkillSurfaceTaskThread})
	}
	return h.agentSkillCatalog(agent, root)
})

func (h *Handler) remoteAgentSkill(c echo.Context, operation string) (bool, error) {
	if _, remote := remoteid.ServerOf(c.Param("id")); !remote {
		return false, nil
	}
	a := agentSkillOperationArgs{AgentID: c.Param("id"), ProjectID: c.QueryParam("project_id"), Operation: operation, Handle: strings.TrimSpace(c.Param("skill")), Scope: c.QueryParam("scope")}
	switch operation {
	case "create", "update":
		if err := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 2<<20)).Decode(&a.Save); err != nil {
			return true, echo.NewHTTPError(http.StatusBadRequest, "invalid JSON")
		}
		if operation == "create" {
			a.Handle = strings.TrimSpace(a.Save.Handle)
		}
		a.Scope = a.Save.Scope
	case "archive":
		var req agentSkillArchiveRequest
		_ = json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 2<<20)).Decode(&req)
		a.Reason = req.Reason
	}
	out, err := actAgentSkillOperation(h, c.Request().Context(), a)
	if err != nil {
		return true, err
	}
	return true, c.JSON(http.StatusOK, out)
}
