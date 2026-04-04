package service

import (
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// ApplyAgentToSystemPrompt prepends the agent's system prompt and skill
// contents to the existing system context string. Used by API paths
// (OAuth, Anthropic, OpenAI) where we can't write files for the CLI to read.
func ApplyAgentToSystemPrompt(base string, agent *models.Agent) string {
	if agent == nil {
		return base
	}

	var parts []string

	// Add agent system prompt
	if agent.SystemPrompt != "" {
		parts = append(parts, agent.SystemPrompt)
	}

	// Add each skill's content as additional instructions
	for _, skill := range agent.Skills {
		if skill.Content != "" {
			skillSection := fmt.Sprintf("## Skill: %s\n\n%s", skill.Name, skill.Content)
			parts = append(parts, skillSection)
		}
	}

	// Add the original base context
	if base != "" {
		parts = append(parts, base)
	}

	return strings.Join(parts, "\n\n---\n\n")
}

// AgentAllowsTool checks whether the agent's tool list allows a tool by name.
// If the agent has no tool restrictions (empty list), all tools are allowed.
// Matching is case-insensitive.
func AgentAllowsTool(agent *models.Agent, toolName string) bool {
	if agent == nil || len(agent.Tools) == 0 {
		return true
	}
	lower := strings.ToLower(toolName)
	for _, t := range agent.Tools {
		if strings.ToLower(t) == lower {
			return true
		}
	}
	return false
}
