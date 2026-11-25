package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type agentSaveArgs struct {
	ProjectID, ID, AgentProjectID, Model string
	Values                               url.Values `remote:"raw"`
	Hooks                                []hookSavePayload
}

type agentSaveResult struct{ ID string }

var actAgentSave = defineAction("agent.save", func(h *Handler, ctx context.Context, a agentSaveArgs) (string, authz.Permission, error) {
	if a.ID != "" {
		agent, err := h.agentRepo.GetByID(ctx, a.ID)
		if err != nil {
			return "", authz.Manage, err
		}
		if agent == nil {
			return "", authz.Manage, statusErr(http.StatusNotFound, "Agent not found")
		}
		if agent.Scope == models.AgentScopeProject && agent.ProjectID != "" {
			if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) && a.ProjectID != agent.ProjectID {
				return "", authz.Manage, statusErr(http.StatusNotFound, "Agent not found")
			}
			if err := h.requireActionProject(ctx, agent.ProjectID, authz.Manage); err != nil {
				return "", authz.Manage, err
			}
		}
	}
	if a.AgentProjectID != "" {
		if err := h.requireActionProject(ctx, a.AgentProjectID, authz.Manage); err != nil {
			return "", authz.Manage, err
		}
	}
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a agentSaveArgs) (agentSaveResult, error) {
	if a.Values == nil {
		return agentSaveResult{}, statusErr(http.StatusBadRequest, "agent fields are required")
	}
	a.Values.Set("project_id", a.ProjectID)
	a.Values.Set("agent_project_id", a.AgentProjectID)
	a.Values.Set("model", a.Model)
	form := ownerFormValues(a.Values)
	agent := &models.Agent{}
	if a.ID != "" {
		var err error
		agent, err = h.agentRepo.GetByID(ctx, a.ID)
		if err != nil {
			return agentSaveResult{}, err
		}
		if agent == nil {
			return agentSaveResult{}, statusErr(http.StatusNotFound, "Agent not found")
		}
	}
	protected := agent.GeneratedStatus == models.AgentStatusProtected
	if protected {
		candidate := *agent
		options, err := h.llmConfigRepo.ListPickerOptions(ctx)
		if err != nil {
			return agentSaveResult{}, err
		}
		if err := applyProtectedAgentControlFormFields(form, &candidate, buildAllowedAgentModels(options)); err != nil {
			return agentSaveResult{}, err
		}
		if service.IsRequiredSystemAgent(agent) && !candidate.Enabled {
			return agentSaveResult{}, statusErr(http.StatusForbidden, "Goal Agent is required and cannot be disabled")
		}
		if !service.IsUserDisableableSystemAgent(agent) {
			candidate.Enabled = agent.Enabled
		}
		if protectedSystemAgentLockedFieldsChanged(agent, &candidate) {
			return agentSaveResult{}, statusErr(http.StatusForbidden, "protected system agents only allow model and eligible enabled-state changes")
		}
		if a.Hooks != nil {
			if err := h.reconcileAgentLifecycleHooks(ctx, agent.ID, a.Hooks); err != nil {
				return agentSaveResult{}, err
			}
		}
		agent.Model, agent.Enabled = candidate.Model, candidate.Enabled
	} else {
		previousProject := agent.ProjectID
		if err := h.applyAgentOwnerForm(ctx, form, a.ProjectID, agent, agentDialogFormOptions{operation: "SaveAgent", defaultMissingCollections: a.ID == ""}); err != nil {
			return agentSaveResult{}, err
		}
		if a.ID != "" && previousProject != agent.ProjectID {
			return agentSaveResult{}, statusErr(http.StatusBadRequest, "Cannot change this Agent's project")
		}
	}
	var err error
	if a.ID == "" {
		err = h.agentRepo.Create(ctx, agent)
	} else {
		err = h.agentRepo.Update(ctx, agent)
	}
	if err != nil {
		if httpErr := agentNameValidationHTTPError(err); httpErr != nil {
			return agentSaveResult{}, httpErr
		}
		return agentSaveResult{}, err
	}
	if protected {
		return agentSaveResult{ID: agent.ID}, nil
	}
	project := a.ProjectID
	if agent.Scope == models.AgentScopeProject && agent.ProjectID != "" {
		project = agent.ProjectID
	}
	root := ""
	if project != "" && !models.IsAggregateProject(project) {
		root = service.ProjectSkillRootForResolver(ctx, h.projectRepo, project)
	}
	finish := func() error {
		if a.Hooks != nil {
			if err := h.reconcileAgentLifecycleHooks(ctx, agent.ID, a.Hooks); err != nil {
				return err
			}
		}
		if err := h.materializeAgentOnOwner(ctx, agent, root, h.usedAgentKeys(nil, root), true); err != nil {
			return err
		}
		return h.migrateAgentSkillsOnOwner(ctx, agent, root)
	}
	if err := finish(); err != nil {
		if a.ID == "" {
			return agentSaveResult{}, errors.Join(err, h.agentRepo.Delete(ctx, agent.ID), cleanupCreatedAgentMaterialization(agent, root, h.agentSkillRoot))
		}
		return agentSaveResult{}, err
	}
	return agentSaveResult{ID: agent.ID}, nil
})

func (h *Handler) saveRemoteAgent(c echo.Context, id string) (bool, error) {
	project, err := h.settingsAgentProject(c, id)
	if err != nil {
		return true, err
	}
	owner := strings.TrimSpace(c.FormValue("agent_project_id"))
	if project == "" {
		owner = ""
	}
	_, remoteID := remoteid.ServerOf(id)
	_, remoteProject := remoteid.ServerOf(project)
	if !remoteID && !remoteProject {
		return false, nil
	}
	values, err := c.FormParams()
	if err != nil {
		return true, err
	}
	a := agentSaveArgs{ProjectID: project, ID: id, AgentProjectID: owner, Model: c.FormValue("model"), Values: values}
	if raw := strings.TrimSpace(c.FormValue("lifecycle_hooks_json")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &a.Hooks); err != nil {
			return true, echo.NewHTTPError(http.StatusBadRequest, "invalid lifecycle hooks")
		}
	}
	if _, err := actAgentSave(h, c.Request().Context(), a); err != nil {
		return true, err
	}
	return true, h.ListAgents(c)
}

type agentMaintainArgs struct {
	AgentID, ProjectID string
	Overwrite          bool
}

var actAgentMaintain = defineAction("agent.maintain", func(h *Handler, ctx context.Context, a agentMaintainArgs) (string, authz.Permission, error) {
	if a.AgentID != "" {
		agent, err := h.agentRepo.GetByID(ctx, a.AgentID)
		if err != nil {
			return "", authz.Manage, err
		}
		if agent == nil {
			return "", authz.Manage, statusErr(http.StatusNotFound, "agent not found")
		}
		if agent.ProjectID != "" {
			if err := h.requireActionProject(ctx, agent.ProjectID, authz.View); err != nil {
				return "", authz.View, err
			}
		}
	}
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a agentMaintainArgs) (struct{}, error) {
	root := ""
	if a.AgentID != "" {
		agent, err := h.agentRepo.GetByID(ctx, a.AgentID)
		if err != nil {
			return struct{}{}, err
		}
		if agent == nil {
			return struct{}{}, statusErr(http.StatusNotFound, "agent not found")
		}
		if agent.ProjectID != "" {
			root = service.ProjectSkillRootForResolver(ctx, h.projectRepo, agent.ProjectID)
		}
		if err := h.materializeAgentOnOwner(ctx, agent, root, h.usedAgentKeys(nil, root), a.Overwrite); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, h.migrateAgentSkillsOnOwner(ctx, agent, root)
	}
	if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) {
		root = service.ProjectSkillRootForResolver(ctx, h.projectRepo, a.ProjectID)
	}
	if h.agentLibraryMaintenanceSvc != nil {
		return struct{}{}, h.agentLibraryMaintenanceSvc.SyncRootDeclarationsForProject(ctx, root, a.ProjectID)
	}
	return struct{}{}, nil
})
