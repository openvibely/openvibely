package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type conversationModelOverride struct {
	Effort string `json:"reasoning_effort"`
	Tier   string `json:"service_tier"`
}
type conversationModelPreferences struct {
	AgentID   string                               `json:"agent_id"`
	Overrides map[string]conversationModelOverride `json:"overrides"`
}
type conversationModelRequest struct {
	AgentID    string                               `json:"agent_id" form:"agent_id"`
	Effort     string                               `json:"reasoning_effort" form:"reasoning_effort"`
	Tier       string                               `json:"service_tier" form:"service_tier"`
	ImportOnly bool                                 `json:"import_only" form:"import_only"`
	Overrides  map[string]conversationModelOverride `json:"overrides"`
}

// ConversationModelSettings serves browser Chat and task pickers. Project scope
// is explicit; it must never depend on another tab's selected-project preference.
func (h *Handler) ConversationModelSettings(c echo.Context) error {
	ctx := c.Request().Context()
	c.Response().Header().Set("Cache-Control", "no-store")
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id required")
	}
	taskID := c.QueryParam("task_id")
	var req conversationModelRequest
	if c.Request().Method == http.MethodPost {
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model settings")
		}
	} else {
		req.AgentID = c.QueryParam("agent_id")
	}
	result, err := actConversationModel(h, ctx, conversationModelArgs{ProjectID: projectID, TaskID: taskID, Write: c.Request().Method == http.MethodPost, Request: req})
	if err != nil {
		return err
	}
	if result.NoContent {
		return c.NoContent(http.StatusNoContent)
	}
	return c.JSON(http.StatusOK, result)
}

type conversationModelArgs struct {
	ProjectID, TaskID string
	Write             bool
	Request           conversationModelRequest
}
type conversationModelResult struct {
	AgentID   string `json:"agent_id"`
	Effort    string `json:"reasoning_effort"`
	Tier      string `json:"service_tier"`
	Saved     bool   `json:"saved"`
	NoContent bool   `json:"-"`
}

var actConversationModel = defineAction("conversation.model", func(h *Handler, ctx context.Context, a conversationModelArgs) (string, authz.Permission, error) {
	permission := authz.View
	if a.Write {
		permission = authz.Edit
	}
	return a.ProjectID, permission, nil
}, func(h *Handler, ctx context.Context, a conversationModelArgs) (conversationModelResult, error) {
	projectID, taskID, req := a.ProjectID, a.TaskID, a.Request
	if taskID != "" && !a.Write {
		selected := req.AgentID
		settings, err := h.taskRepo.TaskModelSettings(ctx, taskID, selected)
		if err != nil {
			return conversationModelResult{}, err
		}
		if settings == nil || settings.ProjectID != projectID {
			return conversationModelResult{}, statusErr(http.StatusNotFound, "task not found")
		}
		if selected == "" {
			selected = settings.Config.ID
			if selected == "" {
				selected = "auto"
			}
		}
		if selected != "auto" && settings.Config.ID == "" {
			return conversationModelResult{}, statusErr(http.StatusBadRequest, "invalid model")
		}
		return conversationModelResult{AgentID: selected, Effort: settings.Effort, Tier: settings.ServiceTier, Saved: true}, nil
	}
	project, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		return conversationModelResult{}, err
	}
	if project == nil {
		return conversationModelResult{}, statusErr(http.StatusNotFound, "project not found")
	}
	var task *models.Task
	if taskID != "" {
		task, err = h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return conversationModelResult{}, err
		}
		if task == nil || task.ProjectID != projectID {
			return conversationModelResult{}, statusErr(http.StatusNotFound, "task not found")
		}
	}
	settingKey := "conversation.chat.model." + projectID
	if projectID == models.AllProjectsID {
		if principal, ok := models.PrincipalFromContext(ctx); ok {
			settingKey += ".user." + principal.UserID
		}
	}
	var raw string
	prefs := conversationModelPreferences{AgentID: "auto", Overrides: map[string]conversationModelOverride{}}
	if task == nil {
		raw, err = h.settingsRepo.Get(ctx, settingKey)
		if err != nil {
			return conversationModelResult{}, err
		}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
				return conversationModelResult{}, err
			}
		}
		if prefs.Overrides == nil {
			prefs.Overrides = map[string]conversationModelOverride{}
		}
	}
	// Imports from another browser must never replace an established preference.
	if req.ImportOnly && task == nil && raw != "" {
		return conversationModelResult{NoContent: true}, nil
	}
	selected := req.AgentID
	if !a.Write && selected == "" {
		selected = prefs.AgentID
	}
	if selected == "" {
		selected = "auto"
	}
	var agent *models.LLMConfig
	if selected != "" && selected != "auto" {
		if selected == "default" {
			agent, err = h.selectTaskAgent(ctx, projectID, selected, "", false)
		} else {
			agent, err = h.llmConfigRepo.GetByID(ctx, selected)
		}
		if err != nil {
			return conversationModelResult{}, err
		}
		if agent == nil {
			if task == nil && ((!a.Write && req.AgentID == "") || req.ImportOnly) {
				selected = "auto"
			} else {
				return conversationModelResult{}, statusErr(http.StatusBadRequest, "invalid model")
			}
		}
	}
	override := conversationModelOverride{}
	if a.Write {
		if req.ImportOnly && task != nil {
			return conversationModelResult{}, statusErr(http.StatusBadRequest, "imports are only supported for Chat")
		}
		if agent != nil {
			if (req.Effort != "" && !models.ValidConversationEffort(*agent, req.Effort)) || !models.ValidServiceTier(*agent, req.Tier) {
				return conversationModelResult{}, statusErr(http.StatusBadRequest, "unsupported model settings")
			}
		} else if req.Effort != "" || req.Tier != "" {
			return conversationModelResult{}, statusErr(http.StatusBadRequest, "automatic selection has no model overrides")
		}
		if task != nil {
			if agent != nil {
				if err := h.taskRepo.SetModelOverrides(ctx, task.ID, *agent, &req.Effort, &req.Tier); err != nil {
					return conversationModelResult{}, err
				}
				if task.AgentID == nil || *task.AgentID != agent.ID {
					if err := h.taskRepo.UpdateAgentID(ctx, task.ID, agent.ID); err != nil {
						return conversationModelResult{}, err
					}
				}
			} else if task.SwarmRole == models.SwarmRoleParent {
				if err := h.taskRepo.UpdateAgentID(ctx, task.ID, ""); err != nil {
					return conversationModelResult{}, err
				}
			}
			return conversationModelResult{NoContent: true}, nil
		}
		if req.ImportOnly {
			for key, value := range req.Overrides {
				parts := strings.SplitN(key, ":", 2)
				if len(parts) != 2 {
					continue
				}
				config, err := h.llmConfigRepo.GetByID(ctx, parts[0])
				if err != nil {
					return conversationModelResult{}, err
				}
				if config == nil || config.Model != parts[1] {
					continue
				}
				if value.Effort != "" && !models.ValidConversationEffort(*config, value.Effort) {
					value.Effort = ""
				}
				if !models.ValidServiceTier(*config, value.Tier) {
					value.Tier = ""
				}
				prefs.Overrides[key] = value
			}
		}
		prefs.AgentID = selected
		if agent != nil && !req.ImportOnly {
			prefs.Overrides[agent.ID+":"+agent.Model] = conversationModelOverride{req.Effort, req.Tier}
		}
		encoded, err := json.Marshal(prefs)
		if err != nil {
			return conversationModelResult{}, err
		}
		updated, err := h.settingsRepo.CompareAndSet(ctx, settingKey, raw, string(encoded), nil)
		if err != nil {
			return conversationModelResult{}, err
		}
		if !updated && !req.ImportOnly {
			return conversationModelResult{}, statusErr(http.StatusConflict, "model settings changed; reopen the picker and retry")
		}
		return conversationModelResult{NoContent: true}, nil
	}
	if agent != nil {
		override = prefs.Overrides[agent.ID+":"+agent.Model]
		if override.Effort != "" && !models.ValidConversationEffort(*agent, override.Effort) {
			override.Effort = ""
		}
		if !models.ValidServiceTier(*agent, override.Tier) {
			override.Tier = ""
		}
	}
	return conversationModelResult{AgentID: selected, Effort: override.Effort, Tier: override.Tier, Saved: raw != "" || task != nil}, nil
})
