package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) instanceSettingsOwner(ctx context.Context, projectID string) (string, authz.Permission, error) {
	if err := h.requireActionAdmin(ctx); err != nil {
		return "", authz.Manage, err
	}
	if projectID != "" && !models.IsAggregateProject(projectID) {
		if err := h.requireActionProject(ctx, projectID, authz.View); err != nil {
			return "", authz.View, err
		}
	}
	return instanceActionProject, authz.Manage, nil
}

type pluginOperationArgs struct {
	ProjectID, AgentID, Operation string
	PluginID, Scope               string `remote:"raw"`
}
type pluginOperationResult struct{ Warning, EnableError string }

var actPluginOperation = defineAction("plugin.operation", func(h *Handler, ctx context.Context, a pluginOperationArgs) (string, authz.Permission, error) {
	if a.AgentID != "" {
		agent, err := h.agentRepo.GetByID(ctx, a.AgentID)
		if err != nil {
			return "", authz.Manage, err
		}
		if agent == nil {
			return "", authz.Manage, statusErr(http.StatusNotFound, "agent not found")
		}
		if agent.ProjectID != "" {
			if err := h.requireActionProject(ctx, agent.ProjectID, authz.Manage); err != nil {
				return "", authz.Manage, err
			}
		}
	}
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a pluginOperationArgs) (pluginOperationResult, error) {
	if strings.TrimSpace(a.PluginID) == "" {
		return pluginOperationResult{}, statusErr(http.StatusBadRequest, "plugin_id is required")
	}
	switch a.Operation {
	case "install":
		warning, err, enableErr := h.installPluginOnOwner(ctx, a.ProjectID, a.PluginID, a.Scope, a.AgentID)
		result := pluginOperationResult{Warning: warning}
		if enableErr != nil {
			result.EnableError = shortGenerationError(enableErr)
		}
		return result, err
	case "uninstall":
		return pluginOperationResult{}, h.uninstallPluginOnOwner(ctx, a.ProjectID, a.PluginID)
	default:
		return pluginOperationResult{}, statusErr(http.StatusBadRequest, "unknown plugin operation")
	}
})

type generateAgentArgs struct {
	ProjectID, AgentID string
	Description        string   `remote:"raw"`
	Plugins            []string `remote:"raw"`
}

var actGenerateAgent = defineAction("agent.generate", func(h *Handler, ctx context.Context, a generateAgentArgs) (string, authz.Permission, error) {
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a generateAgentArgs) (generatedAgentResponse, error) {
	if strings.TrimSpace(a.Description) == "" {
		return generatedAgentResponse{}, statusErr(http.StatusBadRequest, "description is required")
	}
	return h.generateAgentOnOwner(ctx, a.ProjectID, a.Description, a.Plugins)
})

var actPluginState = defineAction("plugin.state", func(h *Handler, ctx context.Context, a pluginOperationArgs) (string, authz.Permission, error) {
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a pluginOperationArgs) (models.PluginState, error) {
	state, err := discoverPluginStateFn(ctx)
	if err != nil {
		return models.PluginState{}, err
	}
	state.Runtime = enrichRuntimePluginIDs(ctx, pluginMCPRuntimeStateFn(), state.Installed)
	return state, nil
})
