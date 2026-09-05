package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/idna"
)

type fakeXAPI struct {
	mu           sync.Mutex
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
	f.mu.Lock()
	f.posted = append(f.posted, reply+"|"+text)
	f.mu.Unlock()
	return "posted", f.postErr
}

func (f *fakeXAPI) Posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posted...)
}

func setupXServiceTest(t testing.TB) (context.Context, *XService, *repository.SettingsRepo, *repository.XAuthRepo, *repository.XUserProjectRepo, *models.Project, *models.Project) {
	return setupXServiceTestWithDB(t, testutil.NewTestDB(t))
}

func setupXServiceTestWithDB(t testing.TB, db *sql.DB) (context.Context, *XService, *repository.SettingsRepo, *repository.XAuthRepo, *repository.XUserProjectRepo, *models.Project, *models.Project) {
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

func setupXBatchService(t testing.TB, db *sql.DB, counter *testutil.SQLStatementCounter, mentionsPerPage, pages int) (context.Context, *XService, *repository.SettingsRepo) {
	ctx, svc, settings, auth, selections, project, _ := setupXServiceTestWithDB(t, db)
	require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
	require.NoError(t, selections.SetUserProject(ctx, "author", project.ID))
	require.NoError(t, settings.Set(ctx, XSettingConfigurationID, "generation"))
	svc.SetConfigurationID("generation")
	api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
	api.mentionsFunc = xMentionBatchFunc(mentionsPerPage, pages)
	svc.setAPI(api)
	svc.me = api.me
	counter.Reset()
	return ctx, svc, settings
}

func xMentionBatchFunc(mentionsPerPage, pages int) func(context.Context, string, string, string) (xMentionsResponse, error) {
	return func(_ context.Context, _, _, pagination string) (xMentionsResponse, error) {
		pageIndex := 0
		if pagination != "" {
			if _, err := fmt.Sscanf(pagination, "page-%d", &pageIndex); err != nil {
				return xMentionsResponse{}, fmt.Errorf("parse X test pagination token %q: %w", pagination, err)
			}
		}
		if pageIndex < 0 || pageIndex >= pages {
			return xMentionsResponse{}, fmt.Errorf("unexpected X test page index %d", pageIndex)
		}
		response := xMentionsResponse{}
		response.Meta.NewestID = fmt.Sprintf("%d", mentionsPerPage*pages)
		if pageIndex+1 < pages {
			response.Meta.NextToken = fmt.Sprintf("page-%d", pageIndex+1)
		}
		start := pageIndex*mentionsPerPage + 1
		response.Data = make([]XTweet, 0, mentionsPerPage)
		for i := 0; i < mentionsPerPage; i++ {
			id := fmt.Sprintf("%d", start+i)
			response.Data = append(response.Data, XTweet{ID: id, Text: "@openvibely", AuthorID: "author", ConversationID: "conversation-" + id})
		}
		return response, nil
	}
}

func xPollBatchProfileList() []xPollBatchProfile {
	return []xPollBatchProfile{
		{name: "1", mentionsPerPage: 1, pages: 1},
		{name: "10", mentionsPerPage: 10, pages: 1},
		{name: "100", mentionsPerPage: 100, pages: 1},
		{name: "paginated-1000", mentionsPerPage: 100, pages: 10},
	}
}

type xPollBatchProfile struct {
	name            string
	mentionsPerPage int
	pages           int
}

func pollXBatchMode(svc *XService, ctx context.Context, checkEachMention bool) error {
	if checkEachMention {
		return svc.pollOnceWithMentionConfigurationCheck(ctx, true)
	}
	return svc.pollOnce(ctx)
}

func countXSettingsSnapshots(statements []string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(statement)), " ", "")
		if normalized == "SELECTKEY,VALUEFROMAPP_SETTINGSWHEREKEYIN(?,?,?)" {
			count++
		}
	}
	return count
}

func xPollBatchStatements(t testing.TB, profile xPollBatchProfile, checkEachMention bool) ([]string, string) {
	t.Helper()
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx, svc, settings := setupXBatchService(t, db, counter, profile.mentionsPerPage, profile.pages)
	counter.SetEnabled(true)
	err := pollXBatchMode(svc, ctx, checkEachMention)
	counter.SetEnabled(false)
	require.NoError(t, err)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	return counter.Statements(), cursor
}

func TestXPollUsesOneConfigurationSnapshotPerBatch(t *testing.T) {
	for _, profile := range xPollBatchProfileList() {
		t.Run(profile.name, func(t *testing.T) {
			baselineStatements, baselineCursor := xPollBatchStatements(t, profile, true)
			candidateStatements, candidateCursor := xPollBatchStatements(t, profile, false)
			mentions := profile.mentionsPerPage * profile.pages
			wantCursor := fmt.Sprintf("%d", mentions)

			require.Equal(t, mentions+1, countXSettingsSnapshots(baselineStatements), "historical X polling should load one initial and one per-mention settings snapshot")
			require.Equal(t, 1, countXSettingsSnapshots(candidateStatements), "X polling should load one settings snapshot per batch")
			require.GreaterOrEqual(t, len(baselineStatements)-len(candidateStatements), mentions, "removing redundant snapshots should reduce total SQLite operations")
			if mentions == 100 {
				require.Equal(t, 1208, len(baselineStatements), "the 100-mention historical fixture should match the issue baseline")
				require.Equal(t, 1108, len(candidateStatements), "the 100-mention candidate should remove exactly 100 snapshot operations")
			}
			require.Equal(t, wantCursor, baselineCursor)
			require.Equal(t, wantCursor, candidateCursor)
		})
	}
}

type xPollBatchFixture struct {
	ctx      context.Context
	svc      *XService
	settings *repository.SettingsRepo
	counter  *testutil.SQLStatementCounter
}

func newXPollBatchFixture(t testing.TB, profile xPollBatchProfile) *xPollBatchFixture {
	t.Helper()
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx, svc, settings := setupXBatchService(t, db, counter, profile.mentionsPerPage, profile.pages)
	return &xPollBatchFixture{ctx: ctx, svc: svc, settings: settings, counter: counter}
}

func resetXPollBatchFixture(t testing.TB, fixture *xPollBatchFixture) {
	t.Helper()
	fixture.counter.SetEnabled(false)
	require.NoError(t, fixture.settings.Set(fixture.ctx, XSettingSinceID, ""))
	fixture.counter.Reset()
}

func warmXPollBatchFixture(t testing.TB, fixture *xPollBatchFixture) {
	t.Helper()
	require.NoError(t, pollXBatchMode(fixture.svc, fixture.ctx, false))
	resetXPollBatchFixture(t, fixture)
}

type xPollBatchMetrics struct {
	wallNs          float64
	bytesPerOp      float64
	allocsPerOp     float64
	statementsPerOp float64
	snapshotsPerOp  float64
}

const xPollBatchBenchmarkSamples = 3

func medianXPollBatch(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	return ordered[len(ordered)/2]
}

func measureXPollBatchMode(t *testing.T, fixture *xPollBatchFixture, checkEachMention bool) xPollBatchMetrics {
	t.Helper()
	wallSamples := make([]float64, 0, xPollBatchBenchmarkSamples)
	bytesSamples := make([]float64, 0, xPollBatchBenchmarkSamples)
	allocSamples := make([]float64, 0, xPollBatchBenchmarkSamples)
	statementSamples := make([]float64, 0, xPollBatchBenchmarkSamples)
	snapshotSamples := make([]float64, 0, xPollBatchBenchmarkSamples)
	for i := 0; i < xPollBatchBenchmarkSamples; i++ {
		resetXPollBatchFixture(t, fixture)
		fixture.counter.SetEnabled(true)
		err := pollXBatchMode(fixture.svc, fixture.ctx, checkEachMention)
		fixture.counter.SetEnabled(false)
		require.NoError(t, err)
		statements := fixture.counter.Statements()
		statementSamples = append(statementSamples, float64(len(statements)))
		snapshotSamples = append(snapshotSamples, float64(countXSettingsSnapshots(statements)))

		resetXPollBatchFixture(t, fixture)
		var pollErr error
		result := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for j := 0; j < b.N; j++ {
				if pollErr = pollXBatchMode(fixture.svc, fixture.ctx, checkEachMention); pollErr != nil {
					return
				}
			}
		})
		require.NoError(t, pollErr)
		wallSamples = append(wallSamples, float64(result.NsPerOp()))
		bytesSamples = append(bytesSamples, float64(result.AllocedBytesPerOp()))
		allocSamples = append(allocSamples, float64(result.AllocsPerOp()))
	}

	return xPollBatchMetrics{
		wallNs:          medianXPollBatch(wallSamples),
		bytesPerOp:      medianXPollBatch(bytesSamples),
		allocsPerOp:     medianXPollBatch(allocSamples),
		statementsPerOp: medianXPollBatch(statementSamples),
		snapshotsPerOp:  medianXPollBatch(snapshotSamples),
	}
}

func measureXPollBatchPair(t *testing.T, profile xPollBatchProfile) (xPollBatchMetrics, xPollBatchMetrics) {
	t.Helper()
	fixture := newXPollBatchFixture(t, profile)
	warmXPollBatchFixture(t, fixture)
	baseline := measureXPollBatchMode(t, fixture, true)
	candidate := measureXPollBatchMode(t, fixture, false)
	return baseline, candidate
}

func TestXPollBatchBenchmarkThresholds(t *testing.T) {
	for _, profile := range xPollBatchProfileList() {
		t.Run(profile.name, func(t *testing.T) {
			baseline, candidate := measureXPollBatchPair(t, profile)
			mentions := profile.mentionsPerPage * profile.pages
			t.Logf("median baseline: wall=%.0f ns/op bytes=%.0f B/op allocs=%.0f statements=%.0f snapshots=%.0f; candidate: wall=%.0f ns/op bytes=%.0f B/op allocs=%.0f statements=%.0f snapshots=%.0f", baseline.wallNs, baseline.bytesPerOp, baseline.allocsPerOp, baseline.statementsPerOp, baseline.snapshotsPerOp, candidate.wallNs, candidate.bytesPerOp, candidate.allocsPerOp, candidate.statementsPerOp, candidate.snapshotsPerOp)

			require.Equal(t, float64(mentions+1), baseline.snapshotsPerOp)
			require.Equal(t, float64(1), candidate.snapshotsPerOp)
			require.GreaterOrEqual(t, baseline.statementsPerOp-candidate.statementsPerOp, float64(mentions), "the candidate must remove one total SQL operation per non-self mention")

			switch mentions {
			case 1:
				// A single mention is too short for a reliable wall-time comparison on a
				// shared CI runner. Guard its deterministic resource costs instead; the
				// larger batch below still enforces the end-to-end timing improvement.
				require.LessOrEqual(t, candidate.bytesPerOp, baseline.bytesPerOp*1.05, "one-mention allocated bytes must not regress by more than 5%%")
				require.LessOrEqual(t, candidate.allocsPerOp, baseline.allocsPerOp*1.05, "one-mention allocations must not regress by more than 5%%")
			case 100:
				require.LessOrEqual(t, candidate.wallNs, baseline.wallNs*0.95, "100-mention wall time must improve by at least 5%%")
				require.LessOrEqual(t, candidate.allocsPerOp, baseline.allocsPerOp*0.95, "100-mention allocations must improve by at least 5%%")
			}
		})
	}
}

func BenchmarkXPollBatch(b *testing.B) {
	for _, profile := range xPollBatchProfileList() {
		b.Run(profile.name, func(b *testing.B) {
			fixture := newXPollBatchFixture(b, profile)
			warmXPollBatchFixture(b, fixture)
			for _, mode := range []struct {
				name             string
				checkEachMention bool
			}{
				{name: "baseline", checkEachMention: true},
				{name: "candidate", checkEachMention: false},
			} {
				b.Run(mode.name, func(b *testing.B) {
					resetXPollBatchFixture(b, fixture)
					fixture.counter.SetEnabled(true)
					err := pollXBatchMode(fixture.svc, fixture.ctx, mode.checkEachMention)
					fixture.counter.SetEnabled(false)
					require.NoError(b, err)
					statements := fixture.counter.Statements()
					mentions := profile.mentionsPerPage * profile.pages
					expectedSnapshots := 1
					if mode.checkEachMention {
						expectedSnapshots = mentions + 1
					}
					require.Equal(b, expectedSnapshots, countXSettingsSnapshots(statements))

					b.ReportAllocs()
					b.ResetTimer()
					b.ReportMetric(float64(len(statements)), "sqlite-statements/op")
					b.ReportMetric(float64(countXSettingsSnapshots(statements)), "settings-snapshots/op")
					for i := 0; i < b.N; i++ {
						if err := pollXBatchMode(fixture.svc, fixture.ctx, mode.checkEachMention); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

type xProjectResolutionFixture struct {
	ctx       context.Context
	counter   *testutil.SQLStatementCounter
	svc       *XService
	projectID string
}

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
		ctx:       ctx,
		counter:   counter,
		svc:       svc,
		projectID: lastProject.ID,
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

func BenchmarkXProjectForUserResolution(b *testing.B) {
	for _, projectCount := range []int{100, 1000} {
		b.Run(fmt.Sprintf("%d_projects", projectCount), func(b *testing.B) {
			fixture := newXProjectResolutionFixture(b, projectCount)
			benchmarkXProjectResolution(b, fixture.counter, fixture.projectID, func() (string, error) {
				return fixture.svc.projectForUser(fixture.ctx, "benchmark-author")
			})
		})
	}
}

func benchmarkXProjectResolution(b *testing.B, counter *testutil.SQLStatementCounter, want string, resolve func() (string, error)) {
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

func TestXPollReplacementBetweenMentionHandoffsCreatesNoStaleWork(t *testing.T) {
	for _, tt := range []struct {
		name              string
		initialGeneration string
		replaceSettings   func(context.Context, *repository.SettingsRepo) error
	}{
		{
			name: "account",
			replaceSettings: func(ctx context.Context, settings *repository.SettingsRepo) error {
				return settings.Set(ctx, XSettingAccountID, "replacement-account")
			},
		},
		{
			name:              "generation",
			initialGeneration: "old-generation",
			replaceSettings: func(ctx context.Context, settings *repository.SettingsRepo) error {
				return settings.Set(ctx, XSettingConfigurationID, "replacement-generation")
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, svc, settings, auth, _, project, _ := setupXServiceTest(t)
			if tt.initialGeneration != "" {
				require.NoError(t, settings.Set(ctx, XSettingConfigurationID, tt.initialGeneration))
				svc.SetConfigurationID(tt.initialGeneration)
			}
			require.NoError(t, auth.Create(ctx, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "author"}))
			require.NoError(t, svc.llmConfigRepo.Create(ctx, &models.LLMConfig{Name: "X Agent", Provider: models.ProviderTest, Model: "test", IsDefault: true}))

			handoffs := 0
			var replacementErr error
			svc.SetRuntime(nil, nil, nil, nil, func(_ context.Context, _ ChannelChatRunRequest) {
				handoffs++
				if handoffs == 1 {
					replacementErr = tt.replaceSettings(ctx, settings)
				}
			}, nil, nil, nil, nil)
			api := &fakeXAPI{me: XUser{ID: "bot", Username: "openvibely"}}
			api.mentions.Meta.NewestID = "2"
			api.mentions.Data = []XTweet{
				{ID: "2", Text: "@openvibely second", AuthorID: "author", ConversationID: "conversation-2"},
				{ID: "1", Text: "@openvibely first", AuthorID: "author", ConversationID: "conversation-1"},
			}
			svc.setAPI(api)
			svc.me = api.me

			err := svc.pollOnce(ctx)
			require.NoError(t, replacementErr)
			require.ErrorContains(t, err, "configuration changed")
			require.Equal(t, 1, handoffs)

			tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryChat))
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			require.Equal(t, "first", tasks[0].Prompt)
			pending, err := svc.threadInputRepo.ListPendingForChat(ctx, project.ID)
			require.NoError(t, err)
			require.Empty(t, pending)
			cursor, err := settings.Get(ctx, XSettingSinceID)
			require.NoError(t, err)
			require.Empty(t, cursor)
		})
	}
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
	require.NoError(t, settings.Set(ctx, XSettingConfigurationID, "generation"))
	old.SetConfigurationID("generation")
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
	replacement.SetConfigurationID("generation")
	replacement.running = true
	replacement.connected = true
	err = replacement.pollOnce(ctx)
	require.ErrorIs(t, err, errXReceiptActive)
	replacement.recordPollResult(err)
	cursor, err := settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
	require.True(t, replacement.Status().Connected)
	require.Empty(t, replacement.Status().LastError)

	// An active receipt is retryable only for the same configuration generation.
	require.NoError(t, settings.Set(ctx, XSettingConfigurationID, "replacement-generation"))
	err = replacement.pollOnce(ctx)
	require.ErrorContains(t, err, "configuration changed")
	cursor, err = settings.Get(ctx, XSettingSinceID)
	require.NoError(t, err)
	require.Empty(t, cursor)
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

	maxURL := "https://example.com/" + strings.Repeat("a", xMaxURLLength-len("https://example.com/"))
	overlongURL := maxURL + "a"
	tcoAtLimit := strings.Repeat("x", 240) + " https://t.co/" + strings.Repeat("a", xMaxTCOURLSlugLength)
	tcoOverLimit := strings.Repeat("x", 240) + " https://t.co/" + strings.Repeat("a", xMaxTCOURLSlugLength+1)
	overlongIDNALabel := strings.Repeat("x", 120) + " " + strings.Repeat("界", 80) + ".com"
	multiSuffixURL := strings.Repeat("x", 241) + " example.com.例.foo.org"
	idnaDomainWithExpandedLimit := strings.Repeat("界.", 500) + "com"
	unicodeURLOverEncodedLimit := "https://" + idnaDomainWithExpandedLimit
	unicodeURLOverEncodedLimit += "/" + strings.Repeat("a", xMaxURLLength-xUTF16Length(unicodeURLOverEncodedLimit)-1)
	encodedIDNADomain, err := idna.Punycode.ToASCII(idnaDomainWithExpandedLimit)
	require.NoError(t, err)
	require.LessOrEqual(t, xUTF16Length(unicodeURLOverEncodedLimit), xMaxURLLength)
	require.Greater(t, xUTF16Length(unicodeURLOverEncodedLimit)+xUTF16Length(encodedIDNADomain)-xUTF16Length(idnaDomainWithExpandedLimit), xMaxURLLength)
	overlongUnicodePrefixURL := strings.Repeat("界.", 2040) + "example.com"
	require.Greater(t, xUTF16Length(overlongUnicodePrefixURL)+xUTF16Length("https://"), xMaxURLLength)

	tests := []struct {
		name       string
		text       string
		shouldPost bool
	}{
		{name: "ascii at limit", text: strings.Repeat("x", 280), shouldPost: true},
		{name: "emoji at weighted limit", text: strings.Repeat("😀", 140), shouldPost: true},
		{name: "family emoji entities at weighted limit", text: strings.Repeat("👨‍⚕️", 140), shouldPost: true},
		{name: "flag emoji entities at weighted limit", text: strings.Repeat("🇺🇸", 140), shouldPost: true},
		{name: "keycap emoji entities at weighted limit", text: strings.Repeat("1️⃣", 140), shouldPost: true},
		{name: "emoji over limit", text: strings.Repeat("😀", 141), shouldPost: false},
		{name: "CJK over limit", text: strings.Repeat("界", 141), shouldPost: false},
		{name: "URL over limit despite rune count", text: strings.Repeat("x", 258) + " https://example.com", shouldPost: false},
		{name: "URL followed by CJK path text", text: strings.Repeat("x", 255) + " https://example.com/界", shouldPost: false},
		{name: "URL followed immediately by CJK text", text: strings.Repeat("x", 255) + " https://example.com界", shouldPost: false},
		{name: "URL query is a fixed-length entity", text: strings.Repeat("x", 257) + " https://x.co?x=y", shouldPost: false},
		{name: "URL fragment is ordinary text", text: strings.Repeat("x", 256) + " https://x.co#fragment", shouldPost: false},
		{name: "trailing URL punctuation is ordinary text", text: strings.Repeat("x", 256) + " https://x.co.", shouldPost: false},
		{name: "malformed URL-shaped text remains ordinary text", text: strings.Repeat("x", 258) + " https://not-a-url", shouldPost: true},
		{name: "pseudo-TLD is not a URL entity", text: strings.Repeat("x", 258) + " example.invalid", shouldPost: true},
		{name: "embedded email is not a URL entity", text: strings.Repeat("x", 258) + " hello@example.com", shouldPost: true},
		{name: "URL exactly at provider maximum", text: maxURL, shouldPost: true},
		{name: "URL over provider maximum", text: overlongURL, shouldPost: false},
		{name: "t.co slug at provider maximum", text: tcoAtLimit, shouldPost: true},
		{name: "t.co slug over provider maximum", text: tcoOverLimit, shouldPost: false},
		{name: "IDNA label over provider maximum", text: overlongIDNALabel, shouldPost: false},
		{name: "multiple URL entities separated by Unicode label", text: multiSuffixURL, shouldPost: false},
		{name: "Unicode URL exceeds provider maximum after Punycode expansion", text: unicodeURLOverEncodedLimit, shouldPost: false},
		{name: "overlong full protocol-less Unicode-prefixed URL", text: overlongUnicodePrefixURL, shouldPost: false},
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

func TestXIDNAPunycodeExpansionRejectsURLEntityAndTruncatesReply(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	domain := strings.Repeat("界.", 500) + "com"
	url := "https://" + domain
	url += "/" + strings.Repeat("a", xMaxURLLength-xUTF16Length(url)-1)
	encodedDomain, err := idna.Punycode.ToASCII(domain)
	require.NoError(t, err)
	require.Equal(t, xMaxURLLength, xUTF16Length(url))
	require.Greater(t, xUTF16Length(url)+xUTF16Length(encodedDomain)-xUTF16Length(domain), xMaxURLLength)

	require.Empty(t, xURLRanges(url))
	result := svc.SendOutboundMessage(ctx, "me", "", url)
	require.False(t, result.OK)
	require.Empty(t, api.posted)

	svc.SendReply(ctx, "tweet", url, "")
	require.Len(t, api.posted, 1)
	posted := strings.TrimPrefix(api.posted[0], "tweet|")
	require.NotEqual(t, url, posted)
	require.Contains(t, posted, "…")
	require.LessOrEqual(t, xWeightedPostLength(posted), xMaxWeightedPostLength)
}

func TestXReplyTruncatesToWeightedPostLimit(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	svc.SendReply(ctx, "tweet", strings.Repeat("😀", 141), "")
	require.Equal(t, []string{"tweet|" + strings.Repeat("😀", 139) + "…"}, api.posted)

	api.posted = nil
	svc.SendReply(ctx, "tweet", strings.Repeat("界", 141), "")
	require.Equal(t, []string{"tweet|" + strings.Repeat("界", 139) + "…"}, api.posted)

	api.posted = nil
	svc.SendReply(ctx, "tweet", strings.Repeat("x", 281), "")
	require.Equal(t, []string{"tweet|" + strings.Repeat("x", 278) + "…"}, api.posted)
}

func TestXWeightedLengthConformanceRegressions(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	directCases := []struct {
		name       string
		text       string
		shouldPost bool
	}{
		{name: "text presentation variation selector", text: strings.Repeat("✈︎", 94), shouldPost: false},
		{name: "unsupported arbitrary ZWJ sequence", text: strings.Repeat("😀\u200d😀", 57), shouldPost: false},
		{name: "NFC equivalent decomposed text", text: strings.Repeat("e\u0301", 141), shouldPost: true},
		{name: "valid bare IDN URL", text: strings.Repeat("x", 258) + " example.рф", shouldPost: false},
		{name: "valid bare URL after Unicode prefix", text: strings.Repeat("x", 258) + " 例.example.com", shouldPost: false},
		{name: "valid bare URL inside mixed Unicode label", text: strings.Repeat("x", 258) + " 例example.com", shouldPost: false},
		{name: "valid bare Unicode TLD URL inside mixed Unicode label", text: strings.Repeat("x", 258) + " 例example.рф", shouldPost: false},
		{name: "valid URL with inner ACE-prefixed label", text: strings.Repeat("x", 257) + " https://foo.xn--é.com", shouldPost: false},
		{name: "bare URL port is ordinary suffix", text: strings.Repeat("x", 251) + " example.com:12345", shouldPost: false},
		{name: "bare URL query is ordinary suffix", text: strings.Repeat("x", 249) + " example.com?foo=bar", shouldPost: false},
		{name: "mixed ACE-prefixed Unicode domain remains ordinary text", text: strings.Repeat("x", 257) + " https://xn--abc.界.com", shouldPost: true},
	}
	for _, tt := range directCases {
		t.Run(tt.name, func(t *testing.T) {
			api.posted = nil
			result := svc.SendOutboundMessage(ctx, "me", "", tt.text)
			if tt.shouldPost {
				require.True(t, result.OK)
				require.Equal(t, []string{"|" + tt.text}, api.posted)
				return
			}
			require.False(t, result.OK)
			require.Empty(t, api.posted)
		})
	}
}

func TestXEmojiRangesRespectVariationSelectorPresentation(t *testing.T) {
	require.Len(t, xEmojiRanges("✈"), 1)
	require.Empty(t, xEmojiRanges("✈︎"))
	ranges := xEmojiRanges("✈️")
	require.Len(t, ranges, 1)
	require.Equal(t, "✈️", "✈️"[ranges[0].start:ranges[0].end])
}

func TestXIDNASeparatorsRespectOuterASCIIDotLabelLimits(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	for _, tt := range []struct {
		name      string
		separator string
	}{
		{name: "ideographic full stop", separator: "\u3002"},
		{name: "fullwidth full stop", separator: "\uff0e"},
		{name: "halfwidth ideographic full stop", separator: "\uff61"},
	} {
		t.Run(tt.name+" valid within outer label", func(t *testing.T) {
			domain := "example" + tt.separator + "foo.com"
			url := "https://" + domain
			require.True(t, xURLIDNADomainValid(domain))
			ranges := xURLRanges(url)
			require.Len(t, ranges, 1)
			require.Equal(t, url, url[ranges[0].start:ranges[0].end])

			text := strings.Repeat("x", 256) + " " + url
			api.posted = nil
			result := svc.SendOutboundMessage(ctx, "me", "", text)
			require.True(t, result.OK)
			require.Equal(t, []string{"|" + text}, api.posted)
		})

		t.Run(tt.name+" preserves inner ACE-looking label", func(t *testing.T) {
			domain := "foo" + tt.separator + "xn--a.com"
			url := "https://" + domain
			require.True(t, xURLIDNADomainValid(domain))
			ranges := xURLRanges(url)
			require.Len(t, ranges, 1)
			require.Equal(t, url, url[ranges[0].start:ranges[0].end])

			text := strings.Repeat("x", 256) + " " + url
			api.posted = nil
			result := svc.SendOutboundMessage(ctx, "me", "", text)
			require.True(t, result.OK)
			require.Equal(t, []string{"|" + text}, api.posted)
		})

		t.Run(tt.name+" overlong outer label", func(t *testing.T) {
			domain := strings.Repeat("a"+tt.separator, 32) + "example.com"
			url := "https://" + domain
			require.False(t, xURLIDNADomainValid(domain))
			require.Empty(t, xURLRanges(url))

			text := strings.Repeat("x", 256) + " " + url
			api.posted = nil
			result := svc.SendOutboundMessage(ctx, "me", "", text)
			require.False(t, result.OK)
			require.Empty(t, api.posted)

			api.posted = nil
			svc.SendReply(ctx, "tweet", text, "")
			require.Len(t, api.posted, 1)
			posted := strings.TrimPrefix(api.posted[0], "tweet|")
			require.NotEqual(t, text, posted)
			require.Contains(t, posted, "…")
			require.LessOrEqual(t, xWeightedPostLength(posted), xMaxWeightedPostLength)
		})
	}
}

func TestXIDNASeparatorsPreserveEmptyInnerSegments(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	for _, tt := range []struct {
		name      string
		separator string
	}{
		{name: "ideographic full stop", separator: "\u3002"},
		{name: "fullwidth full stop", separator: "\uff0e"},
		{name: "halfwidth ideographic full stop", separator: "\uff61"},
	} {
		for _, variant := range []struct {
			name   string
			domain string
		}{
			{name: "leading", domain: tt.separator + "foo.com"},
			{name: "consecutive", domain: "foo" + tt.separator + tt.separator + "bar.com"},
			{name: "trailing", domain: "foo" + tt.separator + ".com"},
		} {
			t.Run(tt.name+" "+variant.name, func(t *testing.T) {
				url := "https://" + variant.domain
				require.True(t, xURLIDNADomainValid(variant.domain))
				ranges := xURLRanges(url)
				require.Len(t, ranges, 1)
				require.Equal(t, url, url[ranges[0].start:ranges[0].end])

				text := strings.Repeat("x", 254) + " " + url + "!!!"
				api.posted = nil
				result := svc.SendOutboundMessage(ctx, "me", "", text)
				require.False(t, result.OK)
				require.Empty(t, api.posted)

				api.posted = nil
				svc.SendReply(ctx, "tweet", text, "")
				require.Equal(t, []string{"tweet|" + strings.Repeat("x", 254) + " " + url + "…"}, api.posted)
				require.Equal(t, xMaxWeightedPostLength, xWeightedPostLength(strings.Repeat("x", 254)+" "+url+"…"))
			})
		}
	}
}

func TestXURLRangesFollowTwitterTextEntityBoundaries(t *testing.T) {
	maxURL := "https://example.com/" + strings.Repeat("a", xMaxURLLength-len("https://example.com/"))
	overlongURL := maxURL + "a"
	tcoAtLimit := "https://t.co/" + strings.Repeat("a", xMaxTCOURLSlugLength)
	tcoOverLimit := "https://t.co/" + strings.Repeat("a", xMaxTCOURLSlugLength+1)
	tcoWithExtraPath := tcoAtLimit + "/extra"
	overlongUnicodePrefixURL := strings.Repeat("界.", 2040) + "example.com"
	overlongInnerACEURL := "https://foo.xn--" + strings.Repeat("é", 100) + ".com"
	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{name: "internationalized domain", text: "example.рф", expected: []string{"example.рф"}},
		{name: "internationalized subdomain is not protocolless URL", text: "пример.рф", expected: []string{}},
		{name: "Unicode prefix before ASCII protocolless URL", text: "例.example.com", expected: []string{"example.com"}},
		{name: "ASCII URL inside mixed Unicode label", text: "例example.com", expected: []string{"example.com"}},
		{name: "Unicode TLD URL inside mixed Unicode label", text: "例example.рф", expected: []string{"example.рф"}},
		{name: "multiple ASCII URLs separated by Unicode label", text: "example.com.例.foo.org", expected: []string{"example.com", "foo.org"}},
		{name: "inner ACE-prefixed Unicode label", text: "https://foo.xn--é.com", expected: []string{"https://foo.xn--é.com"}},
		{name: "overlong inner ACE-prefixed Unicode label", text: overlongInnerACEURL, expected: []string{}},
		{name: "bare URL port is ordinary suffix", text: "example.com:12345", expected: []string{"example.com"}},
		{name: "bare URL port with path remains attached", text: "example.com:12345/path?foo=bar", expected: []string{"example.com:12345/path?foo=bar"}},
		{name: "bare URL query is ordinary suffix", text: "example.com?foo=bar", expected: []string{"example.com"}},
		{name: "bare URL query slash is ordinary suffix", text: "example.com?foo=/bar", expected: []string{"example.com"}},
		{name: "bare URL slash path and query remain attached", text: "example.com/path?foo=bar", expected: []string{"example.com/path?foo=bar"}},
		{name: "uppercase path and query", text: "HTTPS://EXAMPLE.COM/Path?X=Y", expected: []string{"HTTPS://EXAMPLE.COM/Path?X=Y"}},
		{name: "balanced path punctuation", text: "https://example.com/(foo).", expected: []string{"https://example.com/(foo)"}},
		{name: "slash before unsupported Unicode path", text: "https://example.com/界", expected: []string{"https://example.com/"}},
		{name: "fragment after authority", text: "https://example.com#fragment", expected: []string{"https://example.com"}},
		{name: "trailing punctuation", text: "https://example.com/path).", expected: []string{"https://example.com/path"}},
		{name: "email address", text: "hello@example.com", expected: []string{}},
		{name: "pseudo-TLD", text: "example.invalid", expected: []string{}},
		{name: "URL after slash", text: "foo/example.com", expected: []string{}},
		{name: "URL after hyphen", text: "-example.com", expected: []string{}},
		{name: "malformed protocol URL", text: "https://not-a-url", expected: []string{}},
		{name: "mixed ACE-prefixed Unicode domain", text: "https://xn--abc.界.com", expected: []string{}},
		{name: "URL exactly at provider maximum", text: maxURL, expected: []string{maxURL}},
		{name: "URL over provider maximum", text: overlongURL, expected: []string{}},
		{name: "t.co slug exactly at maximum", text: tcoAtLimit, expected: []string{tcoAtLimit}},
		{name: "t.co slug over maximum", text: tcoOverLimit, expected: []string{}},
		{name: "t.co path after slug is ordinary text", text: tcoWithExtraPath, expected: []string{tcoAtLimit}},
		{name: "overlong full protocol-less Unicode-prefixed URL", text: overlongUnicodePrefixURL, expected: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]string, 0, len(tt.expected))
			for _, url := range xURLRanges(tt.text) {
				got = append(got, tt.text[url.start:url.end])
			}
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestXReplyConformanceTruncatesEntitiesWithoutProviderRejection(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)
	multiSuffixURL := strings.Repeat("x", 241) + " example.com.例.foo.org"

	tests := []struct {
		name     string
		text     string
		expected string
	}{
		{name: "text presentation variation selector", text: strings.Repeat("✈︎", 94), expected: strings.Repeat("✈︎", 69) + "…"},
		{name: "unsupported arbitrary ZWJ sequence", text: strings.Repeat("😀\u200d😀", 57), expected: strings.Repeat("😀\u200d😀", 55) + "…"},
		{name: "CJK text", text: strings.Repeat("界", 141), expected: strings.Repeat("界", 139) + "…"}, {name: "URL entity", text: strings.Repeat("x", 258) + " example.рф", expected: strings.Repeat("x", 258) + " …"},
		{name: "URL after Unicode prefix", text: strings.Repeat("x", 258) + " 例.example.com", expected: strings.Repeat("x", 258) + " 例.…"},
		{name: "URL inside mixed Unicode label", text: strings.Repeat("x", 258) + " 例example.com", expected: strings.Repeat("x", 258) + " 例…"},
		{name: "multiple URL entities separated by Unicode label", text: multiSuffixURL, expected: strings.Repeat("x", 241) + " example.com.例.…"},
		{name: "URL followed by CJK text", text: strings.Repeat("x", 255) + " https://example.com/界", expected: strings.Repeat("x", 255) + " …"},
		{name: "decomposed text preserves original prefix boundary", text: strings.Repeat("e\u0301", 281), expected: strings.Repeat("e\u0301", 278) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api.posted = nil
			svc.SendReply(ctx, "tweet", tt.text, "")
			require.Equal(t, []string{"tweet|" + tt.expected}, api.posted)
			require.LessOrEqual(t, xWeightedPostLength(tt.expected), xMaxWeightedPostLength)
		})
	}
}

func TestXReplyRejectsOversizedURLEntitiesBeforeProviderPost(t *testing.T) {
	ctx, svc, _, _, _, _, _ := setupXServiceTest(t)
	api := &fakeXAPI{}
	svc.setAPI(api)

	maxURL := "https://example.com/" + strings.Repeat("a", xMaxURLLength-len("https://example.com/"))
	overlongURL := maxURL + "a"
	tcoOverLimit := "https://t.co/" + strings.Repeat("a", xMaxTCOURLSlugLength+1)
	overlongUnicodePrefixURL := strings.Repeat("界.", 2040) + "example.com"
	for _, text := range []string{
		overlongURL,
		strings.Repeat("x", 240) + " " + tcoOverLimit,
		overlongUnicodePrefixURL,
		strings.Repeat("x", 258) + " 例example.рф",
		strings.Repeat("x", 257) + " https://foo.xn--é.com",
		strings.Repeat("x", 251) + " example.com:12345",
		strings.Repeat("x", 249) + " example.com?foo=bar",
	} {
		api.posted = nil
		svc.SendReply(ctx, "tweet", text, "")
		require.Len(t, api.posted, 1)
		posted := strings.TrimPrefix(api.posted[0], "tweet|")
		require.NotEqual(t, text, posted)
		require.LessOrEqual(t, xWeightedPostLength(posted), xMaxWeightedPostLength)
	}
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

func TestXRuntimeCreateSwarmTaskPreservesReplyContext(t *testing.T) {
	ctx, svc, _, _, _, project, _ := setupXServiceTest(t)
	taskSvc := NewTaskService(svc.taskRepo, nil, nil)
	swarmSvc := NewSwarmService(taskSvc, svc.taskRepo, nil, nil)
	taskSvc.SetSwarmService(swarmSvc)
	svc.taskSvc = taskSvc

	runtime := svc.RuntimeTools("parent-task", project.ID, "bot", "author", "conversation", "source-tweet", "alice")
	output, handled, isError, err := runtime.Executor(ctx, "create_swarm_task", json.RawMessage(`{"title":"X swarm","prompt":"preserve the destination","category":"backlog"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "X swarm")

	tasks, err := svc.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, models.TaskOriginX, tasks[0].CreatedVia)
	meta, err := svc.taskContextRepo.GetByTaskID(ctx, tasks[0].ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, project.ID, meta.ProjectID)
	require.Equal(t, "bot", meta.AccountID)
	require.Equal(t, "conversation", meta.ConversationID)
	require.Equal(t, "source-tweet", meta.ReplyToTweetID)
	require.Equal(t, "author", meta.XUserID)
	require.Equal(t, "alice", meta.Username)
}

func TestXRuntimeCreateTaskPersistsReplyContextBeforeWorkerSubmission(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "X runtime project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, XSettingSendResponses, "true"))
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{
		Name:           "X auto-start test model",
		Provider:       models.ProviderTest,
		Model:          "test",
		IsDefault:      true,
		AutoStartTasks: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	taskRepo := repository.NewTaskRepo(db, nil)
	worker := NewWorkerService(nil, 1, projectRepo)
	worker.SetTaskRepo(taskRepo)
	worker.submitted = make(chan models.Task)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), worker)
	xTaskContextRepo := repository.NewXTaskContextRepo(db)
	xSvc := NewXService(
		XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"},
		settingsRepo,
		projectRepo,
		llmConfigRepo,
		taskRepo,
		repository.NewExecutionRepo(db),
		repository.NewScheduleRepo(db),
		taskSvc,
	)
	xSvc.SetRepositories(repository.NewXAuthRepo(db), repository.NewXUserProjectRepo(db), xTaskContextRepo, repository.NewXInboundReceiptRepo(db), repository.NewThreadInputRepo(db))
	xSvc.me = XUser{ID: "bot", Username: "openvibely"}
	xSvc.setAPI(&fakeXAPI{me: xSvc.me})

	runtime := xSvc.RuntimeTools("parent-task", project.ID, "bot", "author", "conversation", "source-tweet", "alice")
	type actionResult struct {
		output  string
		handled bool
		isError bool
		err     error
	}
	resultCh := make(chan actionResult, 1)
	go func() {
		output, handled, isError, err := runtime.Executor(ctx, "create_task", json.RawMessage(`{"title":"Child task","prompt":"complete immediately"}`))
		resultCh <- actionResult{output: output, handled: handled, isError: isError, err: err}
	}()

	var submitted models.Task
	select {
	case submitted = <-worker.Submitted():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime task submission")
	}
	meta, err := xTaskContextRepo.GetByTaskID(ctx, submitted.ID)
	require.NoError(t, err)
	require.NotNil(t, meta, "X reply context must exist before worker submission")
	require.Equal(t, project.ID, meta.ProjectID)
	require.Equal(t, "bot", meta.AccountID)
	require.Equal(t, "conversation", meta.ConversationID)
	require.Equal(t, "source-tweet", meta.ReplyToTweetID)
	require.Equal(t, "author", meta.XUserID)
	require.Equal(t, "alice", meta.Username)
	stored, err := taskRepo.GetByID(ctx, submitted.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, models.TaskOriginX, stored.CreatedVia)

	select {
	case result := <-resultCh:
		require.True(t, result.handled)
		require.False(t, result.isError)
		require.NoError(t, result.err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for create_task result")
	}
}

type xRuntimeWorkerFixture struct {
	ctx              context.Context
	db               *sql.DB
	project          *models.Project
	taskRepo         *repository.TaskRepo
	xTaskContextRepo *repository.XTaskContextRepo
	worker           *WorkerService
	xSvc             *XService
	api              *fakeXAPI
	mockLLM          *testutil.MockLLMCaller
}

func newXRuntimeWorkerFixture(t *testing.T) *xRuntimeWorkerFixture {
	t.Helper()
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "X runtime worker project"}
	require.NoError(t, projectRepo.Create(ctx, project))

	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, XSettingSendResponses, "true"))
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	agent := &models.LLMConfig{
		Name:           "X auto-start worker model",
		Provider:       models.ProviderTest,
		Model:          "test",
		IsDefault:      true,
		AutoStartTasks: true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mockLLM := testutil.NewMockLLMCaller()
	mockLLM.Response = "child completed"
	mockLLM.TextOnly = mockLLM.Response
	llmSvc.SetLLMCaller(mockLLM)

	worker := NewWorkerService(llmSvc, 1, projectRepo)
	worker.SetTaskRepo(taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetExecutionRepo(execRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, worker)
	llmSvc.SetTaskService(taskSvc)

	xTaskContextRepo := repository.NewXTaskContextRepo(db)
	xSvc := NewXService(
		XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"},
		settingsRepo,
		projectRepo,
		llmConfigRepo,
		taskRepo,
		execRepo,
		scheduleRepo,
		taskSvc,
	)
	xSvc.SetRepositories(repository.NewXAuthRepo(db), repository.NewXUserProjectRepo(db), xTaskContextRepo, repository.NewXInboundReceiptRepo(db), repository.NewThreadInputRepo(db))
	xSvc.me = XUser{ID: "bot", Username: "openvibely"}
	api := &fakeXAPI{me: xSvc.me}
	xSvc.setAPI(api)
	llmSvc.SetXService(xSvc)

	worker.Start(ctx)
	t.Cleanup(worker.Stop)
	return &xRuntimeWorkerFixture{
		ctx:              ctx,
		db:               db,
		project:          project,
		taskRepo:         taskRepo,
		xTaskContextRepo: xTaskContextRepo,
		worker:           worker,
		xSvc:             xSvc,
		api:              api,
		mockLLM:          mockLLM,
	}
}

func TestXRuntimeCreateTaskCompletesWithExactlyOneReply(t *testing.T) {
	fixture := newXRuntimeWorkerFixture(t)
	runtime := fixture.xSvc.RuntimeTools("parent-task", fixture.project.ID, "bot", "author", "conversation", "source-tweet", "alice")

	output, handled, isError, err := runtime.Executor(fixture.ctx, "create_task", json.RawMessage(`{"title":"Child task","prompt":"complete immediately"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "[TASK_ID:")

	var completed *models.Task
	require.Eventually(t, func() bool {
		tasks, listErr := fixture.taskRepo.ListByProject(fixture.ctx, fixture.project.ID, "")
		if listErr != nil || len(tasks) != 1 {
			return false
		}
		if tasks[0].Status != models.StatusCompleted {
			return false
		}
		completed = &tasks[0]
		return true
	}, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, completed)
	require.Equal(t, models.TaskOriginX, completed.CreatedVia)

	meta, err := fixture.xTaskContextRepo.GetByTaskID(fixture.ctx, completed.ID)
	require.NoError(t, err)
	require.Equal(t, &models.XTaskContext{
		TaskID:         completed.ID,
		ProjectID:      fixture.project.ID,
		AccountID:      "bot",
		ConversationID: "conversation",
		ReplyToTweetID: "source-tweet",
		XUserID:        "author",
		Username:       "alice",
	}, &models.XTaskContext{
		TaskID:         meta.TaskID,
		ProjectID:      meta.ProjectID,
		AccountID:      meta.AccountID,
		ConversationID: meta.ConversationID,
		ReplyToTweetID: meta.ReplyToTweetID,
		XUserID:        meta.XUserID,
		Username:       meta.Username,
	})
	require.Eventually(t, func() bool {
		return len(fixture.api.Posts()) == 1
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"source-tweet|child completed"}, fixture.api.Posts())
	require.Equal(t, 1, fixture.mockLLM.CallCount())
}

func TestXRuntimeCreateTaskMetadataFailureDoesNotAdmitTask(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "origin", query: `CREATE TRIGGER fail_x_origin BEFORE UPDATE OF created_via ON tasks WHEN NEW.created_via = 'x' BEGIN SELECT RAISE(ABORT, 'forced X origin failure'); END`},
		{name: "context", query: `CREATE TRIGGER fail_x_context BEFORE INSERT ON x_task_context BEGIN SELECT RAISE(ABORT, 'forced X context failure'); END`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newXRuntimeWorkerFixture(t)
			_, err := fixture.db.ExecContext(fixture.ctx, tt.query)
			require.NoError(t, err)

			runtime := fixture.xSvc.RuntimeTools("parent-task", fixture.project.ID, "bot", "author", "conversation", "source-tweet", "alice")
			output, handled, isError, actionErr := runtime.Executor(fixture.ctx, "create_task", json.RawMessage(`{"title":"Child task","prompt":"must not run"}`))
			require.True(t, handled)
			require.True(t, isError)
			require.Error(t, actionErr)
			require.Contains(t, output, "Failed to create 1 task(s)")
			require.Contains(t, actionErr.Error(), "no tasks were persisted")

			tasks, err := fixture.taskRepo.ListByProject(fixture.ctx, fixture.project.ID, "")
			require.NoError(t, err)
			require.Empty(t, tasks)
			require.Equal(t, 0, fixture.worker.QueueSize())
			require.Equal(t, 0, fixture.worker.TotalRunning())
			require.Empty(t, fixture.api.Posts())
			require.Equal(t, 0, fixture.mockLLM.CallCount())
			select {
			case submitted := <-fixture.worker.Submitted():
				t.Fatalf("metadata failure submitted task %s", submitted.ID)
			default:
			}
		})
	}
}

func TestXRuntimeCreateTaskExplicitBacklogDoesNotAutoStart(t *testing.T) {
	fixture := newXRuntimeWorkerFixture(t)
	runtime := fixture.xSvc.RuntimeTools("parent-task", fixture.project.ID, "bot", "author", "conversation", "source-tweet", "alice")

	output, handled, isError, err := runtime.Executor(fixture.ctx, "create_task", json.RawMessage(`{"title":"Backlog task","prompt":"wait for a later start","category":"backlog"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "[TASK_ID:")

	tasks, err := fixture.taskRepo.ListByProject(fixture.ctx, fixture.project.ID, "")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, models.CategoryBacklog, tasks[0].Category)
	require.Equal(t, models.TaskOriginX, tasks[0].CreatedVia)
	meta, err := fixture.xTaskContextRepo.GetByTaskID(fixture.ctx, tasks[0].ID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	require.Equal(t, "source-tweet", meta.ReplyToTweetID)
	require.Equal(t, 0, fixture.worker.QueueSize())
	require.Equal(t, 0, fixture.worker.TotalRunning())
	require.Empty(t, fixture.api.Posts())
	require.Equal(t, 0, fixture.mockLLM.CallCount())
	select {
	case submitted := <-fixture.worker.Submitted():
		t.Fatalf("explicit backlog task %s was submitted", submitted.ID)
	default:
	}
}
