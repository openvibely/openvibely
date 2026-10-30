package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTaskModelSettingsJoinedSelectionAndInheritance(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	repo := NewTaskRepo(db, nil)
	project := createThreadInputProject(t, ctx, db)
	agent := &models.LLMConfig{Name: "First", IsDefault: true, Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", ReasoningEffort: "low", ServiceTier: "priority", APIKey: "secret"}
	configs := NewLLMConfigRepo(db)
	require.NoError(t, configs.Create(ctx, agent))
	other := &models.LLMConfig{Name: "Other", Provider: models.ProviderOpenAI, Model: "gpt-6.1-terra", ReasoningEffort: "medium"}
	require.NoError(t, configs.Create(ctx, other))
	parent := &models.Task{ProjectID: project.ID, Title: "Parent", Category: models.CategoryActive, Status: models.StatusBlocked, SwarmRole: models.SwarmRoleParent, AgentID: &agent.ID}
	require.NoError(t, repo.Create(ctx, parent))
	child := &models.Task{ProjectID: project.ID, Title: "Planner", Category: models.CategoryActive, Status: models.StatusPending, SwarmRole: models.SwarmRolePlanner, ParentTaskID: &parent.ID, AgentID: &agent.ID}
	require.NoError(t, repo.Create(ctx, child))
	effort, tier := "high", "default"
	require.NoError(t, repo.SetModelOverrides(ctx, parent.ID, *agent, &effort, &tier))
	counter.Reset()
	counter.SetEnabled(true)
	settings, err := repo.TaskModelSettings(ctx, child.ID, "")
	counter.SetEnabled(false)
	require.Len(t, counter.Statements(), 1, "task, defaults and both overrides must use one query")
	require.NoError(t, err)
	require.Equal(t, "high", settings.Effort)
	require.Equal(t, "default", settings.ServiceTier)
	require.Equal(t, "low", settings.Config.ReasoningEffort)
	require.Equal(t, "priority", settings.Config.ServiceTier)
	require.Empty(t, settings.Config.APIKey)
	// An explicit effort default must not erase inherited Fast mode.
	empty := ""
	require.NoError(t, repo.SetModelOverrides(ctx, child.ID, *agent, &empty, nil))
	settings, err = repo.TaskModelSettings(ctx, child.ID, agent.ID)
	require.NoError(t, err)
	require.Empty(t, settings.Effort)
	require.Equal(t, "default", settings.ServiceTier)
	require.NoError(t, repo.SetModelOverrides(ctx, child.ID, *agent, nil, &empty))
	settings, err = repo.TaskModelSettings(ctx, child.ID, agent.ID)
	require.NoError(t, err)
	require.Empty(t, settings.Effort)
	require.Empty(t, settings.ServiceTier)
	// Selecting another configuration never reuses the assigned one's overrides.
	settings, err = repo.TaskModelSettings(ctx, parent.ID, other.ID)
	require.NoError(t, err)
	require.Equal(t, other.ID, settings.Config.ID)
	require.Empty(t, settings.Effort)
	_, err = db.Exec(`UPDATE projects SET default_agent_config_id=? WHERE id=?`, other.ID, project.ID)
	require.NoError(t, err)
	settings, err = repo.TaskModelSettings(ctx, parent.ID, "default")
	require.NoError(t, err)
	require.Equal(t, other.ID, settings.Config.ID)
	_, err = db.Exec(`UPDATE projects SET default_agent_config_id=NULL WHERE id=?`, project.ID)
	require.NoError(t, err)
	settings, err = repo.TaskModelSettings(ctx, parent.ID, "default")
	require.NoError(t, err)
	require.Equal(t, agent.ID, settings.Config.ID)
	settings, err = repo.TaskModelSettings(ctx, "missing", agent.ID)
	require.NoError(t, err)
	require.Nil(t, settings)
	settings, err = repo.TaskModelSettings(ctx, parent.ID, "missing")
	require.NoError(t, err)
	require.Empty(t, settings.Config.ID)
	// A changed underlying model cannot inherit another model's overrides.
	agent.Model = "gpt-6.1-terra"
	require.NoError(t, configs.Update(ctx, agent))
	settings, err = repo.TaskModelSettings(ctx, parent.ID, agent.ID)
	require.NoError(t, err)
	require.Empty(t, settings.Effort)
	require.Empty(t, settings.ServiceTier)
}
