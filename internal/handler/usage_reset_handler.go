package handler

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

func (h *Handler) GetUsageResetCredits(c echo.Context) error {
	if h.usageAnalyticsSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "usage service unavailable")
	}
	result, err := h.usageAnalyticsSvc.UsageResetCredits(c.Request().Context(), c.QueryParam("account_key"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

func (h *Handler) ConsumeUsageReset(c echo.Context) error {
	if h.usageAnalyticsSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "usage service unavailable")
	}
	var input struct {
		AccountKey     string `json:"account_key"`
		CreditID       string `json:"credit_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := c.Bind(&input); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid reset request")
	}
	if input.AccountKey == "" || input.CreditID == "" || input.IdempotencyKey == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "account_key, credit_id and idempotency_key are required")
	}
	result, err := h.usageAnalyticsSvc.ConsumeUsageReset(c.Request().Context(), input.AccountKey, input.CreditID, input.IdempotencyKey)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}
