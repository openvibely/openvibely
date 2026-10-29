package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestModelServiceTierPersistenceAndIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	configs := NewLLMConfigRepo(db)
	agent := &models.LLMConfig{Name: "Fast model", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", APIKey: "fixture", ServiceTier: "priority"}
	require.NoError(t, configs.Create(ctx, agent))
	got, err := configs.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "priority", got.ServiceTier)
	agent.ServiceTier = "default"
	require.NoError(t, configs.Update(ctx, agent))
	got, err = configs.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "default", got.ServiceTier)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	tasks := NewTaskRepo(db, nil)
	require.NoError(t, tasks.SetModelServiceTier(ctx, task.ID, *agent, "priority"))
	selected := *agent
	require.NoError(t, tasks.ApplyModelEffort(ctx, task.ID, &selected))
	require.Equal(t, "priority", selected.ServiceTier)
	selected = *agent
	selected.Model = "gpt-6-astra"
	require.NoError(t, tasks.ApplyModelEffort(ctx, task.ID, &selected))
	require.Equal(t, "default", selected.ServiceTier, "model changes must not inherit overrides")
	require.NoError(t, tasks.SetModelServiceTier(ctx, task.ID, *agent, ""))
	selected = *agent
	require.NoError(t, tasks.ApplyModelEffort(ctx, task.ID, &selected))
	require.Equal(t, "default", selected.ServiceTier)
	// Queue storage preserves an explicit Standard selection.
	queue := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, Content: "queued", ServiceTier: "default"}
	require.NoError(t, queue.CreateQueued(ctx, input))
	queued, err := queue.FindOldestQueuedForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "default", queued.ServiceTier)
}

func TestQueuedTaskClaimUsesServiceTierCurrentAtClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	agent := &models.LLMConfig{Name: "Fast claim", Provider: models.ProviderOpenAI, Model: "gpt-6.1-terra", APIKey: "fixture", ServiceTier: "priority"}
	require.NoError(t, NewLLMConfigRepo(db).Create(ctx, agent))
	tasks := NewTaskRepo(db, nil)
	require.NoError(t, tasks.UpdateAgentID(ctx, task.ID, agent.ID))
	require.NoError(t, tasks.UpdateStatus(ctx, task.ID, models.StatusCompleted))
	queue := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "followup"}
	require.NoError(t, queue.CreateQueued(ctx, input))
	selected := *agent
	require.NoError(t, tasks.SetModelServiceTier(ctx, task.ID, *agent, "default"))
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.NoError(t, queue.ClaimQueuedForTaskExecutionWithModel(ctx, input.ID, exec, &agent.ID, &selected))
	require.Equal(t, "default", selected.ServiceTier)
}
