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

func TestXRuntimeListChannelsReportsLiveStatusAndProjectAuthorization(t *testing.T) {
	ctx, svc, settings, auth, _, p1, _ := setupXServiceTest(t)
	for key, value := range map[string]string{
		XSettingConsumerKey: "a", XSettingConsumerSecret: "b", XSettingAccessToken: "c", XSettingAccessTokenSecret: "d",
	} {
		require.NoError(t, settings.Set(ctx, key, value))
	}
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: p1.ID, XUserID: "123"}))
	svc.mu.Lock()
	svc.me = XUser{ID: "bot", Username: "openvibely"}
	svc.running = true
	svc.mu.Unlock()

	runtime := svc.runtimeTools("caller-task", p1.ID, "123", "conv", "tweet", "alice")
	output, handled, isError, err := runtime.Executor(ctx, "list_channels", []byte(`{}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, `"running": true`)
	require.Contains(t, output, `"username": "openvibely"`)
	require.Contains(t, output, `"authorized_user_count": 1`)
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
