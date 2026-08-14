package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/web/templates/layout"
)

const (
	uiPreferenceThemeKey            = "ui.theme"
	uiPreferenceSidebarCollapsedKey = "ui.sidebar_collapsed"
)

func (h *Handler) uiPreferences(ctx context.Context) layout.UIPreferences {
	if h == nil || h.settingsRepo == nil {
		return layout.UIPreferences{}
	}
	values, err := h.settingsRepo.GetMany(ctx, []string{uiPreferenceThemeKey, uiPreferenceSidebarCollapsedKey})
	if err != nil {
		applog.Debugf("[handler] failed to load desktop UI preferences: %v", err)
		return layout.UIPreferences{}
	}
	prefs := layout.UIPreferences{}
	if theme := strings.TrimSpace(values[uiPreferenceThemeKey]); isSafeUIPreferenceValue(theme) {
		prefs.Theme = theme
	}
	switch strings.TrimSpace(values[uiPreferenceSidebarCollapsedKey]) {
	case "true", "false":
		prefs.SidebarCollapsed = strings.TrimSpace(values[uiPreferenceSidebarCollapsedKey])
	}
	return prefs
}

func isSafeUIPreferenceValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}

type uiPreferencesRequest struct {
	Theme            string `json:"theme"`
	SidebarCollapsed *bool  `json:"sidebar_collapsed"`
}

func (h *Handler) SaveUIPreferences(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "settings unavailable")
	}
	var req uiPreferencesRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid preferences payload")
	}
	ctx := c.Request().Context()
	if theme := strings.TrimSpace(req.Theme); theme != "" {
		if !isSafeUIPreferenceValue(theme) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid theme")
		}
		if err := h.settingsRepo.Set(ctx, uiPreferenceThemeKey, theme); err != nil {
			return err
		}
	}
	if req.SidebarCollapsed != nil {
		value := "false"
		if *req.SidebarCollapsed {
			value = "true"
		}
		if err := h.settingsRepo.Set(ctx, uiPreferenceSidebarCollapsedKey, value); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusNoContent)
}
