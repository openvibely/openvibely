package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
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
	expectedUser := c.Request().Header.Get("X-Preference-User-ID")
	// Project preference writes may be replayed from browser storage. Require
	// ownership on these writes so old tabs cannot save into a different account.
	if expectedUser != "" || (h.organizationSvc != nil && (req.ProjectLocations != nil || req.PinnedProjectIDs != nil || req.ProjectID != "")) {
		principal, ok := models.PrincipalFromContext(ctx)
		if !ok || expectedUser == "" || expectedUser != principal.UserID {
			return echo.NewHTTPError(http.StatusConflict, "account changed; refresh before saving preferences")
		}
	}
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
		available := map[string]bool{models.AllProjectsID: true}
		for _, project := range projects {
			available[project.ID] = true
		}
		servers := h.remoteServerGroups(ctx)
		for _, server := range servers {
			for _, project := range server.Projects {
				available[project.ID] = true
			}
		}
		// A shared tab stays pinned while any one of its copies is reachable.
		for _, shared := range h.repoGroups(ctx, servers) {
			available[shared.ID] = true
		}
		// Tabs of saved servers that are not connected yet stay until they can be checked.
		waiting, err := h.waitingServers(ctx, servers)
		if err != nil {
			return err
		}
		wasPinned := map[string]bool{}
		for _, id := range h.pinnedProjectIDs(ctx) {
			wasPinned[id] = true
		}
		pendingRepositories := h.repositoryLookupsPending(ctx)
		seen := make(map[string]bool)
		valid := make([]string, 0, len(*req.PinnedProjectIDs))
		for _, id := range *req.PinnedProjectIDs {
			if seen[id] {
				return echo.NewHTTPError(http.StatusBadRequest, "duplicate pinned project")
			}
			seen[id] = true
			// Other windows may retain IDs deleted after their catalog was rendered.
			server, remote := remoteid.ServerOf(id)
			_, serverWaiting := waiting[server]
			// A pinned repository tab with every copy offline stays until its servers return.
			if available[id] || remote && serverWaiting || models.IsSharedProjectID(id) && (len(waiting) > 0 || pendingRepositories) && wasPinned[id] {
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
		if project == nil && h.virtualProject(ctx, projectID) == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid project")
		}
		if err := h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, projectID); err != nil {
			return err
		}
	}
	return c.NoContent(http.StatusNoContent)
}

// ProjectCatalog returns the project picker's options and shared-copy map so
// open pages can follow saved servers coming and going without reloading.
func (h *Handler) ProjectCatalog(c echo.Context) error {
	ctx := c.Request().Context()
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	servers := h.remoteServerGroups(ctx)
	ctx = layout.WithDesktopMode(ctx, h.desktopMode)
	ctx = layout.WithRemoteServers(ctx, servers)
	ctx = layout.WithSharedProjects(ctx, h.repoGroups(ctx, servers))
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().Header().Set("Cache-Control", "no-store")
	return layout.ProjectCatalog(projects, c.QueryParam("current")).Render(ctx, c.Response().Writer)
}

func (h *Handler) pinnedProjectIDs(ctx context.Context) []string {
	if h.settingsRepo == nil {
		return nil
	}
	raw, err := h.settingsRepo.Get(ctx, uiPreferencePinnedProjectIDsKey)
	if err != nil {
		return nil
	}
	var ids []string
	_ = json.Unmarshal([]byte(raw), &ids)
	return ids
}

// projectRemembered reports a pinned tab or the last open project.
func (h *Handler) projectRemembered(ctx context.Context, id string) bool {
	if h.settingsRepo == nil {
		return false
	}
	if selected, err := h.settingsRepo.Get(ctx, uiPreferenceSelectedProjectIDKey); err == nil && strings.TrimSpace(selected) == id {
		return true
	}
	for _, pinned := range h.pinnedProjectIDs(ctx) {
		if pinned == id {
			return true
		}
	}
	return false
}

// sharedTabOf names the combined tab a project belongs to, or "".
func (h *Handler) sharedTabOf(ctx context.Context, projectID string) string {
	for _, p := range h.repoGroups(ctx, h.remoteServerGroups(ctx)) {
		if sharedHasMember(&p, projectID) {
			return p.ID
		}
	}
	return ""
}

// unpinDeletedProject closes a deleted project's tab, and its combined tab
// once no reachable copy is left; copies on offline servers do not count.
func (h *Handler) unpinDeletedProject(ctx context.Context, projectID, tab string) {
	ids := h.pinnedProjectIDs(ctx)
	if ids == nil {
		return
	}
	dropTab := tab != "" && h.sharedProject(ctx, tab) == nil && !h.repositoryLookupsPending(ctx)
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != projectID && !(dropTab && id == tab) {
			kept = append(kept, id)
		}
	}
	if len(kept) == len(ids) {
		return
	}
	value, err := json.Marshal(kept)
	if err != nil {
		return
	}
	if err := h.settingsRepo.Set(ctx, uiPreferencePinnedProjectIDsKey, string(value)); err != nil {
		applog.Infof("[handler] unpin deleted project %s: %v", projectID, err)
	}
}
