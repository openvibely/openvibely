package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/web/templates/layout"
)

const (
	uiPreferenceThemeKey             = "ui.theme"
	uiPreferenceSidebarCollapsedKey  = "ui.sidebar_collapsed"
	uiPreferenceDiffViewKey          = "ui.diff_view"
	uiPreferencePinnedProjectIDsKey  = "ui.pinned_project_ids"
	uiPreferenceSelectedProjectIDKey = "ui.selected_project_id"
)

func (h *Handler) uiPreferences(ctx context.Context) layout.UIPreferences {
	if h == nil || h.settingsRepo == nil {
		return layout.UIPreferences{}
	}
	values, err := h.settingsRepo.GetMany(ctx, []string{uiPreferenceThemeKey, uiPreferenceSidebarCollapsedKey, uiPreferencePinnedProjectIDsKey, "ui.project_locations"})
	if err != nil {
		applog.Debugf("[handler] failed to load desktop UI preferences: %v", err)
		return layout.UIPreferences{}
	}
	prefs := layout.UIPreferences{ProjectLocations: values["ui.project_locations"]}
	// IDs are reconciled with the current project catalog when rendering the shell.
	_ = json.Unmarshal([]byte(values[uiPreferencePinnedProjectIDsKey]), &prefs.PinnedProjectIDs)
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

func normalizeUIDiffView(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "split":
		return "split", true
	case "inline", "unified", "":
		return "inline", true
	default:
		return "", false
	}
}

func (h *Handler) uiDiffViewPreference(ctx context.Context) string {
	if h == nil || h.settingsRepo == nil {
		return "inline"
	}
	value, err := h.settingsRepo.Get(ctx, uiPreferenceDiffViewKey)
	if err != nil {
		applog.Debugf("[handler] failed to load UI diff view preference: %v", err)
		return "inline"
	}
	if view, ok := normalizeUIDiffView(value); ok {
		return view
	}
	return "inline"
}

type uiPreferencesRequest struct {
	ProjectLocations map[string]string `json:"project_locations"`
	PinnedProjectIDs *[]string         `json:"pinned_project_ids"`
	Theme            string            `json:"theme"`
	SidebarCollapsed *bool             `json:"sidebar_collapsed"`
	DiffView         string            `json:"diff_view"`
	ProjectID        string            `json:"project_id"`
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
	if req.ProjectLocations != nil {
		for id, path := range req.ProjectLocations {
			u, err := url.Parse(path)
			if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") || u.IsAbs() || u.Host != "" || u.Query().Get("project_id") != id {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid project location")
			}
		}
		value, err := json.Marshal(req.ProjectLocations)
		if err != nil {
			return err
		}
		if err := h.settingsRepo.Set(ctx, "ui.project_locations", string(value)); err != nil {
			return err
		}
	}

	if req.PinnedProjectIDs != nil {
		if h.projectSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "projects unavailable")
		}
		projects, err := h.projectSvc.ListSelectorOptions(ctx)
		if err != nil {
			return err
		}
		available := make(map[string]bool, len(projects))
		for _, project := range projects {
			available[project.ID] = true
		}
		seen := make(map[string]bool)
		valid := make([]string, 0, len(*req.PinnedProjectIDs))
		for _, id := range *req.PinnedProjectIDs {
			if seen[id] {
				return echo.NewHTTPError(http.StatusBadRequest, "duplicate pinned project")
			}
			seen[id] = true
			// Other windows may retain IDs deleted after their catalog was rendered.
			if available[id] {
				valid = append(valid, id)
			}
		}
		value, err := json.Marshal(valid)
		if err != nil {
			return err
		}
		if err := h.settingsRepo.Set(ctx, uiPreferencePinnedProjectIDsKey, string(value)); err != nil {
			return err
		}
	}
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
	if strings.TrimSpace(req.DiffView) != "" {
		value, ok := normalizeUIDiffView(req.DiffView)
		if !ok {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid diff view")
		}
		if err := h.settingsRepo.Set(ctx, uiPreferenceDiffViewKey, value); err != nil {
			return err
		}
	}
	if projectID := strings.TrimSpace(req.ProjectID); projectID != "" {
		if h.projectSvc == nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "projects unavailable")
		}
		project, err := h.projectSvc.GetByID(ctx, projectID)
		if err != nil {
			return err
		}
		if project == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid project")
		}
		if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, projectID); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusNoContent)
}
