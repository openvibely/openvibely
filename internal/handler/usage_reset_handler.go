package handler

import (
	"context"
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
	"time"
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
// @Success 200 {object} map[string]interface{} "Normalized reset result: outcome (reset, not_needed, already_used, no_longer_available, cooldown, in_progress, unknown) and retry_same_request"
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
	result, err := h.auditedUsageReset(c, input, func() (map[string]any, error) {
		return h.usageAnalyticsSvc.ConsumeUsageReset(c.Request().Context(), input.AccountKey, input.CreditID, input.IdempotencyKey)
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, err.Error())
	}
	return c.JSON(http.StatusOK, result)
}

// Keep an intent record even if the provider succeeds but the response or final
// audit write fails. Retry reconciliation uses the same idempotency key.
func (h *Handler) auditedUsageReset(c echo.Context, input ConsumeUsageResetRequest, consume func() (map[string]any, error)) (map[string]any, error) {
	if h.projectAccessSvc == nil {
		return consume()
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return nil, err
	}
	details, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if err := h.projectAccessSvc.RecordAction(c.Request().Context(), projectID, "usage_reset.requested", input.AccountKey, string(details)); err != nil {
		return nil, err
	}
	result, providerErr := consume()
	action := "usage_reset.succeeded"
	if providerErr != nil {
		action = "usage_reset.failed_or_unknown"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 5*time.Second)
	defer cancel()
	if err := h.projectAccessSvc.RecordAction(auditCtx, projectID, action, input.AccountKey, string(details)); err != nil {
		return nil, err
	}
	return result, providerErr
}
