package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
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

func TestDiscordAuthenticatedIngressRequiresVerifiedSender(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := NewWorkerService(nil, 1, env.Projects)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	lookedUp := false
	svc := &DiscordService{workerSvc: worker, projectRepo: env.Projects, discordUserProjectRepo: env.DiscordUserProjects, userProjects: map[string]string{"sender": "default"}, discordAuthRepo: discordIdentityAuthProbe{lookedUp: &lookedUp}}
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, Role: "owner"})
	require.False(t, svc.checkAuthorization(owner, "default", "sender"))
	require.False(t, svc.checkAuthorization(owner, "foreign", "sender"))
	require.False(t, lookedUp, "external allowlist must not substitute for application identity")
	require.Empty(t, svc.getActiveProject(owner, "sender"), "cached selections must not disclose a project")
	require.Empty(t, svc.getActiveProject(owner, "new-sender"), "fallback project enumeration must not run")
	require.ErrorIs(t, svc.setActiveProject(owner, "sender", "foreign"), authz.ErrDenied)
	require.Equal(t, "default", svc.userProjects["sender"])
	saved, err := env.DiscordUserProjects.GetUserProject(ctx, "sender")
	require.NoError(t, err)
	require.Empty(t, saved)
	handedOff := false
	svc.processIncomingMessageFn = func(discordIncomingMessage) { handedOff = true }
	svc.handleMessageCreate(owner, nil, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "message", ChannelID: "channel", Content: "run a task", Author: &discordgo.User{ID: "sender"}}})
	require.False(t, handedOff, "gateway must reject before its handoff hook")
	// Direct ingress also stops before project/dependency/attachment processing.
	svc.processIncomingMessage(discordIncomingMessage{ChannelID: "channel", UserID: "sender", Text: "run a task"})
	require.False(t, lookedUp)
	handlers := svc.discordActionHandlers("default", discordActionContext{UserID: "sender"}, nil)
	require.NotEmpty(t, handlers)
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handler(owner, json.RawMessage(`{}`))
			require.ErrorIs(t, err, authz.ErrDenied)
		})
	}
	worker.runAuthOff = true
	require.True(t, svc.checkAuthorization(owner, "default", "sender"))
	require.True(t, lookedUp)
	require.Equal(t, "default", svc.getActiveProject(owner, "sender"))
	require.NoError(t, svc.setActiveProject(owner, "sender", "default"))
	worker.runAuthOff = false
	_, err = handlers["create_task"](owner, json.RawMessage(`{}`))
	require.ErrorIs(t, err, authz.ErrDenied, "runtime checks policy on invocation, not only construction")
	svc.discordAuthRepo = nil
	require.False(t, svc.checkAuthorization(owner, "default", "sender"))
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
