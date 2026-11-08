package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type discordIdentityAuthProbe struct {
	persistence.DiscordAuthStore
	lookedUp *bool
}

func (p discordIdentityAuthProbe) IsAuthorizedAnywhere(context.Context, string) (bool, error) {
	*p.lookedUp = true
	return true, nil
}

func discordTestMember(t *testing.T, env *teststores.Env, name string) string {
	t.Helper()
	org := NewOrganizationService(env.Organizations, "secret")
	token, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(t.Context(), token, name, "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, "default", user.UserID, "editor"))
	return user.UserID
}

func TestDiscordAuthenticatedIngressRequiresVerifiedSender(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	lookedUp := false
	const sender = "123456789"
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, Role: "owner"})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: sender}
	require.NoError(t, env.DiscordAuth.Create(owner, entry))
	svc := &DiscordService{workerSvc: worker, projectRepo: env.Projects, discordUserProjectRepo: env.DiscordUserProjects, userProjects: map[string]string{}, discordAuthRepo: env.DiscordAuth}

	// Allowlisted but unbound: nothing resolves, discloses or persists.
	require.False(t, svc.checkAuthorization(owner, "default", sender))
	require.Empty(t, svc.getActiveProject(owner, sender), "fallback must not select an unbound project")
	_, err := svc.senderPrincipalContext(owner, sender)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, svc.setActiveProject(owner, sender, "default"), authz.ErrDenied)
	saved, err := env.DiscordUserProjects.GetUserProject(ctx, sender)
	require.NoError(t, err)
	require.Empty(t, saved)
	handlers := svc.discordActionHandlers("default", discordActionContext{UserID: sender}, nil)
	require.NotEmpty(t, handlers)
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handler(owner, json.RawMessage(`{}`))
			require.ErrorIs(t, err, authz.ErrDenied, "owner session cannot stand in for an unbound sender")
		})
	}
	probe := &DiscordService{workerSvc: worker, projectRepo: env.Projects, userProjects: map[string]string{}, discordAuthRepo: discordIdentityAuthProbe{DiscordAuthStore: env.DiscordAuth, lookedUp: &lookedUp}}
	require.False(t, probe.checkAuthorization(owner, "default", sender))
	require.False(t, lookedUp, "external allowlist must not substitute for application identity")

	member := discordTestMember(t, env, "discord-sender")
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, member))
	resolved, err := svc.senderPrincipalContext(ctx, sender)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(resolved)
	require.True(t, ok)
	require.Equal(t, member, principal.UserID)
	identity, ok := models.DiscordIngressIdentityFromContext(resolved)
	require.True(t, ok)
	require.Equal(t, models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: sender, UserID: member}, identity)
	require.Equal(t, "default", svc.getActiveProject(ctx, sender))
	require.True(t, svc.checkAuthorization(resolved, "default", sender))
	require.False(t, svc.checkAuthorization(resolved, "foreign", sender), "binding is project-scoped")
	require.False(t, svc.checkAuthorization(owner, "default", sender), "owner session is not the bound sender")
	_, err = handlers["list_tasks"](resolved, json.RawMessage(`{}`))
	require.NotErrorIs(t, err, authz.ErrDenied)

	// A rebinding cannot replace the principal admitted for this event.
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, models.BootstrapUserID))
	_, err = svc.authorizeSenderAction(resolved, "default", sender)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, member))
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, policy.ProjectID, member, "viewer"))
	_, err = svc.authorizeSenderAction(resolved, "default", sender)
	require.Error(t, err, "viewer demotion revokes channel authority")
	_, err = handlers["list_tasks"](resolved, json.RawMessage(`{}`))
	require.Error(t, err, "runtime checks policy on invocation, not only construction")

	worker.runAuthOff = true
	require.True(t, probe.checkAuthorization(owner, "default", sender))
	require.True(t, lookedUp)
	legacy, err := svc.senderPrincipalContext(ctx, sender)
	require.NoError(t, err)
	principal, _ = models.PrincipalFromContext(legacy)
	require.Equal(t, models.BootstrapUserID, principal.UserID)
}

func TestDiscordUnmappedDirectIngressDoesNotPersistOrRun(t *testing.T) {
	svc, env, settings, projects, tasks, allowlist, contexts := newDiscordServiceForTest(t)
	ctx := t.Context()
	project := &models.Project{Name: "Discord identity boundary", IsDefault: true}
	require.NoError(t, projects.Create(ctx, project))
	require.NoError(t, allowlist.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "sender"}))
	model := &models.LLMConfig{Name: "Discord model", Provider: models.ProviderOpenAI, Model: "gpt-4o", IsDefault: true}
	require.NoError(t, env.LLMConfigs.Create(ctx, model))
	require.NoError(t, settings.Set(ctx, DiscordSettingSendResponses, "true"))
	svc.llmConfigRepo = env.LLMConfigs
	svc.execRepo = env.Executions
	svc.scheduleRepo = env.Schedules
	svc.taskSvc = NewTaskService(tasks, env.Attachments, nil)
	svc.llmSvc = NewLLMService(env.LLMConfigs, env.Executions, tasks, projects, env.Schedules, env.Attachments)
	worker := NewWorkerService(nil, 1, projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc.workerSvc = worker
	replies, runs := 0, 0
	svc.sendMessageFunc = func(string, string, string) (string, error) { replies++; return "ack", nil }
	var request ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, r ChannelChatRunRequest) { runs++; request = r })
	incoming := discordIncomingMessage{ChannelID: "channel", MessageID: "message", UserID: "sender", Text: "hello", Source: "discord"}
	svc.processIncomingMessage(incoming)
	require.Zero(t, runs)
	require.Zero(t, replies)
	// Auth-off exercises the same configured dependencies and external allowlist.
	worker.runAuthOff = true
	svc.processIncomingMessage(incoming)
	require.Equal(t, 1, runs)
	require.NotEmpty(t, request.TaskID)
	stored, err := contexts.GetByTaskID(ctx, request.TaskID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	worker.runAuthOff = false
	svc.processIncomingMessage(incoming)
	require.Equal(t, 1, runs, "cached context must not bypass authenticated ingress")
}

func TestDiscordBoundSenderIngressPersistsProvenanceAndRevokes(t *testing.T) {
	svc, env, settings, _, tasks, allowlist, _ := newDiscordServiceForTest(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	const sender = "223456789"
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: sender}
	require.NoError(t, allowlist.Create(owner, entry))
	member := discordTestMember(t, env, "discord-ingress")
	require.NoError(t, allowlist.BindApplicationUser(owner, "default", entry.ID, member))
	model := &models.LLMConfig{Name: "Discord model", Provider: models.ProviderOpenAI, Model: "gpt-4o", IsDefault: true}
	require.NoError(t, env.LLMConfigs.Create(ctx, model))
	require.NoError(t, settings.Set(ctx, DiscordSettingSendResponses, "true"))
	svc.llmConfigRepo = env.LLMConfigs
	svc.execRepo = env.Executions
	svc.scheduleRepo = env.Schedules
	svc.discordUserProjectRepo = env.DiscordUserProjects
	svc.taskSvc = NewTaskService(tasks, env.Attachments, nil)
	svc.llmSvc = NewLLMService(env.LLMConfigs, env.Executions, tasks, env.Projects, env.Schedules, env.Attachments)
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc.workerSvc = worker
	svc.sendMessageFunc = func(string, string, string) (string, error) { return "ack", nil }
	var request ChannelChatRunRequest
	var runCtx context.Context
	runs := 0
	svc.SetChannelChatRunner(func(c context.Context, r ChannelChatRunRequest) { runs++; request = r; runCtx = c })
	incoming := discordIncomingMessage{ChannelID: "channel", MessageID: "message", UserID: sender, Text: "hello", Source: "discord"}
	svc.processIncomingMessage(incoming)
	require.Equal(t, 1, runs)
	principal, ok := models.PrincipalFromContext(runCtx)
	require.True(t, ok)
	require.Equal(t, member, principal.UserID, "run is handed off as the bound application user")
	task, err := tasks.GetByID(ctx, request.TaskID)
	require.NoError(t, err)
	runAs, err := env.RunIdentities.TaskUser(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, member, runAs)
	bound := models.WithPrincipal(ctx, models.Principal{UserID: member})
	require.NoError(t, env.RunIdentities.RequireChannelTaskProvenance(bound, task.ID))
	require.ErrorIs(t, env.RunIdentities.RequireChannelTaskProvenance(owner, task.ID), authz.ErrDenied, "owner cannot borrow a sender's task")
	require.NoError(t, worker.checkRunPermission(bound, *task))
	executionUser, found, err := env.RunIdentities.ExecutionUser(ctx, request.ExecID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, member, executionUser)
	require.NoError(t, env.RunIdentities.CheckExecutionGoal(bound, request.ExecID, task.ID))

	// A Discord-origin task without durable provenance is denied when authenticated.
	legacy := models.Task{ProjectID: "default", Title: "Legacy Discord task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginDiscord}
	require.NoError(t, tasks.Create(bound, &legacy))
	require.ErrorIs(t, worker.checkRunPermission(bound, legacy), authz.ErrDenied)

	require.NoError(t, allowlist.DeleteForProject(owner, "default", entry.ID))
	require.Error(t, worker.checkRunPermission(bound, *task), "binding removal revokes queued/running work")
	require.Error(t, env.RunIdentities.CheckExecutionGoal(bound, request.ExecID, task.ID))
	svc.mu.Lock()
	svc.userProjects = map[string]string{}
	svc.mu.Unlock()
	svc.processIncomingMessage(incoming)
	require.Equal(t, 1, runs, "removed binding blocks new ingress")
	worker.runAuthOff = true
	require.NoError(t, worker.checkRunPermission(owner, legacy), "auth-off retains legacy Discord recovery")
}
