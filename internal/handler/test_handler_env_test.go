package handler

import (
	"database/sql"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/update"
)

// testHandlerEnv is a fully wired test handler plus the repositories and services it uses,
// so tests can reach the same instances the handler holds.
type testHandlerEnv struct {
	DB      *sql.DB
	Handler *Handler
	Echo    *echo.Echo
	LLM     *testutil.MockLLMCaller

	ProjectRepo    persistence.ProjectStore
	TaskRepo       persistence.TaskStore
	LLMConfigRepo  persistence.LLMConfigStore
	ExecRepo       persistence.ExecutionStore
	ScheduleRepo   persistence.ScheduleStore
	WorkerRepo     persistence.WorkerStore
	AttachmentRepo persistence.AttachmentStore
	AlertRepo      persistence.AlertStore
	SettingsRepo   persistence.SettingsStore

	ProjectSvc *service.ProjectService
	LLMSvc     *service.LLMService
	WorkerSvc  *service.WorkerService
	TaskSvc    *service.TaskService
	TaskGoals  *service.TaskGoalService
}

type testHandlerOptions struct {
	broadcaster *events.Broadcaster
	taskGoals   bool
	insights    bool
}

type testHandlerOption func(*testHandlerOptions)

func withTestBroadcaster(b *events.Broadcaster) testHandlerOption {
	return func(o *testHandlerOptions) { o.broadcaster = b }
}

func withTestTaskGoals() testHandlerOption {
	return func(o *testHandlerOptions) { o.taskGoals = true }
}

func withTestInsights() testHandlerOption {
	return func(o *testHandlerOptions) { o.insights = true }
}

// newTestHandlerEnv is the single place handler tests build a fully wired Handler. It also
// waits for tracked background turns before the test's database closes.
func newTestHandlerEnv(t testing.TB, db *sql.DB, opts ...testHandlerOption) *testHandlerEnv {
	t.Helper()
	var o testHandlerOptions
	for _, opt := range opts {
		opt(&o)
	}

	env := wireTestHandler(t, teststores.Of(db, teststores.WithBroadcaster(o.broadcaster)), o)
	env.DB = db
	return env
}

func setupTestHandlerWithStores(t testing.TB, options ...teststores.Option) (*Handler, *echo.Echo, persistence.LLMConfigStore, *teststores.Env) {
	t.Helper()
	stores := teststores.New(t, options...)
	env := wireTestHandler(t, stores.Stores, testHandlerOptions{})
	return env.Handler, env.Echo, env.LLMConfigRepo, stores
}

func wireTestHandler(t testing.TB, stores persistence.Stores, o testHandlerOptions) *testHandlerEnv {
	t.Helper()
	oldUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = oldUploadsDir })

	env := &testHandlerEnv{
		LLM:            testutil.NewMockLLMCaller(),
		ProjectRepo:    stores.Projects,
		TaskRepo:       stores.Tasks,
		LLMConfigRepo:  stores.LLMConfigs,
		ExecRepo:       stores.Executions,
		ScheduleRepo:   stores.Schedules,
		WorkerRepo:     stores.Workers,
		AttachmentRepo: stores.Attachments,
		AlertRepo:      stores.Alerts,
		SettingsRepo:   stores.Settings,
	}
	chatAttachmentRepo := stores.ChatAttachments
	upcomingRepo := stores.Upcoming

	env.ProjectSvc = service.NewProjectService(env.ProjectRepo)
	env.LLMSvc = service.NewLLMService(env.LLMConfigRepo, env.ExecRepo, env.TaskRepo, env.ProjectRepo, env.ScheduleRepo, env.AttachmentRepo)
	env.LLMSvc.SetLLMCaller(env.LLM)
	env.WorkerSvc = service.NewWorkerService(env.LLMSvc, 0, nil)
	env.TaskSvc = service.NewTaskService(env.TaskRepo, env.AttachmentRepo, env.WorkerSvc)
	env.TaskSvc.SetDeletionUploadsDir(uploadsDir)
	schedulerSvc := service.NewSchedulerService(env.ScheduleRepo, env.TaskRepo, env.WorkerSvc)
	alertSvc := service.NewAlertService(env.AlertRepo, nil)
	upcomingSvc := service.NewUpcomingService(upcomingRepo)
	if o.taskGoals {
		env.TaskGoals = service.NewTaskGoalService(stores.TaskGoals, env.TaskRepo, nil)
		env.TaskSvc.SetTaskGoalService(env.TaskGoals)
		env.WorkerSvc.SetTaskGoalService(env.TaskGoals)
	}
	var insightsSvc *service.InsightsService
	if o.insights {
		insightsSvc = service.NewInsightsService(stores.Insights, env.TaskRepo, env.ProjectRepo, env.LLMConfigRepo, env.ExecRepo)
		insightsSvc.SetLLMService(env.LLMSvc)
	}

	h := New(env.ProjectSvc, env.TaskSvc, env.LLMSvc, env.WorkerSvc, schedulerSvc, alertSvc, upcomingSvc, insightsSvc,
		env.LLMConfigRepo, env.TaskRepo, env.ScheduleRepo, env.ExecRepo, env.WorkerRepo, env.AttachmentRepo, chatAttachmentRepo,
		env.ProjectRepo, env.SettingsRepo, o.broadcaster, nil, runtimeStoresForTests(stores))
	h.oauthIdentityResolver = nil
	h.SetGitHubAuthRepo(stores.GitHubAuth)
	h.SetSlackAuthRepo(stores.SlackAuth)
	h.SetEmailAuthRepo(stores.EmailAuth)
	h.SetEmailTaskContextRepo(stores.EmailTaskContexts)
	h.SetDiscordAuthRepo(stores.DiscordAuth)
	h.SetDiscordTaskContextRepo(stores.DiscordTaskContexts)
	h.SetLocalRepoPathEnabled(true)
	if env.TaskGoals != nil {
		h.SetTaskGoalService(env.TaskGoals)
		env.WorkerSvc.SetAfterCompleteRuntimeToolProvider(h.GoalAgentAfterCompleteRuntimeTools)
	}
	// Turns run on background goroutines; wait for them before the test's database closes so
	// no test leaves work running into the next one.
	h.SetUpdateWorkTracker(update.NewWorkTracker())
	t.Cleanup(func() { waitForHandlerBackgroundWork(t, h) })

	env.Handler = h
	env.Echo = echo.New()
	h.RegisterRoutes(env.Echo)
	return env
}

func waitForHandlerBackgroundWork(t testing.TB, h *Handler) {
	t.Helper()
	tracker := h.updateWorkTracker
	if tracker == nil {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		active := tracker.Active()
		if active == (update.ActiveWork{}) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("background work still running after test: %+v", active)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newTestRuntimeStores(db *sql.DB) RuntimeStores {
	return runtimeStoresForTests(teststores.Of(db))
}

func runtimeStoresForTests(stores persistence.Stores) RuntimeStores {
	return RuntimeStores{
		ThreadInputs:      stores.ThreadInputs,
		Usage:             stores.Usage,
		SkillAnalytics:    stores.SkillAnalytics,
		TaskCommitStats:   stores.TaskCommitStats,
		ChatInputRequests: stores.ChatInputRequests,
	}
}
