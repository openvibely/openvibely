package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

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

type xProjectResolutionFixture struct {
	ctx           context.Context
	counter       *testutil.SQLStatementCounter
	projectRepo   *repository.ProjectRepo
	authRepo      *repository.XAuthRepo
	selectionRepo *repository.XUserProjectRepo
	svc           *XService
	projectID     string
}

const (
	xProjectResolutionMedianSamples  = 7
	xProjectResolutionTimingRuns     = 3
	xProjectResolutionAllocationRuns = 3
)

func newXProjectResolutionFixture(tb testing.TB, projectCount int) xProjectResolutionFixture {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	var existingProjectCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&existingProjectCount); err != nil {
		tb.Fatalf("count seeded projects: %v", err)
	}
	if existingProjectCount >= projectCount {
		tb.Fatalf("fixture already has %d projects, cannot create exact %d-project fixture", existingProjectCount, projectCount)
	}
	var lastProject *models.Project
	for i := existingProjectCount; i < projectCount; i++ {
		project := &models.Project{Name: fmt.Sprintf("X benchmark project %04d", i)}
		if err := projectRepo.Create(ctx, project); err != nil {
			tb.Fatalf("create benchmark project %d: %v", i, err)
		}
		lastProject = project
	}
	authRepo := repository.NewXAuthRepo(db)
	if err := authRepo.Create(ctx, &models.XAuthorizedUser{ProjectID: lastProject.ID, XUserID: "benchmark-author"}); err != nil {
		tb.Fatalf("authorize benchmark user: %v", err)
	}
	selectionRepo := repository.NewXUserProjectRepo(db)
	svc := NewXService(XCredentials{}, repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil)
	svc.SetRepositories(authRepo, selectionRepo, nil, nil, nil)
	return xProjectResolutionFixture{
		ctx:           ctx,
		counter:       counter,
		projectRepo:   projectRepo,
		authRepo:      authRepo,
		selectionRepo: selectionRepo,
		svc:           svc,
		projectID:     lastProject.ID,
	}
}

func TestXProjectForUserUsesBoundedUserKeyedAuthorizationLookup(t *testing.T) {
	fixture := newXProjectResolutionFixture(t, 100)
	counter := fixture.counter

	counter.Reset()
	counter.SetEnabled(true)
	for i := 0; i < 2; i++ {
		projectID, err := fixture.svc.projectForUser(fixture.ctx, "benchmark-author")
		require.NoError(t, err)
		require.Equal(t, fixture.projectID, projectID)
	}
	counter.SetEnabled(false)

	statements := counter.Statements()
	require.Len(t, statements, 4)
	for _, statement := range statements {
		require.NotContains(t, statement, "FROM projects ORDER BY")
	}
}

func TestXProjectForUserPreservesExplicitSelectionAndOrderedFallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projects := repository.NewProjectRepo(db)
	defaultProject := &models.Project{Name: "Zulu"}
	nameOrderedProject := &models.Project{Name: "Alpha"}
	otherProject := &models.Project{Name: "Bravo"}
	for _, project := range []*models.Project{defaultProject, nameOrderedProject, otherProject} {
		require.NoError(t, projects.Create(ctx, project))
	}
	_, err := db.ExecContext(ctx, `UPDATE projects SET is_default = 1 WHERE id = ?`, defaultProject.ID)
	require.NoError(t, err)

	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	svc := NewXService(XCredentials{}, repository.NewSettingsRepo(db), projects, nil, nil, nil, nil, nil)
	svc.SetRepositories(auth, selections, nil, nil, nil)

	nameOrderedAuth := &models.XAuthorizedUser{ProjectID: nameOrderedProject.ID, XUserID: "author"}
	otherAuth := &models.XAuthorizedUser{ProjectID: otherProject.ID, XUserID: "author"}
	for _, user := range []*models.XAuthorizedUser{nameOrderedAuth, otherAuth} {
		require.NoError(t, auth.Create(ctx, user))
	}
	projectID, err := svc.projectForUser(ctx, "author")
	require.NoError(t, err)
	require.Equal(t, nameOrderedProject.ID, projectID)

	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: defaultProject.ID, XUserID: "author"}))
	projectID, err = svc.projectForUser(ctx, "author")
	require.NoError(t, err)
	require.Equal(t, defaultProject.ID, projectID)

	require.NoError(t, selections.SetUserProject(ctx, "author", otherProject.ID))
	projectID, err = svc.projectForUser(ctx, "author")
	require.NoError(t, err)
	require.Equal(t, otherProject.ID, projectID)

	require.NoError(t, auth.Delete(ctx, otherProject.ID, otherAuth.ID))
	projectID, err = svc.projectForUser(ctx, "author")
	require.NoError(t, err)
	require.Equal(t, defaultProject.ID, projectID)

	projectID, err = svc.projectForUser(ctx, "unauthorized")
	require.NoError(t, err)
	require.Empty(t, projectID)
}

func TestXProjectForUserMeetsMedianResolutionThresholdAt1000Projects(t *testing.T) {
	fixture := newXProjectResolutionFixture(t, 1000)
	current := measureXProjectResolution(t, fixture.projectID, func() (string, error) {
		return xProjectForUserFanoutBaseline(fixture.ctx, fixture.projectRepo, fixture.authRepo, fixture.selectionRepo, "benchmark-author")
	})
	candidate := measureXProjectResolution(t, fixture.projectID, func() (string, error) {
		return fixture.svc.projectForUser(fixture.ctx, "benchmark-author")
	})

	require.LessOrEqual(t, float64(candidate.medianDuration), 0.25*float64(current.medianDuration),
		"candidate median resolution time must be at least 75%% lower: current=%s candidate=%s", current.medianDuration, candidate.medianDuration)
	require.LessOrEqual(t, candidate.medianAllocs, 0.25*current.medianAllocs,
		"candidate median allocations must be at least 75%% lower: current=%.1f candidate=%.1f", current.medianAllocs, candidate.medianAllocs)
}

func BenchmarkXProjectForUserResolution(b *testing.B) {
	var candidateMedians = make(map[int]time.Duration, 2)
	for _, projectCount := range []int{100, 1000} {
		projectCount := projectCount
		b.Run(fmt.Sprintf("%d_projects", projectCount), func(b *testing.B) {
			fixture := newXProjectResolutionFixture(b, projectCount)
			currentMeasurement := measureXProjectResolution(b, fixture.projectID, func() (string, error) {
				return xProjectForUserFanoutBaseline(fixture.ctx, fixture.projectRepo, fixture.authRepo, fixture.selectionRepo, "benchmark-author")
			})
			candidateMeasurement := measureXProjectResolution(b, fixture.projectID, func() (string, error) {
				return fixture.svc.projectForUser(fixture.ctx, "benchmark-author")
			})
			candidateMedians[projectCount] = candidateMeasurement.medianDuration
			if projectCount == 1000 {
				requireXProjectResolutionThreshold(b, currentMeasurement, candidateMeasurement)
			}

			b.Run("CurrentFanOut", func(b *testing.B) {
				benchmarkXProjectResolution(b, fixture.counter, fixture.projectID, currentMeasurement, func() (string, error) {
					return xProjectForUserFanoutBaseline(fixture.ctx, fixture.projectRepo, fixture.authRepo, fixture.selectionRepo, "benchmark-author")
				})
			})
			b.Run("UserKeyedLookup", func(b *testing.B) {
				benchmarkXProjectResolution(b, fixture.counter, fixture.projectID, candidateMeasurement, func() (string, error) {
					return fixture.svc.projectForUser(fixture.ctx, "benchmark-author")
				})
			})
		})
	}
	if candidateMedians[100] > 0 && candidateMedians[1000] > 4*candidateMedians[100] {
		b.Fatalf("candidate median resolution is not effectively flat: 100 projects=%s, 1000 projects=%s", candidateMedians[100], candidateMedians[1000])
	}
}

func xProjectForUserFanoutBaseline(ctx context.Context, projects *repository.ProjectRepo, auth *repository.XAuthRepo, selections *repository.XUserProjectRepo, userID string) (string, error) {
	if id, err := selections.GetUserProject(ctx, userID); err != nil {
		return "", err
	} else if id != "" {
		ok, err := auth.IsAuthorized(ctx, id, userID)
		if err != nil {
			return "", err
		}
		if ok {
			return id, nil
		}
	}
	allProjects, err := projects.List(ctx)
	if err != nil {
		return "", err
	}
	for _, project := range allProjects {
		ok, err := auth.IsAuthorized(ctx, project.ID, userID)
		if err != nil {
			return "", err
		}
		if ok {
			return project.ID, nil
		}
	}
	return "", nil
}

type xProjectResolutionMeasurement struct {
	medianDuration time.Duration
	medianAllocs   float64
}

func measureXProjectResolution(tb testing.TB, want string, resolve func() (string, error)) xProjectResolutionMeasurement {
	tb.Helper()
	durations := make([]time.Duration, xProjectResolutionMedianSamples)
	allocations := make([]float64, xProjectResolutionMedianSamples)
	for sample := 0; sample < xProjectResolutionMedianSamples; sample++ {
		started := time.Now()
		for run := 0; run < xProjectResolutionTimingRuns; run++ {
			got, err := resolve()
			if err != nil {
				tb.Fatalf("timed resolution: %v", err)
			}
			if got != want {
				tb.Fatalf("timed resolution project = %q, want %q", got, want)
			}
		}
		durations[sample] = time.Since(started) / time.Duration(xProjectResolutionTimingRuns)

		var got string
		var err error
		allocations[sample] = testing.AllocsPerRun(xProjectResolutionAllocationRuns, func() {
			got, err = resolve()
		})
		if err != nil {
			tb.Fatalf("allocation resolution: %v", err)
		}
		if got != want {
			tb.Fatalf("allocation resolution project = %q, want %q", got, want)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	sort.Float64s(allocations)
	return xProjectResolutionMeasurement{
		medianDuration: durations[len(durations)/2],
		medianAllocs:   allocations[len(allocations)/2],
	}
}

func requireXProjectResolutionThreshold(tb testing.TB, current, candidate xProjectResolutionMeasurement) {
	tb.Helper()
	if float64(candidate.medianDuration) > 0.25*float64(current.medianDuration) {
		tb.Fatalf("candidate median resolution time must be at least 75%% lower: current=%s candidate=%s", current.medianDuration, candidate.medianDuration)
	}
	if candidate.medianAllocs > 0.25*current.medianAllocs {
		tb.Fatalf("candidate median allocations must be at least 75%% lower: current=%.1f candidate=%.1f", current.medianAllocs, candidate.medianAllocs)
	}
}

func benchmarkXProjectResolution(b *testing.B, counter *testutil.SQLStatementCounter, want string, median xProjectResolutionMeasurement, resolve func() (string, error)) {
	b.Helper()
	counter.Reset()
	counter.SetEnabled(true)
	got, err := resolve()
	counter.SetEnabled(false)
	if err != nil {
		b.Fatalf("warm resolution: %v", err)
	}
	if got != want {
		b.Fatalf("warm resolution project = %q, want %q", got, want)
	}
	statementCount := len(counter.Statements())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := resolve()
		if err != nil {
			b.Fatalf("resolve project: %v", err)
		}
		if got != want {
			b.Fatalf("resolved project = %q, want %q", got, want)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(median.medianDuration.Nanoseconds()), "median_ns/op")
	b.ReportMetric(median.medianAllocs, "median_allocs/op")
	b.ReportMetric(float64(statementCount), "sql_statements/op")
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
