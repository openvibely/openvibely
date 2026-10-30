package handler

import (
	"github.com/labstack/echo/v4"
	"net/http"
)

// GetUsageResetCredits returns available provider-owned usage reset credits.
// @Summary Get usage reset credits
// @Tags analytics
// @Produce json
// @Param account_key query string true "Connected OAuth account key"
// @Success 200 {object} map[string]interface{} "Provider reset credits"
// @Failure 502 {object} ErrorResponse "Account or provider request failed"
// @Failure 503 {object} ErrorResponse "Usage service unavailable"
// @Router /api/analytics/usage/resets [get]
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

type ConsumeUsageResetRequest struct {
	AccountKey     string `json:"account_key"`
	CreditID       string `json:"credit_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ConsumeUsageReset redeems a selected provider-owned reset credit.
// @Summary Consume a usage reset credit
// @Tags analytics
// @Accept json
// @Produce json
// @Param request body ConsumeUsageResetRequest true "Account, credit, and idempotency key"
// @Success 200 {object} map[string]interface{} "Provider reset result"
// @Failure 400 {object} ErrorResponse "Invalid reset request"
// @Failure 502 {object} ErrorResponse "Account or provider request failed"
// @Failure 503 {object} ErrorResponse "Usage service unavailable"
// @Router /api/analytics/usage/resets/consume [post]
func (h *Handler) ConsumeUsageReset(c echo.Context) error {
	if h.usageAnalyticsSvc == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "usage service unavailable")
	}
	var input ConsumeUsageResetRequest
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
