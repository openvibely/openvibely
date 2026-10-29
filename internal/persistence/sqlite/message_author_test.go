package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMessageAuthorsSurviveReloadAndQueuePromotion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	alice := models.WithPrincipal(ctx, models.Principal{UserID: "alice-id", Username: "alice"})
	bob := models.WithPrincipal(ctx, models.Principal{UserID: "bob-id", Username: "testuser1"})
	agent := createThreadInputLLMConfig(t, ctx, db)
	tasks := NewTaskRepo(db, nil)
	executions := NewExecutionRepo(db)
	inputs := NewThreadInputRepo(db)
	task := &models.Task{ProjectID: "default", Title: "Author test", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "original", AgentID: &agent.ID}
	require.NoError(t, tasks.Create(alice, task))
	original := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: task.Prompt}
	// Starting Alice's task as Bob must not relabel Alice's original prompt.
	require.NoError(t, executions.Create(bob, original))
	require.Equal(t, "alice", original.AuthorName)
	followup := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "Bob's message", IsFollowup: true}
	require.NoError(t, executions.Create(bob, followup))
	loaded, err := executions.GetByID(alice, followup.ID)
	require.NoError(t, err)
	require.Equal(t, "bob-id", loaded.AuthorUserID)
	require.Equal(t, "testuser1", loaded.AuthorName)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "Queued by Bob"}
	require.NoError(t, inputs.CreateQueued(bob, input))
	saved, err := inputs.GetByID(alice, input.ID)
	require.NoError(t, err)
	require.Equal(t, "testuser1", saved.AuthorName)
	promoted := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecQueued, PromptSent: input.Content, IsFollowup: true}
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(ctx, input.ID, promoted))
	require.Equal(t, "testuser1", promoted.AuthorName)
	loaded, err = executions.GetByID(alice, promoted.ID)
	require.NoError(t, err)
	require.Equal(t, "testuser1", loaded.AuthorName)
	list, err := executions.ListByTask(alice, task.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	for _, e := range list {
		require.NotEmpty(t, e.AuthorName)
	}
}

func TestMessageAuthorsDirectFollowupAndSteering(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	sender := models.WithPrincipal(ctx, models.Principal{UserID: "bob-id", Username: "testuser1"})
	agent := createThreadInputLLMConfig(t, ctx, db)
	task := &models.Task{ProjectID: "default", Title: "Direct attribution", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "original", AgentID: &agent.ID}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	repo := NewExecutionRepo(db)
	e := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, PromptSent: "direct", IsFollowup: true}
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "direct"}
	started, err := repo.CreateDirectTaskFollowupOrQueue(sender, e, input)
	require.NoError(t, err)
	require.True(t, started)
	require.Equal(t, "testuser1", e.AuthorName)
	loaded, err := repo.GetByID(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, "testuser1", loaded.AuthorName)
	// A legacy original prompt stays anonymous rather than guessing its owner.
	original := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	require.NoError(t, repo.Create(ctx, original))
	require.Empty(t, original.AuthorName)
	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, Content: "steer", ExpectedTurnID: original.ID, InputMode: models.ThreadInputModeSteering, InputStatus: models.ThreadInputPending}
	require.NoError(t, NewThreadInputRepo(db).CreateSteeringForActiveExecution(sender, steering, original.ID))
	loadedInput, err := NewThreadInputRepo(db).GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.Equal(t, "testuser1", loadedInput.AuthorName)
}

func TestHostedProfileUsedForMessageAttribution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	org := NewOrganizationRepo(db)
	owner, err := org.BindHostedOwner(ctx, "hosted-owner", "owner@example.com")
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, owner.UserID)
	// Background processing reloads the persisted principal, not SSO cookies.
	owner, err = org.UserByID(ctx, owner.UserID)
	require.NoError(t, err)
	sender := models.WithPrincipal(ctx, owner.Principal)
	agent := createThreadInputLLMConfig(t, ctx, db)
	task := &models.Task{ProjectID: "default", Title: "Hosted author", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "hello", AgentID: &agent.ID}
	require.NoError(t, NewTaskRepo(db, nil).Create(sender, task))
	for _, followup := range []bool{false, true} {
		execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "hello", IsFollowup: followup}
		require.NoError(t, NewExecutionRepo(db).Create(sender, execution))
		loaded, err := NewExecutionRepo(db).GetByID(ctx, execution.ID)
		require.NoError(t, err)
		require.Equal(t, "owner@example.com", loaded.AuthorName)
		require.Equal(t, models.BootstrapUserID, loaded.AuthorUserID)
	}
}
