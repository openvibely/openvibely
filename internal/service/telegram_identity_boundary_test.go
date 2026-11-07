package service

import (
	"context"
	"encoding/json"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestTelegramAuthenticatedIngressRequiresVerifiedSender(t *testing.T) {
	env := teststores.New(t)
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	lookedUp := false
	s := &TelegramService{workerSvc: worker, projectRepo: env.Projects, telegramUserProjectRepo: env.TelegramUserProjects, userProjects: map[int64]string{71: "default"}, telegramAuthRepo: telegramAuthorizationStoreStub{
		isAuthorizedAnywhere: func(context.Context, int64, string) (bool, error) { lookedUp = true; return true, nil },
	}}
	require.False(t, s.checkAuthorization(71, "trusted-owner", "default"))
	require.False(t, s.checkAuthorization(71, "trusted-owner", "foreign-project"))
	require.False(t, lookedUp, "transport allowlist and username must not establish application identity")
	require.Equal(t, telegramIdentityUnavailable, s.handleStart(71))
	require.Equal(t, telegramIdentityUnavailable, s.handleProject(71, ""))
	require.Equal(t, telegramIdentityUnavailable, s.handleProject(71, "foreign-project"))
	result, handled := s.handleNaturalLanguageProjectCommand(71, "list projects")
	require.True(t, handled)
	require.Equal(t, telegramIdentityUnavailable, result)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID, Role: "owner"})
	require.ErrorIs(t, s.setTelegramActiveProject(owner, 71, "foreign-project"), authz.ErrDenied)
	require.Equal(t, "default", s.userProjects[71])
	handedOff := false
	require.True(t, s.handleChatMessageWithDurableHandoff(owner, nil, func() { handedOff = true }), "rejected input is consumed without invoking persistence or model work")
	require.False(t, handedOff)
	handlers := s.telegramActionHandlers("default", 1, 71, nil)
	require.NotEmpty(t, handlers)
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handler(owner, json.RawMessage(`{}`))
			require.ErrorIs(t, err, authz.ErrDenied, "borrowed owner must not authorize Telegram tools")
		})
	}
	// The guard is checked when each action executes, not just at runtime creation.
	worker.runAuthOff = true
	require.True(t, s.checkAuthorization(71, "trusted-owner", "default"))
	require.True(t, lookedUp, "auth-off retains the existing external allowlist")
	worker.runAuthOff = false
	_, err := handlers["create_task"](owner, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied)
	s.telegramAuthRepo = nil
	require.False(t, s.checkAuthorization(71, "", "default"), "missing allowlist cannot bypass authenticated identity checks")
}

func TestTelegramMappedSenderUsesCurrentProjectPermissions(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	member := discordTestMember(t, env, "telegram-alice")
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	row := &models.TelegramAuthorizedUser{TelegramUserID: 123, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: member}}
	require.NoError(t, env.TelegramAuth.Create(owner, row))
	s := &TelegramService{workerSvc: worker, telegramAuthRepo: env.TelegramAuth, projectRepo: authz.NewProjectStore(env.Projects, &authz.Authorizer{Store: env.ProjectAccess}), userProjects: map[int64]string{123: "default"}}
	sender, err := s.senderProjectContext(owner, 123, "default", authz.Edit)
	require.NoError(t, err)
	p, ok := models.PrincipalFromContext(sender)
	require.True(t, ok)
	require.Equal(t, member, p.UserID)
	require.True(t, s.checkAuthorization(123, "any-username", "default"))
	require.False(t, s.checkAuthorization(456, "telegram-alice", "default"), "username cannot impersonate numeric sender")
	task := &models.Task{ProjectID: "default", Title: "Mapped Telegram task", Category: models.CategoryActive, Status: models.StatusPending, CreatedVia: models.TaskOriginTelegram}
	require.NoError(t, env.Tasks.Create(sender, task))
	require.NoError(t, worker.checkRunPermission(sender, *task))
	forged := &models.Task{ProjectID: "default", Title: "Forged Telegram origin", Category: models.CategoryActive, Status: models.StatusPending, CreatedVia: models.TaskOriginTelegram}
	require.NoError(t, env.Tasks.Create(owner, forged))
	require.Error(t, worker.checkRunPermission(owner, *forged), "origin alone is not sender provenance")
	handlers := s.telegramActionHandlers("default", 123, 123, nil)
	_, err = handlers["update_project_settings"](sender, json.RawMessage(`{}`))
	require.Error(t, err, "editor cannot perform maintainer actions")
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = s.senderProjectContext(sender, 123, "default", authz.Edit)
	require.Error(t, err)
	require.Error(t, worker.checkRunPermission(sender, *task), "next start checks revocation")
	binder := env.TelegramAuth.(interface {
		BindSender(context.Context, string, string, string) error
	})
	require.NoError(t, binder.BindSender(owner, row.ID, models.BootstrapUserID, ""))
	_, err = s.senderPrincipalContext(sender, 123)
	require.Error(t, err, "rebinding cannot change the captured user")
	require.NoError(t, binder.BindSender(owner, row.ID, member, ""))
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, member, "member", true))
	require.False(t, s.checkAuthorization(123, "", "default"))
}

func TestTelegramMappedIngressPersistsSenderIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	member := discordTestMember(t, env, "telegram-runner")
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.TelegramAuth.Create(owner, &models.TelegramAuthorizedUser{TelegramUserID: 71, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: member}}))
	worker := NewWorkerService(nil, 0, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := &TelegramService{workerSvc: worker, telegramAuthRepo: env.TelegramAuth, projectRepo: env.Projects, llmConfigRepo: env.LLMConfigs, taskSvc: NewTaskService(env.Tasks, env.Attachments, worker), taskRepo: env.Tasks, execRepo: env.Executions, userProjects: map[int64]string{71: "default"}, sendMessageFunc: func(int64, string) {}}
	called := false
	svc.SetChannelChatRunner(func(runCtx context.Context, req ChannelChatRunRequest) {
		called = true
		require.EqualValues(t, 71, req.ReplyContext.TelegramUserID)
		require.Equal(t, models.TaskOriginTelegram, req.ReplyContext.Source)
		p, ok := models.PrincipalFromContext(runCtx)
		require.True(t, ok)
		require.Equal(t, member, p.UserID)
		taskUser, err := env.RunIdentities.TaskUser(ctx, req.TaskID)
		require.NoError(t, err)
		require.Equal(t, member, taskUser)
		executionUser, found, err := env.RunIdentities.ExecutionUser(ctx, req.ExecID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, member, executionUser)
		require.NoError(t, env.RunIdentities.RequireChannelTaskProvenance(runCtx, req.TaskID))
	})
	require.True(t, svc.handleChatMessageWithDurableHandoff(owner, &tgbotapi.Message{From: &tgbotapi.User{ID: 71}, Chat: &tgbotapi.Chat{ID: 71}, Text: "hello"}, nil))
	require.True(t, called)
}
