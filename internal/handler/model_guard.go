package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

const (
	noModelsConfiguredMessage  = "No models configured. Add at least one model to continue."
	noModelsConfiguredLinkURL  = "/models"
	noModelsConfiguredLinkText = "Open Models"
)

func (h *Handler) hasConfiguredModels(c echo.Context) (bool, error) {
	if h.llmConfigRepo == nil {
		return false, fmt.Errorf("model repository is not configured")
	}
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil {
		return false, err
	}
	return len(agents) > 0, nil
}

func setHTMXToast(c echo.Context, message, status string) {
	setHTMXToastWithLink(c, message, status, "", "")
}

func setHTMXToastWithLink(c echo.Context, message, status, linkURL, linkText string) {
	toast := map[string]any{
		"message": message,
		"status":  status,
	}
	if linkURL != "" {
		toast["link_url"] = linkURL
	}
	if linkText != "" {
		toast["link_text"] = linkText
	}

	payload := map[string]any{
		"openvibelyToast": toast,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	c.Response().Header().Set("HX-Trigger", string(encoded))
}

func setNoModelsConfiguredHTMXToast(c echo.Context) {
	setHTMXToastWithLink(c, noModelsConfiguredMessage, "failed", noModelsConfiguredLinkURL, noModelsConfiguredLinkText)
}

func noModelsConfiguredResponse(c echo.Context) error {
	if isHTMX(c) {
		setNoModelsConfiguredHTMXToast(c)
		return c.NoContent(http.StatusNoContent)
	}
	return echo.NewHTTPError(http.StatusBadRequest, noModelsConfiguredMessage)
}
