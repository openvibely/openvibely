package handler

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceChatIsPersonal(t *testing.T) {
	tc, app := organizationTestApp(t, true)
	ctx := t.Context()
	h := tc.handler
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	agent := createAgent(t, h.llmConfigRepo)
	create := func(c context.Context, text string) *models.Execution {
		task := &models.Task{ProjectID: models.AllProjectsID, Title: text, Category: models.CategoryChat, Status: models.StatusCompleted, AgentID: &agent.ID}
		require.NoError(t, h.taskRepo.Create(c, task))
		ex := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: text}
		require.NoError(t, h.execRepo.Create(c, ex))
		return ex
	}
	legacy := create(ctx, "LEGACY SHARED SECRET")
	admin := create(owner, "ADMIN PERSONAL SECRET")
	var aliceCtx, bobCtx context.Context
	var aliceCookie *http.Cookie
	var alice, bob *models.Execution
	for _, name := range []string{"alice-personal", "bob-personal"} {
		token, err := h.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
		require.NoError(t, err)
		user, err := h.organizationSvc.Accept(ctx, token, name, "long-member-password", "")
		require.NoError(t, err)
		c := models.WithPrincipal(ctx, user.Principal)
		projects, err := guarded.ListSelectorOptions(c)
		require.NoError(t, err)
		require.Empty(t, projects)
		ex := create(c, name+" PRIVATE CHAT")
		if alice == nil {
			alice = ex
			aliceCtx = c
			login := orgRequest(app, http.MethodPost, "/login", url.Values{"username": {name}, "password": {"long-member-password"}}, nil)
			aliceCookie = orgCookie(t, login)
		} else {
			bob = ex
			bobCtx = c
		}
	}

	// Provider history and active-turn detection are isolated too.
	modelHistory, err := h.execRepo.ListChatModelHistoryBefore(aliceCtx, models.AllProjectsID, "", "", "", 10)
	require.NoError(t, err)
	require.Len(t, modelHistory, 1)
	require.Equal(t, alice.ID, modelHistory[0].ID)
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: models.AllProjectsID, AgentConfigID: agent.ID, Content: "BOB QUEUED SECRET"}
	require.NoError(t, h.threadInputRepo.CreateQueued(bobCtx, queued))
	h.chatBroadcaster = events.NewChatBroadcaster()
	sub, err := h.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer h.chatBroadcaster.Unsubscribe(sub)
	h.publishChatEvent(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: models.AllProjectsID, ExecID: queued.ID, Queued: true})
	select {
	case event := <-sub:
		require.True(t, h.liveEventVisible(bobCtx, nil, liveFrame{Data: event}))
		require.False(t, h.liveEventVisible(aliceCtx, nil, liveFrame{Data: event}))
	case <-time.After(time.Second):
		t.Fatal("queued lifecycle event was not published to its owner")
	}

	pending, err := h.threadInputRepo.ListPendingForChat(aliceCtx, models.AllProjectsID)
	require.NoError(t, err)
	require.Empty(t, pending)
	// Claiming with valid rows must still reject another user's pending input.
	task := &models.Task{ProjectID: models.AllProjectsID, Title: "promoted Bob", Category: models.CategoryChat, Status: models.StatusRunning}
	run := &models.Execution{AgentConfigID: agent.ID, Status: models.ExecRunning}
	require.Error(t, h.threadInputRepo.ClaimQueuedForChatExecution(aliceCtx, queued.ID, task, run, nil, nil, nil))
	require.NoError(t, h.threadInputRepo.ClaimQueuedForChatExecution(bobCtx, queued.ID, task, run, nil, nil, nil))
	active, err := h.execRepo.FindLatestActiveChatExecution(aliceCtx, models.AllProjectsID)
	require.NoError(t, err)
	require.Nil(t, active)
	active, err = h.execRepo.FindLatestActiveChatExecution(bobCtx, models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, run.ID, active.ID)
	remaining := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: models.AllProjectsID, AgentConfigID: agent.ID, RunExecutionID: run.ID, Content: "Bob still queued"}
	require.NoError(t, h.threadInputRepo.CreateQueued(bobCtx, remaining))
	foreign := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: models.AllProjectsID, AgentConfigID: agent.ID, RunExecutionID: run.ID, Content: "Alice cannot join Bob's turn"}
	require.Error(t, h.threadInputRepo.CreateQueued(aliceCtx, foreign))

	require.False(t, h.liveSubscriptionAllowed(aliceCtx, liveCommand{Op: "execution", ProjectID: models.AllProjectsID, ExecID: run.ID}))
	require.NotEqual(t, models.WorkspaceChatScope(aliceCtx, models.AllProjectsID), models.WorkspaceChatScope(bobCtx, models.AllProjectsID))
	response := orgRequest(app, http.MethodGet, "/chat?project_id="+models.AllProjectsID, nil, aliceCookie)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), alice.PromptSent)
	for _, ex := range []*models.Execution{legacy, admin, bob} {
		require.NotContains(t, response.Body.String(), ex.PromptSent)
		require.False(t, h.liveSubscriptionAllowed(aliceCtx, liveCommand{Op: "execution", ProjectID: models.AllProjectsID, ExecID: ex.ID}))
		require.ErrorIs(t, h.startInstallationContinuation(aliceCtx, persistence.ChatInputRequestRecord{ProjectID: models.AllProjectsID, ExecutionID: ex.ID}), persistence.ErrProjectDenied)

		r := orgRequest(app, http.MethodGet, "/chat/output/"+ex.ID+"?project_id="+models.AllProjectsID, nil, aliceCookie)
		require.Equal(t, 404, r.Code)
		r = orgRequest(app, http.MethodGet, "/chat/output/"+ex.ID+"?before=0&project_id="+models.AllProjectsID, nil, aliceCookie)
		require.Equal(t, 404, r.Code)
		r = orgRequest(app, http.MethodDelete, "/tasks/"+ex.TaskID+"?project_id="+models.AllProjectsID, nil, aliceCookie)
		require.Equal(t, 404, r.Code)
	}
	require.True(t, h.liveSubscriptionAllowed(aliceCtx, liveCommand{Op: "execution", ProjectID: models.AllProjectsID, ExecID: alice.ID}))
	ownOutput := orgRequest(app, http.MethodGet, "/chat/output/"+alice.ID+"?before=0&project_id="+models.AllProjectsID, nil, aliceCookie)
	require.Equal(t, 200, ownOutput.Code, ownOutput.Body.String())

	history, err := h.execRepo.ListChatHistory(owner, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	p, _ := models.PrincipalFromContext(aliceCtx)
	require.True(t, h.liveEventVisible(aliceCtx, nil, liveFrame{Data: events.ChatEvent{ProjectID: models.AllProjectsID, AudienceUserID: p.UserID}}))
	require.False(t, h.liveEventVisible(aliceCtx, nil, liveFrame{Data: events.ChatEvent{ProjectID: models.AllProjectsID, AudienceUserID: models.BootstrapUserID}}))
	require.False(t, h.liveEventVisible(aliceCtx, nil, liveFrame{Data: events.ChatEvent{ProjectID: models.AllProjectsID}}))
	response = orgRequest(app, http.MethodDelete, "/chat/history?project_id="+models.AllProjectsID, nil, aliceCookie)
	require.Equal(t, 200, response.Code, response.Body.String())
	history, err = h.execRepo.ListChatHistory(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, history, 4)
	history, err = h.execRepo.ListChatHistory(aliceCtx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Empty(t, history)
	pending, err = h.threadInputRepo.ListPendingForChat(bobCtx, models.AllProjectsID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "clearing Alice must not cancel Bob's pending message")
	require.Equal(t, remaining.ID, pending[0].ID)

}
