package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type fakeXAPI struct {
	me           XUser
	mentions     xMentionsResponse
	mentionsErr  error
	mentionsFunc func(context.Context, string, string, string) (xMentionsResponse, error)
	posted       []string
	postErr      error
}

func (f *fakeXAPI) Me(context.Context) (XUser, error) { return f.me, nil }
func (f *fakeXAPI) Mentions(ctx context.Context, userID, sinceID, pagination string) (xMentionsResponse, error) {
	if f.mentionsFunc != nil {
		return f.mentionsFunc(ctx, userID, sinceID, pagination)
	}
	return f.mentions, f.mentionsErr
}
func (f *fakeXAPI) Post(_ context.Context, text, reply string) (string, error) {
	f.posted = append(f.posted, reply+"|"+text)
	return "posted", f.postErr
}

func setupXServiceTest(t *testing.T) (context.Context, *XService, *repository.SettingsRepo, *repository.XAuthRepo, *repository.XUserProjectRepo, *models.Project, *models.Project) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := repository.NewProjectRepo(db)
	p1 := &models.Project{Name: "One"}
	p2 := &models.Project{Name: "Two"}
	require.NoError(t, projects.Create(ctx, p1))
	require.NoError(t, projects.Create(ctx, p2))
	settings := repository.NewSettingsRepo(db)
	require.NoError(t, settings.SetMany(ctx, map[string]string{XSettingAccountID: "bot", XSettingSinceID: ""}))
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	svc := NewXService(
		XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"},
		settings,
		projects,
		repository.NewLLMConfigRepo(db),
		repository.NewTaskRepo(db, nil),
		repository.NewExecutionRepo(db),
		repository.NewScheduleRepo(db),
		nil,
	)
	svc.SetRepositories(auth, selections, repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db), repository.NewThreadInputRepo(db))
	return ctx, svc, settings, auth, selections, p1, p2
}

func TestXRuntimeProjectSwitchRequiresTargetAuthorizationAndPersists(t *testing.T) {
	ctx, svc, _, auth, selections, p1, p2 := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: p1.ID, XUserID: "123"}))
	runtime := svc.runtimeTools("caller-task", p1.ID, "bot", "123", "conv", "tweet", "alice")

	output, handled, isError, err := runtime.Executor(ctx, "switch_project", []byte(`{"project":"Two"}`))
	require.True(t, handled)
	require.True(t, isError)
	require.Error(t, err)
	require.Contains(t, output+err.Error(), "authorized")
	selected, err := selections.GetUserProject(ctx, "123")
	require.NoError(t, err)
	require.Empty(t, selected)

	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: p2.ID, XUserID: "123"}))
	output, handled, isError, err = runtime.Executor(ctx, "switch_project", []byte(`{"project":"Two"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "Two")
	selected, err = selections.GetUserProject(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, p2.ID, selected)
}

func TestXPollBoundsPaginationWithoutAdvancingCursor(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot"}}
	api.mentions.Meta.NewestID = "100"
	api.mentions.Meta.NextToken = "more"
	svc.setAPI(api)
	svc.me = api.me
	require.ErrorContains(t, svc.pollOnce(ctx), "pagination exceeded")
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
}

func TestXPollCannotOverwriteCursorAfterAccountReplacement(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	require.NoError(t, settings.SetMany(ctx, map[string]string{XSettingAccountID: "new-account", XSettingSinceID: "50"}))
	api := &fakeXAPI{me: XUser{ID: "old-account"}}
	api.mentions.Meta.NewestID = "99"
	svc.setAPI(api)
	svc.me = api.me
	require.ErrorContains(t, svc.pollOnce(ctx), "configuration changed")
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "50", cursor)
}

func TestXTestConnectionRequiresMentionReadAccess(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot"}, mentionsErr: errors.New("mention access revoked")}
	svc.setAPI(api)
	_, err := svc.TestConnection(ctx)
	require.ErrorContains(t, err, "mention access")
}

func TestXPollProviderFailureDoesNotAdvanceCursor(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot"}, mentionsErr: errors.New("provider down")}
	svc.setAPI(api)
	svc.me = api.me
	require.Error(t, svc.pollOnce(ctx))
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
}

func TestXPollCursorReadFailureDoesNotCallProvider(t *testing.T) {
	db := testutil.NewTestDB(t)
	settings := repository.NewSettingsRepo(db)
	svc := NewXService(
		XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"},
		settings,
		repository.NewProjectRepo(db),
		repository.NewLLMConfigRepo(db),
		repository.NewTaskRepo(db, nil),
		repository.NewExecutionRepo(db),
		repository.NewScheduleRepo(db),
		nil,
	)
	svc.SetRepositories(repository.NewXAuthRepo(db), repository.NewXUserProjectRepo(db), repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db), repository.NewThreadInputRepo(db))
	called := false
	svc.setAPI(&fakeXAPI{mentionsFunc: func(context.Context, string, string, string) (xMentionsResponse, error) {
		called = true
		return xMentionsResponse{}, nil
	}})
	svc.me = XUser{ID: "bot"}
	require.NoError(t, db.Close())

	require.ErrorContains(t, svc.pollOnce(context.Background()), "load X polling settings")
	require.False(t, called)
}

func TestXReplySettingReadFailureDoesNotPost(t *testing.T) {
	db := testutil.NewTestDB(t)
	settings := repository.NewSettingsRepo(db)
	svc := NewXService(
		XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"},
		settings, nil, nil, nil, nil, nil, nil,
	)
	api := &fakeXAPI{}
	svc.setAPI(api)
	require.NoError(t, db.Close())

	svc.SendReply(context.Background(), "tweet", "must not post", "")
	require.Empty(t, api.posted)
}

func TestXPollConfigurationRemovalDuringProviderRequestCreatesNoWork(t *testing.T) {
	ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentionsFunc = func(context.Context, string, string, string) (xMentionsResponse, error) {
		require.NoError(t, settings.SetMany(ctx, map[string]string{XSettingAccountID: "", XSettingSinceID: ""}))
		var page xMentionsResponse
		page.Meta.NewestID = "22"
		page.Data = []XTweet{{ID: "22", Text: "@openvibely stale", AuthorID: "author", ConversationID: "conversation"}}
		return page, nil
	}
	svc.setAPI(api)
	svc.me = api.me

	require.ErrorContains(t, svc.pollOnce(ctx), "configuration changed")
	tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
	require.NoError(t, err)
	require.Empty(t, tasks)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
}

func TestXPollSameAccountReplacementGenerationCreatesNoWork(t *testing.T) {
	ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, settings.Set(ctx, XSettingConfigurationID, "old-generation"))
	svc.SetConfigurationID("old-generation")
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentionsFunc = func(context.Context, string, string, string) (xMentionsResponse, error) {
		require.NoError(t, settings.Set(ctx, XSettingConfigurationID, "replacement-generation"))
		var page xMentionsResponse
		page.Meta.NewestID = "23"
		page.Data = []XTweet{{ID: "23", Text: "@openvibely stale", AuthorID: "author", ConversationID: "conversation"}}
		return page, nil
	}
	svc.setAPI(api)
	svc.me = api.me

	require.ErrorContains(t, svc.pollOnce(ctx), "configuration changed")
	tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
	require.NoError(t, err)
	require.Empty(t, tasks)
}

func TestXAuthorizedMentionUsesSharedIngressAndAdvancesCursorAfterDurableHandoff(t *testing.T) {
	ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))
	var run ChannelChatRunRequest
	svc.SetRuntime(nil, nil, nil, nil, func(_ context.Context, req ChannelChatRunRequest) { run = req }, nil, nil, nil, nil)
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "20"
	api.mentions.Data = []XTweet{{ID: "20", Text: "@openvibely ship it", AuthorID: "author", ConversationID: "conversation"}}
	api.mentions.Includes.Users = []XUser{{ID: "author", Username: "alice"}}
	svc.setAPI(api)
	svc.me = api.me

	require.NoError(t, svc.pollOnce(ctx))
	require.Equal(t, "20", run.ReplyContext.XReplyToTweetID)
	require.NotNil(t, run.RuntimeTools)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "20", cursor)
	tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, models.TaskOriginX, tasks[0].CreatedVia)
	meta, err := svc.taskContextRepo.GetByTaskID(ctx, tasks[0].ID)
	require.NoError(t, err)
	require.Equal(t, "20", meta.ReplyToTweetID)
	require.Equal(t, "bot", meta.AccountID)

	// Provider redelivery after the durable transaction must observe the completed
	// receipt and never create duplicate work.
	require.NoError(t, svc.pollOnce(ctx))
	tasks, err = svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestXAuthorizedMentionTextPreservesHandlesAndPunctuation(t *testing.T) {
	tests := []struct {
		name     string
		username string
		text     string
		want     string
	}{
		{name: "exact case", username: "openvibely", text: "@openvibely ship it", want: "ship it"},
		{name: "mixed case", username: "openvibely", text: "@OpenVibely ship it", want: "ship it"},
		{name: "prefixed handle", username: "openvibely", text: "@openvibely please check @openvibelybot", want: "please check @openvibelybot"},
		{name: "repeated mentions", username: "openvibely", text: "@OpenVibely, please ask @OPENVIBELY!", want: ", please ask !"},
		{name: "punctuation adjacent", username: "openvibely", text: "Please check (@OpenVibely), now.", want: "Please check (), now."},
		{name: "ordinary at sign", username: "openvibely", text: "Keep email@example.com and @someone unchanged", want: "Keep email@example.com and @someone unchanged"},
		{name: "empty username", username: "", text: "Keep email@example.com and @someone and @openvibely unchanged", want: "Keep email@example.com and @someone and @openvibely unchanged"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
			require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
			require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))
			var run ChannelChatRunRequest
			svc.SetRuntime(nil, nil, nil, nil, func(_ context.Context, req ChannelChatRunRequest) { run = req }, nil, nil, nil, nil)
			api := &fakeXAPI{me: XUser{ID: "bot", Username: tt.username}}
			api.mentions.Meta.NewestID = fmt.Sprintf("%d", 100+i)
			api.mentions.Data = []XTweet{{ID: api.mentions.Meta.NewestID, Text: tt.text, AuthorID: "author", ConversationID: "conversation"}}
			svc.setAPI(api)
			svc.me = api.me

			require.NoError(t, svc.pollOnce(ctx))
			require.Equal(t, tt.want, run.Message)
			cursor, err := settings.Get(ctx, XSettingSinceID)
			require.NoError(t, err)
			require.Equal(t, api.mentions.Meta.NewestID, cursor)
			tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, models.TaskOriginX, tasks[0].CreatedVia)
			receipt, err := svc.receiptRepo.Claim(ctx, api.mentions.Meta.NewestID, project.ID, svc.now(), xReceiptLease)
			require.NoError(t, err)
			require.Equal(t, repository.XReceiptCompleted, receipt.Result)

			// A provider redelivery observes the completed durable receipt and does
			// not create a second task.
			require.NoError(t, svc.pollOnce(ctx))
			tasks, err = svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
			require.NoError(t, err)
			require.Len(t, tasks, 1)
		})
	}
}

func TestXAuthorizedMentionUsesAuthorIDWhenExpansionIsMissing(t *testing.T) {
	ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))
	var run ChannelChatRunRequest
	svc.SetRuntime(nil, nil, nil, nil, func(_ context.Context, req ChannelChatRunRequest) { run = req }, nil, nil, nil, nil)
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "21"
	api.mentions.Data = []XTweet{{ID: "21", Text: "@openvibely ship it", AuthorID: "author", ConversationID: "conversation"}}
	svc.setAPI(api)
	svc.me = api.me

	require.NoError(t, svc.pollOnce(ctx))
	require.Equal(t, "author", run.ReplyContext.XUserID)
	require.Equal(t, "bot", run.ReplyContext.XAccountID)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "21", cursor)
}

func TestXMissingAuthorIDFailsClosedWithoutAdvancingCursor(t *testing.T) {
	ctx, svc, settings, _, _, project, otherProject := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "22"
	api.mentions.Data = []XTweet{{ID: "22", Text: "@openvibely ship it", ConversationID: "conversation"}}
	svc.setAPI(api)
	svc.me = api.me

	require.ErrorContains(t, svc.pollOnce(ctx), "author_id")
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
	for _, projectID := range []string{project.ID, otherProject.ID} {
		tasks, err := svc.taskRepo.ListByProject(ctx, projectID, string(models.CategoryChat))
		require.NoError(t, err)
		require.Empty(t, tasks)
	}
}

func TestXSameAccountCursorRaceIsBenign(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentionsFunc = func(context.Context, string, string, string) (xMentionsResponse, error) {
		require.NoError(t, settings.Set(ctx, XSettingSinceID, "20"))
		var page xMentionsResponse
		page.Meta.NewestID = "20"
		return page, nil
	}
	svc.setAPI(api)
	svc.me = api.me
	svc.running = true
	svc.connected = true

	err := svc.pollOnce(ctx)
	require.NoError(t, err)
	svc.recordPollResult(err)
	require.True(t, svc.Status().Connected)
	require.Empty(t, svc.Status().LastError)
}

func TestXReplyRequiresOriginatingAccount(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)
	svc.me = XUser{ID: "new-account", Username: "new"}

	require.False(t, svc.SendReplyForAccount(ctx, "old-account", "tweet", "response", ""))
	require.Empty(t, api.posted)
	require.True(t, svc.SendReplyForAccount(ctx, "new-account", "tweet", "response", ""))
	require.Equal(t, []string{"tweet|response"}, api.posted)
}

func TestXPollActiveReceiptLeaseDoesNotAdvanceCursorOrDegradeReplacementHealth(t *testing.T) {
	ctx, old, settings, auth, selections, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "10"
	api.mentions.Data = []XTweet{{ID: "10", Text: "@openvibely hello", AuthorID: "author", ConversationID: "conversation"}}
	api.mentions.Includes.Users = []XUser{{ID: "author", Username: "alice"}}
	claim, err := old.receiptRepo.Claim(ctx, "10", project.ID, old.now(), xReceiptLease)
	require.NoError(t, err)
	require.Equal(t, repository.XReceiptClaimed, claim.Result)

	replacement := NewXService(old.credentials, settings, old.projectRepo, old.llmConfigRepo, old.taskRepo, old.execRepo, old.scheduleRepo, old.taskSvc)
	replacement.SetRepositories(auth, selections, old.taskContextRepo, old.receiptRepo, old.threadInputRepo)
	replacement.setAPI(api)
	replacement.me = api.me
	replacement.running = true
	replacement.connected = true
	err = replacement.pollOnce(ctx)
	require.Error(t, err)
	replacement.recordPollResult(err)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
	require.True(t, replacement.Status().Connected)
	require.Empty(t, replacement.Status().LastError)
}

func TestXRuntimeOwnsOnlyIdentitySensitiveOverrides(t *testing.T) {
	ctx, svc, _, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123"}))
	runtime := svc.RuntimeTools("caller", project.ID, "bot", "123", "conversation", "tweet", "alice")
	names := map[string]bool{}
	for _, def := range runtime.Definitions {
		names[def.Name] = true
	}
	require.True(t, names["switch_project"])
	require.True(t, names["create_task"])
	require.True(t, names["send_to_task"])
	require.False(t, names["list_channels"], "generic handler must retain complete cross-channel status dependencies")
	require.False(t, names["view_pulse"], "generic handler must retain complete upcoming-work dependencies")
}

func TestXImmediateTaskThreadRunCarriesAuthorizedRuntimeTools(t *testing.T) {
	ctx, svc, _, auth, selections, project, targetProject := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123"}))
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: targetProject.ID, XUserID: "123"}))
	agent := &models.LLMConfig{Name: "X Task Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, svc.llmConfigRepo.Create(ctx, agent))
	task := &models.Task{ProjectID: project.ID, Title: "X follow-up target", Prompt: "initial", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, AgentID: &agent.ID}
	require.NoError(t, svc.taskRepo.Create(ctx, task))
	var run ChannelTaskRunRequest
	svc.SetRuntime(nil, nil, nil, nil, nil, func(_ context.Context, req ChannelTaskRunRequest) { run = req }, nil, nil, nil)

	runtime := svc.RuntimeTools("caller", project.ID, "bot", "123", "conversation", "tweet", "alice")
	payload, err := json.Marshal(SendToTaskRequest{TaskID: task.ID, Message: "continue"})
	require.NoError(t, err)
	output, handled, isError, err := runtime.Executor(ctx, "send_to_task", payload)
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "started processing")
	require.NotNil(t, run.RuntimeTools)

	output, handled, isError, err = run.RuntimeTools.Executor(ctx, "switch_project", json.RawMessage(`{"project":"Two"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "Two")
	selected, err := selections.GetUserProject(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, targetProject.ID, selected)
}

func TestXPollAcknowledgesUnauthorizedMentionsWithoutCreatingWork(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "10"
	api.mentions.Data = []XTweet{{ID: "10", Text: "@openvibely hello", AuthorID: "intruder", ConversationID: "conversation"}}
	api.mentions.Includes.Users = []XUser{{ID: "intruder", Username: "intruder"}}
	svc.setAPI(api)
	svc.me = api.me

	require.NoError(t, svc.pollOnce(ctx))
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "10", cursor)
}

func TestXTweetIDsSortNumerically(t *testing.T) {
	require.True(t, xTweetIDLess("9", "10"))
	require.True(t, xTweetIDLess("0009", "10"))
	require.False(t, xTweetIDLess("10", "9"))
}

func TestXDisconnectedAndResponsesDisabledFailClosed(t *testing.T) {
	_, incomplete, _, _, _, _, _ := setupXServiceTest(t)
	incomplete.credentials = XCredentials{}
	require.Error(t, incomplete.Start())
	require.False(t, incomplete.Status().Running)

	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)
	require.NoError(t, settings.Set(ctx, XSettingSendResponses, "false"))
	svc.SendReply(ctx, "tweet", "response", "")
	require.Empty(t, api.posted)
}

func TestXReadinessTracksPollingFailureAndRecovery(t *testing.T) {
	_, svc, _, _, _, _, _ := setupXServiceTest(t)
	svc.running = true
	svc.connected = true
	svc.me = XUser{ID: "bot", Username: "openvibely"}
	svc.recordPollResult(errors.New("mention access revoked"))
	status := svc.Status()
	require.True(t, status.Running, "poller liveness remains distinct from provider readiness")
	require.False(t, status.Connected)
	require.Contains(t, status.LastError, "revoked")

	svc.recordPollResult(nil)
	status = svc.Status()
	require.True(t, status.Connected)
	require.Empty(t, status.LastError)
}

func TestXOutboundUsesWeightedPostLimit(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	tests := []struct {
		name       string
		text       string
		shouldPost bool
	}{
		{name: "ascii at limit", text: strings.Repeat("x", 280), shouldPost: true},
		{name: "emoji at weighted limit", text: strings.Repeat("😀", 140), shouldPost: true},
		{name: "emoji over limit", text: strings.Repeat("😀", 141), shouldPost: false},
		{name: "CJK over limit", text: strings.Repeat("界", 141), shouldPost: false},
		{name: "URL over limit despite rune count", text: strings.Repeat("x", 258) + " https://example.com", shouldPost: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api.posted = nil
			result := svc.SendOutboundMessage(ctx, "me", "", tt.text)
			if tt.shouldPost {
				require.True(t, result.OK)
				require.Len(t, api.posted, 1)
			} else {
				require.False(t, result.OK)
				require.Empty(t, api.posted)
			}
		})
	}
}

func TestXReplyTruncatesToWeightedPostLimit(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	svc.SendReply(ctx, "tweet", strings.Repeat("😀", 141), "")
	require.Equal(t, []string{"tweet|" + strings.Repeat("😀", 139) + "…"}, api.posted)

	api.posted = nil
	svc.SendReply(ctx, "tweet", strings.Repeat("x", 281), "")
	require.Equal(t, []string{"tweet|" + strings.Repeat("x", 278) + "…"}, api.posted)
}

func TestXReplyDisabledOrEmptyDoesNotPost(t *testing.T) {
	ctx, svc, settings, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	require.NoError(t, settings.Set(ctx, XSettingSendResponses, "false"))
	svc.SendReply(ctx, "tweet", "disabled", "")
	require.Empty(t, api.posted)

	require.NoError(t, settings.Set(ctx, XSettingSendResponses, "true"))
	svc.SendReply(ctx, "tweet", "  \t\n", "")
	require.Empty(t, api.posted)
}

func TestXOutboundRejectsUnsupportedTargetAndOversizeAndPropagatesProviderFailure(t *testing.T) {
	_, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{postErr: errors.New("write access unavailable")}
	svc.setAPI(api)

	result := svc.SendOutboundMessage(context.Background(), "123", "", "hello")
	require.False(t, result.OK)
	require.Empty(t, api.posted)
	result = svc.SendOutboundMessage(context.Background(), "me", "", strings.Repeat("x", 281))
	require.False(t, result.OK)
	require.Empty(t, api.posted)
	result = svc.SendOutboundMessage(context.Background(), "me", "", "short")
	require.False(t, result.OK)
	require.Contains(t, result.Error, "write access unavailable")
}
