package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type fakeXAPI struct {
	me          XUser
	mentions    xMentionsResponse
	mentionsErr error
	posted      []string
	postErr     error
}

func (f *fakeXAPI) Me(context.Context) (XUser, error) { return f.me, nil }
func (f *fakeXAPI) Mentions(context.Context, string, string, string) (xMentionsResponse, error) {
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
	runtime := svc.runtimeTools("caller-task", p1.ID, "123", "conv", "tweet", "alice")

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

	// Provider redelivery after the durable transaction must observe the completed
	// receipt and never create duplicate work.
	require.NoError(t, svc.pollOnce(ctx))
	tasks, err = svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
}

func TestXPollActiveReceiptLeaseDoesNotAdvanceCursor(t *testing.T) {
	ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentions.Meta.NewestID = "10"
	api.mentions.Data = []XTweet{{ID: "10", Text: "@openvibely hello", AuthorID: "author", ConversationID: "conversation"}}
	api.mentions.Includes.Users = []XUser{{ID: "author", Username: "alice"}}
	svc.setAPI(api)
	svc.me = api.me
	receipts := svc.receiptRepo
	claim, err := receipts.Claim(ctx, "10", project.ID, svc.now(), xReceiptLease)
	require.NoError(t, err)
	require.Equal(t, repository.XReceiptClaimed, claim.Result)

	require.Error(t, svc.pollOnce(ctx))
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
}

func TestXRuntimeOwnsOnlyIdentitySensitiveOverrides(t *testing.T) {
	ctx, svc, _, auth, _, project, _ := setupXServiceTest(t)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123"}))
	runtime := svc.RuntimeTools("caller", project.ID, "123", "conversation", "tweet", "alice")
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
