package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
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
	if taskID != "" && c.Request().Method == http.MethodGet {
		selected := c.QueryParam("agent_id")
		settings, err := h.taskRepo.TaskModelSettings(ctx, taskID, selected)
		if err != nil {
			return err
		}
		if settings == nil || settings.ProjectID != projectID {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
		if selected == "" {
			selected = settings.Config.ID
			if selected == "" {
				selected = "auto"
			}
		}
		if selected != "auto" && settings.Config.ID == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model")
		}
		return c.JSON(http.StatusOK, map[string]any{"agent_id": selected, "reasoning_effort": settings.Effort, "service_tier": settings.ServiceTier, "saved": true})
	}
	project, err := h.projectSvc.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusNotFound, "project not found")
	}
	var task *models.Task
	if taskID != "" {
		task, err = h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return err
		}
		if task == nil || task.ProjectID != projectID {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
	}
	var req conversationModelRequest
	if c.Request().Method == http.MethodPost {
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid model settings")
		}
	} else {
		req.AgentID = c.QueryParam("agent_id")
	}
	settingKey := "conversation.chat.model." + projectID
	var raw string
	prefs := conversationModelPreferences{AgentID: "auto", Overrides: map[string]conversationModelOverride{}}
	if task == nil {
		raw, err = h.settingsRepo.Get(ctx, settingKey)
		if err != nil {
			return err
		}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
				return err
			}
		}
		if prefs.Overrides == nil {
			prefs.Overrides = map[string]conversationModelOverride{}
		}
	}
	// Imports from another browser must never replace an established preference.
	if req.ImportOnly && task == nil && raw != "" {
		return c.NoContent(http.StatusNoContent)
	}
	selected := req.AgentID
	if c.Request().Method == http.MethodGet && selected == "" {
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
			return err
		}
		if agent == nil {
			if task == nil && ((c.Request().Method == http.MethodGet && req.AgentID == "") || req.ImportOnly) {
				selected = "auto"
			} else {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid model")
			}
		}
	}
	override := conversationModelOverride{}
	if c.Request().Method == http.MethodPost {
		if req.ImportOnly && task != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "imports are only supported for Chat")
		}
		if agent != nil {
			if (req.Effort != "" && !models.ValidConversationEffort(*agent, req.Effort)) || !models.ValidServiceTier(*agent, req.Tier) {
				return echo.NewHTTPError(http.StatusBadRequest, "unsupported model settings")
			}
		} else if req.Effort != "" || req.Tier != "" {
			return echo.NewHTTPError(http.StatusBadRequest, "automatic selection has no model overrides")
		}
		if task != nil {
			if agent != nil {
				if err := h.taskRepo.SetModelOverrides(ctx, task.ID, *agent, &req.Effort, &req.Tier); err != nil {
					return err
				}
				if task.AgentID == nil || *task.AgentID != agent.ID {
					if err := h.taskRepo.UpdateAgentID(ctx, task.ID, agent.ID); err != nil {
						return err
					}
				}
			} else if task.SwarmRole == models.SwarmRoleParent {
				if err := h.taskRepo.UpdateAgentID(ctx, task.ID, ""); err != nil {
					return err
				}
			}
			return c.NoContent(http.StatusNoContent)
		}
		if req.ImportOnly {
			for key, value := range req.Overrides {
				parts := strings.SplitN(key, ":", 2)
				if len(parts) != 2 {
					continue
				}
				config, err := h.llmConfigRepo.GetByID(ctx, parts[0])
				if err != nil {
					return err
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
			return err
		}
		updated, err := h.settingsRepo.CompareAndSet(ctx, settingKey, raw, string(encoded), nil)
		if err != nil {
			return err
		}
		if !updated && !req.ImportOnly {
			return echo.NewHTTPError(http.StatusConflict, "model settings changed; reopen the picker and retry")
		}
		return c.NoContent(http.StatusNoContent)
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
	return c.JSON(http.StatusOK, map[string]any{"agent_id": selected, "reasoning_effort": override.Effort, "service_tier": override.Tier, "saved": raw != "" || task != nil})
}
