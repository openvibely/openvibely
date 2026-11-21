package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
)

// instanceSettingsPath identifies server-wide controls. Project IDs on these
// URLs are navigation state, never a destination for settings writes.
func instanceSettingsPath(path string) bool {
	if capacityAPIPath(path) {
		return true
	}
	if modelManagementPath(path) || path == "/personality" || strings.HasPrefix(path, "/personality/") || path == "/organization" || strings.HasPrefix(path, "/organization/") {
		return true
	}
	if path == "/api/analytics/usage/resets" || path == "/api/analytics/usage/resets/consume" {
		return true
	}
	if path == "/api/system/health" || path == "/api/system/update" || path == "/api/system/update/apply" || path == "/api/system/update/cancel" {
		return true
	}
	if path == "/workers" || path == "/settings/workers" || path == "/settings/worktree" || strings.HasPrefix(path, "/workers/stats/") {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "channels" {
		switch parts[2] {
		case "configure", "connect", "callback", "disconnect", "remove", "test", "send-responses", "identity", "runtime-settings":
			switch parts[1] {
			case "slack", "discord", "email", "telegram", "github", "x":
				return true
			}
		}
	}
	return path == "/channels/telegram"
}

// Mixed pages show local global definitions alongside project-owned records.
// Only explicit entity/project arguments may route these reads remotely.
func mixedSettingsPath(path string) bool {
	for _, prefix := range []string{"/agents", "/skills", "/plugins", "/channels"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (h *Handler) checkSettingsAgentOwner(c echo.Context) error {
	id := c.QueryParam("agent_id")
	if strings.HasPrefix(c.Request().URL.Path, "/agents/") && c.Param("id") != "" {
		id = c.Param("id")
	}
	if _, ok := remoteid.ServerOf(id); !ok {
		return nil
	}
	agent, err := h.agentRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return err
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "Agent not found")
	}
	if agent.Scope != models.AgentScopeProject {
		return echo.NewHTTPError(http.StatusBadRequest, "Manage global agents on their server's OV app.")
	}
	return nil
}

func (h *Handler) settingsAgentProject(c echo.Context, id string) (string, error) {
	if id == "" {
		id = c.Param("id")
	}
	if id != "" {
		agent, err := h.agentRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return "", err
		}
		if agent == nil {
			return "", echo.NewHTTPError(http.StatusNotFound, "Agent not found")
		}
		if agent.Scope == models.AgentScopeProject {
			return agent.ProjectID, nil
		}
		if _, ok := remoteid.ServerOf(id); ok {
			return "", echo.NewHTTPError(http.StatusBadRequest, "Manage global agents on their server's OV app.")
		}
		return "", nil
	}
	if c.FormValue("scope") != "project" && c.QueryParam("scope") != "project" {
		// Preserve Local project context (for generation hints and alerts),
		// but never use a selected remote as a global settings destination.
		selected := strings.TrimSpace(c.QueryParam("project_id"))
		if _, remote := remoteid.ServerOf(selected); remote || models.IsAggregateProject(selected) {
			return "", nil
		}
		project, err := h.getCurrentProjectID(c)
		if err != nil {
			return "", err
		}
		if _, remote := remoteid.ServerOf(project); remote || models.IsAggregateProject(project) {
			return "", nil
		}
		return project, nil
	}
	if owner := strings.TrimSpace(c.FormValue("agent_project_id")); owner != "" {
		return owner, nil
	}
	return h.getCurrentProjectID(c)
}

func (h *Handler) listManagedAgents(c echo.Context, limit, offset int, filter persistence.AgentPageFilter) ([]models.Agent, error) {
	_, isRemote := remoteid.ServerOf(filter.ProjectID)
	if !isRemote && !models.IsAggregateProject(filter.ProjectID) {
		return h.agentRepo.ListPageFiltered(c.Request().Context(), limit, offset, filter)
	}
	var result []models.Agent
	if filter.Scope != "project" {
		local := filter
		local.ProjectID, local.Scope = "", "global"
		items, err := h.agentRepo.ListPageFiltered(c.Request().Context(), limit, offset, local)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	if filter.Scope == "global" {
		return result, nil
	}
	projects := []string{filter.ProjectID}
	if models.IsAggregateProject(filter.ProjectID) {
		var err error
		projects, err = h.cardActionProjects(c.Request().Context(), filter.ProjectID)
		if err != nil {
			return nil, err
		}
	}
	for _, project := range projects {
		part := filter
		part.ProjectID, part.Scope = project, "project"
		items, err := h.agentRepo.ListPageFiltered(c.Request().Context(), limit, offset, part)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func (h *Handler) deleteManagedAgents(c echo.Context, ids []string) error {
	for _, id := range ids {
		agent, err := h.agentRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return err
		}
		if agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Agent not found")
		}
		if agent.Scope != models.AgentScopeProject {
			if _, ok := remoteid.ServerOf(id); ok {
				return echo.NewHTTPError(http.StatusBadRequest, "Manage global agents on their server's OV app.")
			}
			continue
		}
		scope := c.QueryParam("project_id")
		if !models.InScope(c.Request().Context(), agent.ProjectID) || (scope != "" && !models.IsAggregateProject(scope) && scope != agent.ProjectID) {
			return echo.NewHTTPError(http.StatusForbidden, "Agent is outside the selected project")
		}
	}
	// Per-item ownership was checked above. Local globals and remote project
	// agents may coexist in one selection, so don't impose one server on it.
	return h.runCardBulk(c.Request().Context(), "agents", "delete", "", ids)
}

// Capacity reports describe the worker pool of this installation.
func capacityAPIPath(path string) bool {
	return path == "/api/capacity" || strings.HasPrefix(path, "/api/capacity/")
}
