package repository

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTaskModelEffort_UsesDedicatedWriter(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "task-effort.db"))
	require.NoError(t, err)
	defer connections.Close()
	unregister := RegisterDedicatedWriter(connections.Reader, connections.Writer)
	defer unregister()

	ctx := context.Background()
	project := createThreadInputProject(t, ctx, connections.Writer)
	task := createThreadInputTask(t, ctx, connections.Writer, project.ID)
	agent := &models.LLMConfig{Name: "Effort test model", Provider: models.ProviderOpenAI, Model: "gpt-5.5", APIKey: "test", ReasoningEffort: "medium"}
	require.NoError(t, NewLLMConfigRepo(connections.Writer).Create(ctx, agent))

	repo := NewTaskRepo(connections.Reader, nil)
	require.NoError(t, repo.SetModelEffort(ctx, task.ID, *agent, "high"))
	effort, err := repo.ModelEffort(ctx, task.ID, *agent)
	require.NoError(t, err)
	require.Equal(t, "high", effort)
}

func TestTaskModelEffort_IsolatedAndModelBound(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewTaskRepo(db, nil)
	project := createThreadInputProject(t, ctx, db)
	task := createThreadInputTask(t, ctx, db, project.ID)
	other := &models.Task{ProjectID: project.ID, Title: "Other task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "test"}
	require.NoError(t, repo.Create(ctx, other))
	agent := createThreadInputLLMConfig(t, ctx, db)
	agent.Provider = models.ProviderOpenAI
	agent.Model = "gpt-5.5"
	agent.ReasoningEffort = "medium"
	require.NoError(t, repo.SetModelEffort(ctx, task.ID, *agent, "high"))
	copy := *agent
	require.NoError(t, repo.ApplyModelEffort(ctx, task.ID, &copy))
	require.Equal(t, "high", copy.ReasoningEffort)
	copy = *agent
	require.NoError(t, repo.ApplyModelEffort(ctx, other.ID, &copy))
	require.Equal(t, "medium", copy.ReasoningEffort)
	copy = *agent
	copy.Model = "gpt-5.4"
	require.NoError(t, repo.ApplyModelEffort(ctx, task.ID, &copy))
	require.Equal(t, "medium", copy.ReasoningEffort)
	queue := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, Content: "queued", ReasoningEffort: "high"}
	require.NoError(t, queue.CreateQueued(ctx, input))
	got, err := queue.FindOldestQueuedForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
}

func TestTaskModelEffort_SwarmPlannerUsesParentConversation(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewTaskRepo(db, nil)
	project := createThreadInputProject(t, ctx, db)
	parent := &models.Task{ProjectID: project.ID, Title: "Swarm", Category: models.CategoryActive, Status: models.StatusBlocked, SwarmRole: models.SwarmRoleParent}
	require.NoError(t, repo.Create(ctx, parent))
	agent := createThreadInputLLMConfig(t, ctx, db)
	agent.Provider = models.ProviderOpenAI
	agent.Model = "gpt-5.5"
	require.NoError(t, repo.SetModelEffort(ctx, parent.ID, *agent, "high"))
	for _, role := range []models.SwarmRole{models.SwarmRolePlanner, models.SwarmRoleWorker} {
		child := &models.Task{ProjectID: project.ID, Title: string(role), Category: models.CategoryActive, Status: models.StatusPending, ParentTaskID: &parent.ID, SwarmRole: role}
		require.NoError(t, repo.Create(ctx, child))
		effort, err := repo.ModelEffort(ctx, child.ID, *agent)
		require.NoError(t, err)
		if role == models.SwarmRolePlanner {
			require.Equal(t, "high", effort)
		} else {
			require.Empty(t, effort)
		}
		require.NoError(t, repo.SetModelEffort(ctx, child.ID, *agent, ""))
		effort, err = repo.ModelEffort(ctx, child.ID, *agent)
		require.NoError(t, err)
		require.Empty(t, effort)
	}
}
