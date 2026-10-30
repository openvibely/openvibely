package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// A missing field preserves an existing task override; an empty field restores
// the saved model default. Never mutate the persisted model configuration.
func (h *Handler) applyConversationEffort(c echo.Context, taskID string, agent *models.LLMConfig) error {
	effort, present := formValueIfPresent(c, "reasoning_effort")
	if present && effort != "" && !models.ValidConversationEffort(*agent, effort) {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported reasoning effort for selected model")
	}
	tier, tierPresent := formValueIfPresent(c, "service_tier")
	if tierPresent && !models.ValidServiceTier(*agent, tier) {
		return echo.NewHTTPError(http.StatusBadRequest, "unsupported service tier for selected model")
	}
	if taskID != "" && h.taskRepo != nil {
		var effortOverride, tierOverride *string
		if present {
			effortOverride = &effort
		}
		if tierPresent {
			tierOverride = &tier
		}
		if present || tierPresent {
			if err := h.taskRepo.SetModelOverrides(c.Request().Context(), taskID, *agent, effortOverride, tierOverride); err != nil {
				return err
			}
		}
		return h.taskRepo.ApplyModelEffort(c.Request().Context(), taskID, agent)
	}
	if tier != "" {
		agent.ServiceTier = tier
	}
	if effort != "" {
		agent.ReasoningEffort = effort
	}
	return nil
}

// Read the selected configuration's task override when opening the shared picker.
func (h *Handler) TaskThreadModelEffort(c echo.Context) error {
	settings, err := h.taskRepo.TaskModelSettings(c.Request().Context(), c.Param("taskId"), c.QueryParam("agent_id"))
	if err != nil {
		return err
	}
	if settings == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	if settings.Config.ID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid model")
	}
	return c.JSON(http.StatusOK, map[string]string{"reasoning_effort": settings.Effort, "service_tier": settings.ServiceTier})
}
