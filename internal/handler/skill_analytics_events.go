package handler

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/models"
)

func skillWriteEventType(res *agentlibrary.ImportResult) string {
	if res != nil && len(res.Created) > 0 {
		return models.SkillEventCreated
	}
	return models.SkillEventEdited
}

func (h *Handler) recordManualSkillEvent(c echo.Context, eventType, handle, scope, agentID string) {
	if h == nil || h.skillAnalyticsRepo == nil || strings.TrimSpace(handle) == "" {
		return
	}
	normalizedScope := strings.TrimSpace(scope)
	switch normalizedScope {
	case models.SkillScopeAgentOwned:
		// keep
	case models.SkillScopeProject:
		normalizedScope = models.SkillScopeProject
	default:
		normalizedScope = models.SkillScopeGlobal
	}
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == models.AllProjectsID {
		switch normalizedScope {
		case models.SkillScopeProject:
			projectID = strings.TrimSpace(c.QueryParam("skill_project_id"))
		case models.SkillScopeAgentOwned:
			if h.agentRepo != nil {
				if agent, err := h.agentRepo.GetByID(c.Request().Context(), agentID); err == nil && agent != nil && agent.ProjectID != "" {
					projectID = agent.ProjectID
				}
			}
		}
	}
	_ = h.skillAnalyticsRepo.RecordEvent(c.Request().Context(), &models.SkillAnalyticsEvent{
		ProjectID:   projectID,
		AgentID:     agentID,
		SkillScope:  normalizedScope,
		SkillHandle: strings.TrimSpace(handle),
		EventType:   normalizedSkillAnalyticsEventType(eventType),
		Source:      models.SkillEventSourceManual,
		Surface:     models.SkillSurfaceTaskThread,
	})
}

func normalizedSkillAnalyticsEventType(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case models.SkillEventCreated:
		return models.SkillEventCreated
	case models.SkillEventSelected:
		return models.SkillEventSelected
	case models.SkillEventLoaded:
		return models.SkillEventLoaded
	case models.SkillEventViewed:
		return models.SkillEventViewed
	default:
		return models.SkillEventEdited
	}
}
