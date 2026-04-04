package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAgentRepo_CreateAndReadWithoutColorColumn(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewAgentRepo(db)
	ctx := context.Background()

	agent := &models.Agent{
		Name:         "No Color Agent",
		Description:  "Agent without legacy color field",
		SystemPrompt: "Do focused work.",
		Model:        "inherit",
		Tools:        []string{"Read", "Grep"},
		Plugins:      []string{"playwright@claude-plugins-official"},
		Skills: []models.SkillConfig{
			{
				Name:        "scope-and-plan",
				Description: "Understand constraints before edits",
				Tools:       "Read, Grep",
				Content:     "Review related files first.",
			},
		},
		MCPServers: []models.MCPServerConfig{
			{
				Name:    "playwright",
				Command: []string{"npx", "-y", "@playwright/mcp"},
			},
		},
	}

	if err := repo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	stored, err := repo.GetByID(ctx, agent.ID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if stored == nil {
		t.Fatalf("expected stored agent")
	}
	if stored.Name != agent.Name {
		t.Fatalf("expected name %q, got %q", agent.Name, stored.Name)
	}
	if len(stored.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(stored.Tools))
	}
	if len(stored.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(stored.Plugins))
	}
	if len(stored.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(stored.Skills))
	}
	if len(stored.MCPServers) != 1 {
		t.Fatalf("expected 1 MCP server, got %d", len(stored.MCPServers))
	}
}
