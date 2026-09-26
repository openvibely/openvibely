package handler

import (
	"database/sql"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/update"
)

// testHandlerEnv is a fully wired test handler plus the repositories and services it uses,
// so tests can reach the same instances the handler holds.
type testHandlerEnv struct {
	DB      *sql.DB
	Handler *Handler
	Echo    *echo.Echo
	LLM     *testutil.MockLLMCaller

	ProjectRepo    *repository.ProjectRepo
	TaskRepo       *repository.TaskRepo
	LLMConfigRepo  *repository.LLMConfigRepo
	ExecRepo       *repository.ExecutionRepo
	ScheduleRepo   *repository.ScheduleRepo
	WorkerRepo     *repository.WorkerRepo
	AttachmentRepo *repository.AttachmentRepo
	AlertRepo      *repository.AlertRepo
	SettingsRepo   *repository.SettingsRepo

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

	oldUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = oldUploadsDir })

	env := &testHandlerEnv{
		DB:             db,
		LLM:            testutil.NewMockLLMCaller(),
		ProjectRepo:    repository.NewProjectRepo(db),
		TaskRepo:       repository.NewTaskRepo(db, o.broadcaster),
		LLMConfigRepo:  repository.NewLLMConfigRepo(db),
		ExecRepo:       repository.NewExecutionRepo(db),
		ScheduleRepo:   repository.NewScheduleRepo(db),
		WorkerRepo:     repository.NewWorkerRepo(db),
		AttachmentRepo: repository.NewAttachmentRepo(db),
		AlertRepo:      repository.NewAlertRepo(db),
		SettingsRepo:   repository.NewSettingsRepo(db),
	}
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	upcomingRepo := repository.NewUpcomingRepo(db)

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
		env.TaskGoals = service.NewTaskGoalService(repository.NewTaskGoalRepo(db), env.TaskRepo, nil)
		env.TaskSvc.SetTaskGoalService(env.TaskGoals)
		env.WorkerSvc.SetTaskGoalService(env.TaskGoals)
	}
	var insightsSvc *service.InsightsService
	if o.insights {
		insightsSvc = service.NewInsightsService(repository.NewInsightsRepo(db), env.TaskRepo, env.ProjectRepo, env.LLMConfigRepo, env.ExecRepo)
		insightsSvc.SetLLMService(env.LLMSvc)
	}

	h := New(env.ProjectSvc, env.TaskSvc, env.LLMSvc, env.WorkerSvc, schedulerSvc, alertSvc, upcomingSvc, insightsSvc,
		env.LLMConfigRepo, env.TaskRepo, env.ScheduleRepo, env.ExecRepo, env.WorkerRepo, env.AttachmentRepo, chatAttachmentRepo,
		env.ProjectRepo, env.SettingsRepo, o.broadcaster, nil)
	h.oauthIdentityResolver = nil
	h.SetGitHubAuthRepo(repository.NewGitHubAuthRepo(db))
	h.SetSlackAuthRepo(repository.NewSlackAuthRepo(db))
	h.SetEmailAuthRepo(repository.NewEmailAuthRepo(db))
	h.SetEmailTaskContextRepo(repository.NewEmailTaskContextRepo(db))
	h.SetDiscordAuthRepo(repository.NewDiscordAuthRepo(db))
	h.SetDiscordTaskContextRepo(repository.NewDiscordTaskContextRepo(db))
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
