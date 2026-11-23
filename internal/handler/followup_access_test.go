package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestFollowupRevocationDeniesNextStartOnly(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "followup-user", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	task := createTask(t, h, "default", "Followup access", func(task *models.Task) { task.Category = models.CategoryBacklog })
	params := streamingResponseParams{TaskID: task.ID, ProjectID: "default", IsTaskFollowup: true}
	restored, err := h.followupUserContext(ctx, user.UserID)
	require.NoError(t, err)
	require.NoError(t, h.requireFollowupAccess(restored, params))
	parent, cancel := context.WithCancel(restored)
	defer cancel()
	waiting, waitCancel := context.WithCancel(parent)
	defer waitCancel()
	running, runCancel := context.WithCancel(parent)
	defer runCancel()
	policy, err := tc.stores.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, policy.ProjectID, user.UserID, "viewer"))
	for _, child := range []context.Context{waiting, running} {
		require.NoError(t, child.Err(), "revocation does not stop a run already started")
	}
	require.ErrorIs(t, h.requireFollowupAccess(restored, params), authz.ErrDenied)
	input := models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, Content: "Queued before revocation", Source: models.TaskOriginWeb}
	require.NoError(t, tc.stores.ThreadInputs.CreateQueued(restored, &input))
	require.ErrorIs(t, h.startQueuedTaskThreadInput(ctx, input), authz.ErrDenied)
	stored, err := tc.stores.ThreadInputs.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputCancelled, stored.InputStatus)

	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	task.AutoMergeOnGoalAchieved = true
	require.NoError(t, tc.stores.Tasks.Update(ctx, task))
	require.ErrorIs(t, h.requireFollowupAccess(restored, params), authz.ErrDenied)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "maintainer"))
	require.NoError(t, h.requireFollowupAccess(restored, params))
	params.ProjectID = "foreign"
	require.ErrorIs(t, h.requireFollowupAccess(restored, params), authz.ErrDenied)
	_, err = h.followupUserContext(ctx, "")
	require.ErrorIs(t, err, authz.ErrDenied)
	h.SetAuthMode(auth.AuthModeDisabled)
	owner, err := h.followupUserContext(ctx, "")
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(owner)
	require.True(t, ok)
	require.Equal(t, models.BootstrapUserID, principal.UserID)
}

func TestStandaloneFollowupHistoricalReviewerPolicy(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetRunIdentityStore(tc.stores.RunIdentities)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	h.SetAuthMode(auth.AuthModeLocal)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := createTask(t, h, "default", "Historical followup", func(task *models.Task) { task.Category = models.CategoryBacklog; task.Status = models.StatusCompleted })
	reviewer := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer"}
	require.NoError(t, tc.stores.GitHubAuth.UpsertAuthorizedActor(owner, reviewer))
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42"}
	require.NoError(t, tc.stores.TaskPullRequests.Upsert(owner, pr))
	feedback := &models.GitHubPRFeedbackForwarded{TaskPullRequestID: pr.ID, TaskID: task.ID, RepoFullName: "example/repo", PRNumber: 42, FeedbackKind: "review", GitHubID: "historical", AuthorLogin: "reviewer", Body: "revise", CreatedAt: time.Now()}
	model := models.LLMConfig{Name: "Historical followup model", Provider: "test", Model: "test"}
	require.NoError(t, tc.stores.LLMConfigs.Create(ctx, &model))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: task.ID, AgentConfigID: model.ID, Content: "revise"}
	recorded, err := tc.stores.GitHubPRFeedback.RecordForwardedAndQueue(owner, feedback, input)
	require.NoError(t, err)
	require.True(t, recorded)
	strict, err := h.followupUserContext(owner, models.BootstrapUserID)
	require.NoError(t, err)
	require.True(t, models.RequiredReviewerIdentity(strict))
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: model.ID, IsFollowup: true}
	require.ErrorIs(t, tc.stores.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(strict, input.ID, execution, nil, &model), authz.ErrDenied)
	h.SetAuthMode(auth.AuthModeDisabled)
	compatible, err := h.followupUserContext(ctx, "")
	require.NoError(t, err)
	require.False(t, models.RequiredReviewerIdentity(compatible))
	require.NoError(t, tc.stores.ThreadInputs.ClaimQueuedForTaskExecutionWithModel(compatible, input.ID, execution, nil, &model))
	params := streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID, ExecID: execution.ID, IsTaskFollowup: true}
	require.NoError(t, h.requireFollowupAccess(compatible, params))
	h.SetAuthMode(auth.AuthModeLocal)
	// A fresh owner context must not bypass checks, nor must a later mapping be
	// mistaken for the identity captured when this historical instruction arrived.
	require.NoError(t, tc.stores.GitHubAuth.BindReviewerIdentity(owner, models.GitHubReviewerIdentity{ProjectID: "default", ActorID: reviewer.ID, Host: "github.com", ExternalUserID: 123, ApplicationUserID: models.BootstrapUserID}))
	require.ErrorIs(t, h.requireFollowupAccess(owner, params), authz.ErrDenied)
	require.ErrorIs(t, tc.stores.ThreadInputs.CheckSteeringDelivery(strict, execution.ID, nil), authz.ErrDenied)
	running, cancel := context.WithCancel(owner)
	defer cancel()
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
}

func TestStandaloneFollowupValidatesLegacyExecutionScope(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetRunIdentityStore(tc.stores.RunIdentities)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	task := createTask(t, h, "default", "Legacy followup scope")
	other := createTask(t, h, "default", "Other legacy followup scope")
	execution := &models.Execution{TaskID: other.ID, Status: models.ExecRunning}
	require.NoError(t, tc.stores.Executions.Create(ctx, execution))
	h.SetAuthMode(auth.AuthModeDisabled)
	owner, err := h.followupUserContext(ctx, "")
	require.NoError(t, err)
	params := streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID, ExecID: execution.ID, IsTaskFollowup: true}
	require.ErrorIs(t, h.requireFollowupAccess(owner, params), authz.ErrDenied)
	params.TaskID = other.ID
	require.NoError(t, h.requireFollowupAccess(owner, params))
	h.SetAuthMode(auth.AuthModeLocal)
	require.ErrorIs(t, h.requireFollowupAccess(owner, params), authz.ErrDenied, "authenticated legacy runs need a saved executor")
}

func TestFollowupUnmappedChannelRecoveryDenied(t *testing.T) {
	for _, origin := range []string{models.TaskOriginTelegram, models.TaskOriginDiscord, models.TaskOriginEmail} {
		t.Run(origin, func(t *testing.T) {
			tc := NewStoreTestContext(t)
			h := tc.handler
			h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
			h.SetAuthMode(auth.AuthModeLocal)
			task := createTask(t, h, "default", origin+" followup", func(task *models.Task) { task.CreatedVia = origin; task.Category = models.CategoryBacklog })
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
			params := streamingResponseParams{TaskID: task.ID, ProjectID: task.ProjectID}
			require.ErrorIs(t, h.requireFollowupAccess(ctx, params), authz.ErrDenied)
			h.SetAuthMode(auth.AuthModeDisabled)
			require.NoError(t, h.requireFollowupAccess(ctx, params))
			h.SetAuthMode(auth.AuthModeLocal)
			running, cancel := context.WithCancel(ctx)
			defer cancel()
			require.NoError(t, running.Err(), "revocation does not stop a run already started")
			browser := createTask(t, h, "default", origin+" browser task")
			params.TaskID = browser.ID
			params.ChannelReply.Source = origin
			require.ErrorIs(t, h.requireFollowupAccess(ctx, params), authz.ErrDenied, "channel reply cannot borrow browser task authority")
		})
	}
}

func TestFollowupDiscordRequiresCurrentSenderBinding(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetDiscordAuthRepo(tc.stores.DiscordAuth)
	h.SetRunIdentityStore(tc.stores.RunIdentities)
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "discord-followup", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.DiscordAuthorizedUser{ProjectID: "default", DiscordUserID: "323456789"}
	require.NoError(t, tc.stores.DiscordAuth.Create(owner, entry))
	require.NoError(t, tc.stores.DiscordAuth.BindApplicationUser(owner, "default", entry.ID, user.UserID))
	admitted := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: user.UserID}), models.DiscordIngressIdentity{ProjectID: "default", ExternalUserID: entry.DiscordUserID, UserID: user.UserID})
	task := &models.Task{ProjectID: "default", Title: "Discord bound followup", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginDiscord}
	require.NoError(t, tc.stores.Tasks.Create(admitted, task))
	params := streamingResponseParams{TaskID: task.ID, ProjectID: "default", IsTaskFollowup: true, ChannelReply: service.ChannelReplyContext{Source: models.TaskOriginDiscord, DiscordUserID: entry.DiscordUserID}}
	restored, err := h.followupUserContext(ctx, user.UserID)
	require.NoError(t, err)
	require.NoError(t, h.requireFollowupAccess(restored, params))
	ownerCtx, err := h.followupUserContext(ctx, models.BootstrapUserID)
	require.NoError(t, err)
	require.ErrorIs(t, h.requireFollowupAccess(ownerCtx, params), authz.ErrDenied, "owner cannot replace the bound sender")
	foreign := params
	foreign.ChannelReply.DiscordUserID = "999999999"
	require.Error(t, h.requireFollowupAccess(restored, foreign))
	running, cancel := context.WithCancel(restored)
	defer cancel()
	require.NoError(t, tc.stores.DiscordAuth.DeleteForProject(owner, "default", entry.ID))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	require.Error(t, h.requireFollowupAccess(restored, params))
}

func TestFollowupTelegramRequiresCurrentSenderBinding(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetTelegramAuthRepo(tc.stores.TelegramAuth)
	h.SetRunIdentityStore(tc.stores.RunIdentities)
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "telegram-followup", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, "editor"))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.TelegramAuthorizedUser{TelegramUserID: 323456789, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: user.UserID}}
	require.NoError(t, tc.stores.TelegramAuth.Create(owner, entry))
	admitted := models.WithTelegramIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: user.UserID}), models.TelegramIngressIdentity{ExternalUserID: entry.TelegramUserID, UserID: user.UserID})
	task := &models.Task{ProjectID: "default", Title: "Telegram bound followup", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, CreatedVia: models.TaskOriginTelegram}
	require.NoError(t, tc.stores.Tasks.Create(admitted, task))
	params := streamingResponseParams{TaskID: task.ID, ProjectID: "default", IsTaskFollowup: true, ChannelReply: service.ChannelReplyContext{Source: models.TaskOriginTelegram, TelegramUserID: entry.TelegramUserID}}
	restored, err := h.followupUserContext(ctx, user.UserID)
	require.NoError(t, err)
	require.NoError(t, h.requireFollowupAccess(restored, params))
	ownerCtx, err := h.followupUserContext(ctx, models.BootstrapUserID)
	require.NoError(t, err)
	require.ErrorIs(t, h.requireFollowupAccess(ownerCtx, params), authz.ErrDenied, "owner cannot replace the bound sender")
	foreign := params
	foreign.ChannelReply.TelegramUserID = 999999999
	require.Error(t, h.requireFollowupAccess(restored, foreign))
	running, cancel := context.WithCancel(restored)
	defer cancel()
	require.NoError(t, tc.stores.TelegramAuth.DeleteForProject(owner, "default", entry.ID))
	require.NoError(t, running.Err(), "revocation does not stop a run already started")
	require.Error(t, h.requireFollowupAccess(restored, params))
}

func TestAuthenticatedFollowupRestart(t *testing.T) {
	for _, action := range []string{"run", "active lane"} {
		for _, status := range []models.ExecutionStatus{models.ExecCancelled, models.ExecFailed} {
			for _, role := range []string{"editor", "viewer"} {
				t.Run(fmt.Sprintf("%s/%s/%s", action, status, role), func(t *testing.T) {
					tc := NewStoreTestContext(t)
					h, ctx := tc.handler, t.Context()
					h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
					h.SetRunIdentityStore(tc.stores.RunIdentities)
					h.SetAuthMode(auth.AuthModeLocal)
					org := service.NewOrganizationService(tc.stores.Organizations, "secret")
					h.SetOrganizationService(org)
					invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
					require.NoError(t, err)
					user, err := org.Accept(ctx, invite, "retry-user", "long-password-value", "")
					require.NoError(t, err)
					require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, "default", user.UserID, role))
					agent := createAgent(t, tc.llmConfigRepo)
					task := createTask(t, h, "default", "Authenticated restart", func(task *models.Task) {
						task.Category = models.CategoryBacklog
						task.Status = models.TaskStatus(status)
						task.AgentID = &agent.ID
						task.Prompt = "original prompt"
					})
					createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
						exec.Status = status
						exec.IsFollowup = true
						exec.PromptSent = "longer!"
					})
					mock := testutil.NewMockLLMCaller()
					mock.Response, mock.TextOnly = "finished", "finished"
					h.llmSvc.SetLLMCaller(mock)
					caller := models.WithPrincipal(ctx, user.Principal)
					form := url.Values{"project_id": {"default"}, "category": {"active"}, "target_status": {"running"}}
					req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/run?project_id=default", strings.NewReader(form.Encode())).WithContext(caller)
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.Header.Set("HX-Request", "true")
					c := tc.echo.NewContext(req, httptest.NewRecorder())
					c.SetParamNames("taskId")
					c.SetParamValues(task.ID)
					if action == "run" {
						err = h.RunTask(c)
					} else {
						err = h.UpdateTaskCategory(c)
					}
					if role == "viewer" {
						require.Error(t, err)
						execs, loadErr := h.execRepo.ListByTaskChronological(ctx, task.ID)
						require.NoError(t, loadErr)
						require.Len(t, execs, 1)
						require.Zero(t, mock.CallCount())
						return
					}
					// Active-lane routing can return a conflict after successfully starting
					// its lifecycle-owned follow-up; execution state is the success criterion.
					if action == "run" {
						require.NoError(t, err)
					}
					require.Eventually(t, func() bool {
						execs, loadErr := h.execRepo.ListByTaskChronological(ctx, task.ID)
						return loadErr == nil && len(execs) == 2 && execs[1].Status == models.ExecCompleted
					}, 3*time.Second, 10*time.Millisecond)
					require.Equal(t, 1, mock.CallCount())
					require.Equal(t, "longer!", mock.LastCall().Prompt)
					execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					executor, found, err := tc.stores.RunIdentities.ExecutionUser(ctx, execs[1].ID)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, user.UserID, executor)
				})
			}
		}
	}
}
