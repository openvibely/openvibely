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
	if taskID != "" && h.taskRepo != nil {
		if present {
			if err := h.taskRepo.SetModelEffort(c.Request().Context(), taskID, *agent, effort); err != nil {
				return err
			}
		}
		return h.taskRepo.ApplyModelEffort(c.Request().Context(), taskID, agent)
	}
	if effort != "" {
		agent.ReasoningEffort = effort
	}
	return nil
}

// Read the selected configuration's task override when opening the shared picker.
func (h *Handler) TaskThreadModelEffort(c echo.Context) error {
	task, err := h.taskSvc.GetByID(c.Request().Context(), c.Param("taskId"))
	if err != nil {
		return err
	}
	if task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), c.QueryParam("agent_id"))
	if err != nil {
		return err
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid model")
	}
	effort, err := h.taskRepo.ModelEffort(c.Request().Context(), task.ID, *agent)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"reasoning_effort": effort})
}
