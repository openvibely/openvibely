package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"net/http"
	"runtime"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Settings actions carry an explicit owning project even for instance-wide
// settings. The selected desktop workspace must never select a local store.
type personalityOperationArgs struct {
	ProjectID, Key, Operation string
	Personality               *models.CustomPersonality `remote:"raw"`
}

var actPersonalityOperation = defineAction("personality.operation", func(h *Handler, ctx context.Context, a personalityOperationArgs) (string, authz.Permission, error) {
	if err := h.requireActionAdmin(ctx); err != nil {
		return "", authz.Manage, err
	}
	if a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) {
		if err := h.requireActionProject(ctx, a.ProjectID, authz.View); err != nil {
			return "", authz.View, err
		}
	}
	return instanceActionProject, authz.Manage, nil
}, func(h *Handler, ctx context.Context, a personalityOperationArgs) (*models.CustomPersonality, error) {
	if a.Operation == "select" {
		if !service.IsAvailablePersonalityKey(ctx, a.Key, h.customPersonalityRepo) {
			return nil, statusErr(http.StatusBadRequest, "Unknown personality")
		}
		if h.settingsRepo != nil {
			return nil, h.settingsRepo.Set(ctx, "personality", a.Key)
		}
		return nil, nil
	}
	if h.customPersonalityRepo == nil {
		return nil, statusErr(http.StatusServiceUnavailable, "Custom personalities not configured")
	}
	existing, err := h.customPersonalityRepo.GetByKey(ctx, a.Key)
	if err != nil {
		return nil, err
	}
	switch a.Operation {
	case "create":
		if existing != nil {
			return nil, statusErr(http.StatusConflict, "A custom personality with this key already exists")
		}
		if a.Personality == nil || a.Key == "" {
			return nil, statusErr(http.StatusBadRequest, "Key is required")
		}
		a.Personality.Key = a.Key
		err = h.customPersonalityRepo.Create(ctx, a.Personality)
	case "update":
		if a.Personality == nil {
			return nil, statusErr(http.StatusBadRequest, "Personality is required")
		}
		a.Personality.Key = a.Key
		if existing != nil {
			err = h.customPersonalityRepo.Update(ctx, a.Key, a.Personality)
		} else if service.IsPresetPersonality(a.Key) {
			err = h.customPersonalityRepo.Create(ctx, a.Personality)
		} else {
			return nil, statusErr(http.StatusNotFound, "Custom personality not found")
		}
	case "delete":
		if existing == nil && !service.IsPresetPersonality(a.Key) {
			return nil, statusErr(http.StatusNotFound, "Custom personality not found")
		}
		if existing != nil {
			if err := h.customPersonalityRepo.Delete(ctx, a.Key); err != nil {
				return nil, err
			}
		}
		if h.settingsRepo != nil {
			current, err := h.settingsRepo.Get(ctx, "personality")
			if err != nil {
				return nil, err
			}
			if current == a.Key {
				return nil, h.settingsRepo.Set(ctx, "personality", "")
			}
		}
		return nil, nil
	default:
		return nil, statusErr(http.StatusBadRequest, "Unknown personality operation")
	}
	if err != nil {
		return nil, err
	}
	return h.customPersonalityRepo.GetByKey(ctx, a.Key)
})

type agentHooksArgs struct {
	AgentID string
	Hooks   []hookSavePayload
}

var actSaveAgentHooks = defineAction("agent.hooks.save", func(h *Handler, ctx context.Context, a agentHooksArgs) (string, authz.Permission, error) {
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
	if agent.Scope == models.AgentScopeProject && agent.ProjectID != "" {
		return agent.ProjectID, authz.Manage, nil
	}
	return instanceActionProject, authz.Manage, nil
}, func(h *Handler, ctx context.Context, a agentHooksArgs) ([]models.AgentLifecycleHook, error) {
	if err := h.reconcileAgentLifecycleHooks(ctx, a.AgentID, a.Hooks); err != nil {
		return nil, err
	}
	return h.lifecycleRepo.HooksByAgent(ctx, a.AgentID)
})

type modelAccountArgs struct {
	ID, Operation, Name, ReplacementID string
	ModelIDs                           []string
}

var actModelAccount = defineAction("model.account", func(h *Handler, ctx context.Context, a modelAccountArgs) (string, authz.Permission, error) {
	return instanceActionProject, authz.Manage, h.requireActionAdmin(ctx)
}, func(h *Handler, ctx context.Context, a modelAccountArgs) (bool, error) {
	switch a.Operation {
	case "rename":
		name := strings.TrimSpace(a.Name)
		if name == "" || len(name) > 200 {
			return false, statusErr(http.StatusBadRequest, "Invalid OAuth account name")
		}
		return false, h.llmConfigRepo.RenameOAuthConnection(ctx, a.ID, name)
	case "move":
		if len(a.ModelIDs) == 0 || len(a.ModelIDs) > bulkDeleteMaxItems {
			return false, statusErr(http.StatusBadRequest, "Invalid model selection")
		}
		return false, h.llmConfigRepo.MoveModelsToOAuthConnection(ctx, a.ModelIDs, a.ID)
	case "delete-account":
		connection, err := h.llmConfigRepo.GetOAuthConnectionByID(ctx, a.ID)
		if err != nil {
			return false, err
		}
		if connection == nil {
			return false, statusErr(http.StatusNotFound, "OAuth account not found")
		}
		if connection.LinkedModels != 0 {
			return false, statusErr(http.StatusConflict, "OAuth account is still linked to models")
		}
		return false, h.llmConfigRepo.DeleteOAuthConnection(ctx, a.ID)
	}
	cfg, err := h.llmConfigRepo.GetByID(ctx, a.ID)
	if err != nil {
		return false, err
	}
	if cfg == nil {
		return false, statusErr(http.StatusNotFound, "model not found")
	}
	switch a.Operation {
	case "status":
		return cfg.HasValidOAuthToken(), nil
	case "disconnect":
		if cfg.OAuthConnectionID == "" || cfg.AuthMethod != models.AuthMethodOAuth || (cfg.Provider != models.ProviderOpenAI && cfg.Provider != models.ProviderAnthropic) {
			return false, statusErr(http.StatusBadRequest, "model does not use a shared OAuth account")
		}
		ok, err := h.llmConfigRepo.DisconnectLinkedOAuthConnection(ctx, cfg.ID, cfg.OAuthConnectionID, cfg.OAuthConfigRevision, cfg.Provider)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, statusErr(http.StatusConflict, "OAuth account changed; reload and try again")
		}
	case "default":
		if !models.BuiltInModelSupportedForAuth(cfg.Provider, cfg.Model, cfg.AuthMethod) {
			return false, statusErr(http.StatusBadRequest, "Select a supported model")
		}
		cfg.IsDefault = true
		if err := h.llmConfigRepo.Update(ctx, cfg); err != nil {
			return false, err
		}
	case "delete":
		if mixtures, err := h.mixturesUsingModel(ctx, a.ID); err != nil {
			return false, err
		} else if len(mixtures) > 0 {
			return false, statusErr(http.StatusBadRequest, "Remove this model from its mixtures before deleting")
		}
		if cfg.IsDefault && a.ReplacementID != "" {
			replacement, err := h.llmConfigRepo.GetByID(ctx, a.ReplacementID)
			if err != nil {
				return false, err
			}
			if replacement == nil || replacement.ID == cfg.ID || !models.BuiltInModelSupportedForAuth(replacement.Provider, replacement.Model, replacement.AuthMethod) {
				return false, statusErr(http.StatusBadRequest, "Invalid new default model selection")
			}
			if err := h.llmConfigRepo.TransferDefaultAndDelete(ctx, cfg.ID, replacement.ID); err != nil {
				return false, err
			}
		} else if err := h.llmConfigRepo.Delete(ctx, cfg.ID); err != nil {
			return false, err
		}
	default:
		return false, statusErr(http.StatusBadRequest, "Unknown model operation")
	}
	if h.workerSvc != nil {
		h.workerSvc.DispatchNext()
	}
	return false, nil
})

type projectPathArgs struct{ ProjectID string }

var actProjectPathHealth = defineAction("project.path-health", func(h *Handler, ctx context.Context, a projectPathArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.View, nil
}, func(h *Handler, ctx context.Context, a projectPathArgs) (models.ProjectRepoPathHealth, error) {
	p, err := h.projectSvc.GetByID(ctx, a.ProjectID)
	if err != nil {
		return models.ProjectRepoPathHealth{}, err
	}
	if p == nil {
		return models.ProjectRepoPathHealth{}, statusErr(http.StatusNotFound, "project not found")
	}
	return h.projectSvc.RepoPathHealth(p), nil
})

type ollamaDiscoveryArgs struct {
	ModelID        string
	ProjectID      string
	BaseURL, Model string `remote:"raw"`
	Metadata       bool
}
type ollamaDiscoveryResult struct {
	Models   []service.OllamaModelInfo
	Metadata service.OllamaModelMetadata
}

var actOllamaDiscovery = defineAction("model.ollama-discovery", func(h *Handler, ctx context.Context, a ollamaDiscoveryArgs) (string, authz.Permission, error) {
	return instanceActionProject, authz.View, h.requireActionAdmin(ctx)
}, func(h *Handler, ctx context.Context, a ollamaDiscoveryArgs) (ollamaDiscoveryResult, error) {
	if a.Metadata {
		return ollamaDiscoveryResult{Metadata: service.DiscoverOllamaModelMetadata(ctx, a.BaseURL, a.Model)}, nil
	}
	entries, err := service.ListOllamaModels(ctx, a.BaseURL)
	return ollamaDiscoveryResult{Models: entries}, err
})

type skillPreviewArgs struct {
	ProjectID string
	Content   []byte `remote:"raw"`
}
type skillPreviewResult struct {
	Dependencies []skilldeps.Dependency `json:"dependencies"`
	Platform     string                 `json:"platform"`
}

var actSkillPreview = defineAction("skill.preview", func(h *Handler, ctx context.Context, a skillPreviewArgs) (string, authz.Permission, error) {
	return h.instanceSettingsOwner(ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a skillPreviewArgs) (skillPreviewResult, error) {
	if len(a.Content) > 1<<20 {
		return skillPreviewResult{}, statusErr(http.StatusBadRequest, "skill preview exceeds size limit")
	}
	deps, err := skilldeps.Parse(a.Content)
	if err != nil {
		return skillPreviewResult{}, statusErr(http.StatusBadRequest, err.Error())
	}
	return skillPreviewResult{Dependencies: deps, Platform: runtime.GOOS + "/" + runtime.GOARCH}, nil
})
