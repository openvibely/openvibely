package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// WriteAgentFiles writes agent.md, skill files, and .mcp.json to workDir
// before spawning the Claude CLI. Returns a cleanup func that deletes
// the files we wrote.
func WriteAgentFiles(workDir string, agent *models.Agent) (cleanup func(), err error) {
	var createdPaths []string

	cleanup = func() {
		for _, p := range createdPaths {
			os.RemoveAll(p)
		}
	}

	// 1. Write agent.md
	agentDir := filepath.Join(workDir, ".claude", "agents")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		return cleanup, fmt.Errorf("creating agents dir: %w", err)
	}

	agentFile := filepath.Join(agentDir, agent.Name+".md")
	content := buildAgentMarkdown(agent)
	if err := os.WriteFile(agentFile, []byte(content), 0644); err != nil {
		return cleanup, fmt.Errorf("writing agent file: %w", err)
	}
	createdPaths = append(createdPaths, agentFile)

	// 2. Write skill files
	for _, skill := range agent.Skills {
		skillDir := filepath.Join(workDir, ".claude", "skills", skill.Name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			return cleanup, fmt.Errorf("creating skill dir: %w", err)
		}

		skillFile := filepath.Join(skillDir, "SKILL.md")
		skillContent := buildSkillMarkdown(&skill)
		if err := os.WriteFile(skillFile, []byte(skillContent), 0644); err != nil {
			return cleanup, fmt.Errorf("writing skill file: %w", err)
		}
		createdPaths = append(createdPaths, skillDir)
	}

	// 3. Write .mcp.json if MCP servers are configured
	if len(agent.MCPServers) > 0 {
		mcpFile := filepath.Join(workDir, ".mcp.json")
		mcpContent := buildMCPJSON(agent.MCPServers)
		if err := os.WriteFile(mcpFile, mcpContent, 0644); err != nil {
			return cleanup, fmt.Errorf("writing .mcp.json: %w", err)
		}
		createdPaths = append(createdPaths, mcpFile)
	}

	return cleanup, nil
}

// buildAgentMarkdown generates the agent .md file content with YAML frontmatter.
func buildAgentMarkdown(agent *models.Agent) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", agent.Name))
	if agent.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %q\n", agent.Description))
	}
	if agent.Model != "" && agent.Model != "inherit" {
		sb.WriteString(fmt.Sprintf("model: %s\n", agent.Model))
	}
	if len(agent.Tools) > 0 {
		toolsJSON, _ := json.Marshal(agent.Tools)
		sb.WriteString(fmt.Sprintf("tools: %s\n", string(toolsJSON)))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(agent.SystemPrompt)
	sb.WriteString("\n")
	return sb.String()
}

// buildSkillMarkdown generates a SKILL.md file content with YAML frontmatter.
func buildSkillMarkdown(skill *models.SkillConfig) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", skill.Name))
	if skill.Description != "" {
		sb.WriteString(fmt.Sprintf("description: %q\n", skill.Description))
	}
	if skill.Tools != "" {
		sb.WriteString(fmt.Sprintf("tools: %s\n", skill.Tools))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(skill.Content)
	sb.WriteString("\n")
	return sb.String()
}

// buildMCPJSON generates .mcp.json content from MCP server configs.
func buildMCPJSON(servers []models.MCPServerConfig) []byte {
	mcpMap := make(map[string]map[string]any)
	for _, s := range servers {
		entry := make(map[string]any)
		if len(s.Command) > 0 {
			entry["command"] = s.Command[0]
			if len(s.Command) > 1 {
				entry["args"] = s.Command[1:]
			}
		}
		if s.URL != "" {
			entry["url"] = s.URL
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		mcpMap[s.Name] = entry
	}

	wrapper := map[string]any{"mcpServers": mcpMap}
	b, _ := json.MarshalIndent(wrapper, "", "  ")
	return b
}
