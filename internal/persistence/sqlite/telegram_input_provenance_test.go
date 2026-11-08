package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTelegramQueuedInputPreservesSenderAcrossRecovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	auth := NewTelegramAuthRepo(db)
	entry := &models.TelegramAuthorizedUser{TelegramUserID: 123, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: models.BootstrapUserID}}
	require.NoError(t, auth.Create(owner, entry))
	admitted := models.WithTelegramIngressIdentity(owner, models.TelegramIngressIdentity{ExternalUserID: 123, UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	agent := createThreadInputLLMConfig(t, owner, db)
	require.NoError(t, NewTaskRepo(db, nil).UpdateStatus(owner, task.ID, models.StatusCompleted))
	inputs := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "Continue", Source: models.TaskOriginTelegram, TelegramChatID: -987}
	require.NoError(t, inputs.CreateQueued(admitted, input))
	saved, err := inputs.GetByID(t.Context(), input.ID)
	require.NoError(t, err)
	require.EqualValues(t, 123, saved.TelegramUserID)
	require.EqualValues(t, -987, saved.TelegramChatID, "group chat ID is not the sender identity")
	require.NoError(t, auth.Delete(owner, entry.ID))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, IsFollowup: true}
	require.Error(t, inputs.ClaimQueuedForTaskExecution(t.Context(), input.ID, execution))
	saved, err = inputs.GetByID(t.Context(), input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, saved.InputStatus)
	require.NoError(t, auth.Create(owner, entry))
	require.NoError(t, inputs.ClaimQueuedForTaskExecution(t.Context(), input.ID, execution))
	user, found, err := NewRunIdentityRepo(db).ExecutionUser(t.Context(), execution.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, models.BootstrapUserID, user)
}

func TestTelegramInputCannotForgeSenderField(t *testing.T) {
	db := testutil.NewTestDB(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	task := createThreadInputTask(t, owner, db, "default")
	agent := createThreadInputLLMConfig(t, owner, db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: agent.ID, Content: "Continue", TelegramUserID: 123}
	repo := NewThreadInputRepo(db)
	require.NoError(t, repo.CreateQueued(owner, input))
	saved, err := repo.GetByID(t.Context(), input.ID)
	require.NoError(t, err)
	require.Zero(t, saved.TelegramUserID, "only trusted ingress context may persist sender identity")
}

func TestTelegramQueuedChatPromotesTaskProvenance(t *testing.T) {
	db := testutil.NewTestDB(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	auth := NewTelegramAuthRepo(db)
	entry := &models.TelegramAuthorizedUser{TelegramUserID: 123, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: models.BootstrapUserID}}
	require.NoError(t, auth.Create(owner, entry))
	admitted := models.WithTelegramIngressIdentity(owner, models.TelegramIngressIdentity{ExternalUserID: 123, UserID: models.BootstrapUserID})
	agent := createThreadInputLLMConfig(t, owner, db)
	activeTask := &models.Task{ProjectID: "default", Title: "Active chat", Category: models.CategoryChat, Status: models.StatusRunning}
	require.NoError(t, NewTaskRepo(db, nil).Create(owner, activeTask))
	active := &models.Execution{TaskID: activeTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, NewExecutionRepo(db).Create(owner, active))
	repo := NewThreadInputRepo(db)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: "default", RunExecutionID: active.ID, AgentConfigID: agent.ID, Content: "Next", Source: models.TaskOriginTelegram}
	require.NoError(t, repo.CreateQueued(admitted, input))
	task := &models.Task{ProjectID: "default", Title: "Queued", Category: models.CategoryChat, Status: models.StatusRunning, CreatedVia: models.TaskOriginTelegram}
	exec := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.NoError(t, repo.ClaimQueuedForChatExecution(t.Context(), input.ID, task, exec, nil, nil, nil))
	require.NoError(t, NewRunIdentityRepo(db).RequireChannelTaskProvenance(owner, task.ID))
	require.NoError(t, auth.Delete(owner, entry.ID))
	require.Error(t, NewRunIdentityRepo(db).RequireChannelTaskProvenance(owner, task.ID))
}
