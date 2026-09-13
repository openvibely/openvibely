package service

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// IsRequiredSystemAgent reports whether a protected system agent must remain
// enabled for lifecycle/task bookkeeping correctness.
func IsRequiredSystemAgent(agent *models.Agent) bool {
	return systemAgentKind(agent) == models.AgentSystemKindGoal
}

// IsUserDisableableSystemAgent reports whether users may turn off the protected
// system agent from settings. Disabled agents are skipped by lifecycle hook
// discovery and their maintenance schedules use the normal visible schedule row.
func IsUserDisableableSystemAgent(agent *models.Agent) bool {
	switch systemAgentKind(agent) {
	case models.AgentSystemKindSkillCurator, models.AgentSystemKindMemoryCurator:
		return true
	default:
		return false
	}
}

func systemAgentKind(agent *models.Agent) string {
	if agent == nil {
		return ""
	}
	if kind := strings.TrimSpace(agent.SystemKind); kind != "" {
		return kind
	}
	return strings.TrimSpace(agent.Key)
}

func explicitSystemAgentModelID(agent *models.Agent) string {
	if agent == nil {
		return ""
	}
	model := strings.TrimSpace(agent.Model)
	if model == "" || strings.EqualFold(model, "inherit") {
		return ""
	}
	return model
}
