package handler

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

// effortForm is a composer's reasoning effort and service tier; nil fields
// were absent from the form.
type effortForm struct {
	Effort *string
	Tier   *string
}

func parseEffortForm(c echo.Context) effortForm {
	var f effortForm
	if v, ok := formValueIfPresent(c, "reasoning_effort"); ok {
		f.Effort = &v
	}
	if v, ok := formValueIfPresent(c, "service_tier"); ok {
		f.Tier = &v
	}
	return f
}

// A missing field preserves an existing task override; an empty field restores
// the saved model default. Never mutate the persisted model configuration.
func (h *Handler) applyConversationEffort(c echo.Context, taskID string, agent *models.LLMConfig) error {
	return actionHTTPError(h.applyEffort(c.Request().Context(), taskID, agent, parseEffortForm(c)))
}

func (h *Handler) applyEffort(ctx context.Context, taskID string, agent *models.LLMConfig, f effortForm) error {
	if f.Effort != nil && *f.Effort != "" && !models.ValidConversationEffort(*agent, *f.Effort) {
		return statusErr(http.StatusBadRequest, "unsupported reasoning effort for selected model")
	}
	if f.Tier != nil && !models.ValidServiceTier(*agent, *f.Tier) {
		return statusErr(http.StatusBadRequest, "unsupported service tier for selected model")
	}
	if taskID != "" && h.taskRepo != nil {
		if f.Effort != nil || f.Tier != nil {
			if err := h.taskRepo.SetModelOverrides(ctx, taskID, *agent, f.Effort, f.Tier); err != nil {
				return err
			}
		}
		return h.taskRepo.ApplyModelEffort(ctx, taskID, agent)
	}
	if f.Tier != nil && *f.Tier != "" {
		agent.ServiceTier = *f.Tier
	}
	if f.Effort != nil && *f.Effort != "" {
		agent.ReasoningEffort = *f.Effort
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
