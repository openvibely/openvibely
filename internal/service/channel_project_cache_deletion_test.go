package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelProjectSelectionCacheMissCannotRepopulateDeletedProject(t *testing.T) {
	t.Run("slack", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Slack B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		userProjectRepo := repository.NewSlackUserProjectRepo(db)
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "T-RACE", "U-RACE", projectB.ID))

		svc := NewSlackService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, userProjectRepo, nil, nil)
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan struct {
			projectID string
			err       error
		}, 1)
		go func() {
			projectID, resolveErr := svc.getActiveProject(ctx, "T-RACE", "U-RACE")
			resolved <- struct {
				projectID string
				err       error
			}{projectID, resolveErr}
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Slack cache miss did not reach population barrier")
		}
		require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
		close(releasePopulate)
		select {
		case result := <-resolved:
			require.NoError(t, result.err)
			require.Equal(t, projectA.ID, result.projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Slack cache miss did not resolve after deletion")
		}
	})

	t.Run("discord", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Discord B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		userProjectRepo := repository.NewDiscordUserProjectRepo(db)
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "U-RACE", projectB.ID))

		svc := NewDiscordService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		svc.SetDiscordUserProjectRepo(userProjectRepo)
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan string, 1)
		go func() {
			resolved <- svc.getActiveProject(ctx, "U-RACE")
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Discord cache miss did not reach population barrier")
		}
		require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
		close(releasePopulate)
		select {
		case projectID := <-resolved:
			require.Equal(t, projectA.ID, projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Discord cache miss did not resolve after deletion")
		}
	})

	t.Run("telegram", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Telegram B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		userProjectRepo := repository.NewTelegramUserProjectRepo(db)
		const userID int64 = 11841184
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "11841184", projectB.ID))

		svc := &TelegramService{
			projectRepo:             projectRepo,
			telegramUserProjectRepo: userProjectRepo,
			userProjects:            make(map[int64]string),
			userProjectVersions:     make(map[int64]uint64),
		}
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan string, 1)
		go func() {
			resolved <- svc.getActiveProject(userID)
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Telegram cache miss did not reach population barrier")
		}
		require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
		close(releasePopulate)
		select {
		case projectID := <-resolved:
			require.Equal(t, projectA.ID, projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Telegram cache miss did not resolve after deletion")
		}
	})
}

func TestChannelProjectSelectionCacheMissCannotOverwriteSuccessfulSwitch(t *testing.T) {
	t.Run("slack", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Slack B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		projectC := &models.Project{Name: "Concurrent Slack C"}
		require.NoError(t, projectRepo.Create(ctx, projectC))
		userProjectRepo := repository.NewSlackUserProjectRepo(db)
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "T-SWITCH", "U-SWITCH", projectB.ID))

		svc := NewSlackService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, userProjectRepo, nil, nil)
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan struct {
			projectID string
			err       error
		}, 1)
		go func() {
			projectID, resolveErr := svc.getActiveProject(ctx, "T-SWITCH", "U-SWITCH")
			resolved <- struct {
				projectID string
				err       error
			}{projectID, resolveErr}
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Slack cache miss did not reach population barrier")
		}
		require.NoError(t, svc.setActiveProject(ctx, "T-SWITCH", "U-SWITCH", projectC.ID))
		close(releasePopulate)
		select {
		case result := <-resolved:
			require.NoError(t, result.err)
			require.Equal(t, projectC.ID, result.projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Slack cache miss did not resolve after switch")
		}
	})

	t.Run("discord", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Discord B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		projectC := &models.Project{Name: "Concurrent Discord C"}
		require.NoError(t, projectRepo.Create(ctx, projectC))
		userProjectRepo := repository.NewDiscordUserProjectRepo(db)
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "U-SWITCH", projectB.ID))

		svc := NewDiscordService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		svc.SetDiscordUserProjectRepo(userProjectRepo)
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan string, 1)
		go func() {
			resolved <- svc.getActiveProject(ctx, "U-SWITCH")
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Discord cache miss did not reach population barrier")
		}
		require.NoError(t, svc.setActiveProject(ctx, "U-SWITCH", projectC.ID))
		close(releasePopulate)
		select {
		case projectID := <-resolved:
			require.Equal(t, projectC.ID, projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Discord cache miss did not resolve after switch")
		}
	})

	t.Run("telegram", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		projectRepo := repository.NewProjectRepo(db)
		projectA, err := projectRepo.GetByID(ctx, "default")
		require.NoError(t, err)
		require.NotNil(t, projectA)
		projectB := &models.Project{Name: "Concurrent Telegram B"}
		require.NoError(t, projectRepo.Create(ctx, projectB))
		projectC := &models.Project{Name: "Concurrent Telegram C"}
		require.NoError(t, projectRepo.Create(ctx, projectC))
		userProjectRepo := repository.NewTelegramUserProjectRepo(db)
		const userID int64 = 11841185
		require.NoError(t, userProjectRepo.SetUserProject(ctx, "11841185", projectB.ID))

		svc := &TelegramService{
			projectRepo:             projectRepo,
			telegramUserProjectRepo: userProjectRepo,
			userProjects:            make(map[int64]string),
			userProjectVersions:     make(map[int64]uint64),
		}
		projectSvc := NewProjectService(projectRepo)
		svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
		populateStarted := make(chan struct{})
		releasePopulate := make(chan struct{})
		var populateOnce sync.Once
		svc.beforeActiveProjectCachePopulateHook = func() {
			populateOnce.Do(func() {
				close(populateStarted)
				<-releasePopulate
			})
		}
		resolved := make(chan string, 1)
		go func() {
			resolved <- svc.getActiveProject(userID)
		}()
		select {
		case <-populateStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("Telegram cache miss did not reach population barrier")
		}
		require.NoError(t, svc.setTelegramActiveProject(ctx, userID, projectC.ID))
		close(releasePopulate)
		select {
		case projectID := <-resolved:
			require.Equal(t, projectC.ID, projectID)
		case <-time.After(2 * time.Second):
			t.Fatal("Telegram cache miss did not resolve after switch")
		}
	})
}

func TestChannelProjectSelectionInvalidationPreservesUnrelatedSelections(t *testing.T) {
	slack := &SlackService{userProjects: map[string]string{"T:U-A": "project-a", "T:U-B": "project-b"}}
	slack.InvalidateProjectSelection("project-b")
	require.Equal(t, "project-a", slack.userProjects["T:U-A"])
	require.NotContains(t, slack.userProjects, "T:U-B")

	discord := &DiscordService{userProjects: map[string]string{"U-A": "project-a", "U-B": "project-b"}}
	discord.InvalidateProjectSelection("project-b")
	require.Equal(t, "project-a", discord.userProjects["U-A"])
	require.NotContains(t, discord.userProjects, "U-B")

	telegram := &TelegramService{
		userProjects:        map[int64]string{1: "project-a", 2: "project-b"},
		userProjectVersions: map[int64]uint64{1: 4, 2: 7},
	}
	telegram.InvalidateProjectSelection("project-b")
	require.Equal(t, "project-a", telegram.userProjects[1])
	require.NotContains(t, telegram.userProjects, int64(2))
	require.Equal(t, uint64(4), telegram.userProjectVersions[1])
	require.Equal(t, uint64(8), telegram.userProjectVersions[2], "Telegram eviction must fence in-flight cache population")
}

func TestSlackService_LiveDeletedSelectedProjectFallsBackWithoutRestart(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectA, err := projectRepo.GetByID(ctx, "default")
	require.NoError(t, err)
	require.NotNil(t, projectA)
	projectB := &models.Project{Name: "Selected B"}
	require.NoError(t, projectRepo.Create(ctx, projectB))

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	authRepo := repository.NewSlackAuthRepo(db)
	for _, project := range []*models.Project{projectA, projectB} {
		require.NoError(t, authRepo.Create(ctx, &models.SlackAuthorizedUser{
			ProjectID: project.ID, SlackUserID: "U1184", DisplayName: "Issue 1184", AddedBy: "test",
		}))
	}
	userProjectRepo := repository.NewSlackUserProjectRepo(db)
	svc := NewSlackService(repository.NewSettingsRepo(db), projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, userProjectRepo, repository.NewSlackTaskContextRepo(db), authRepo)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(taskSvc)
	svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
	require.NoError(t, svc.setActiveProject(ctx, "T1184", "U1184", projectB.ID))

	require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
	_, cached := svc.userProjects[slackUserProjectKey("T1184", "U1184")]
	require.False(t, cached, "successful deletion must evict Slack's deleted project cache entry")
	selected, err := userProjectRepo.GetUserProject(ctx, "T1184", "U1184")
	require.NoError(t, err)
	require.Empty(t, selected, "project deletion must cascade the durable selector")

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, request ChannelChatRunRequest) { got = &request })
	svc.postMessageFn = func(string, string, string) (string, error) { return "", nil }
	svc.processIncomingMessage(slackIncomingMessage{
		TeamID: "T1184", ChannelID: "C1184", ThreadTS: "1710000000.1184", UserID: "U1184", Text: "continue after delete", Source: models.TaskOriginSlack,
	})
	require.NotNil(t, got)
	require.Equal(t, projectA.ID, got.ProjectID, "Slack must fall back to the live default project")
	require.NotEqual(t, projectB.ID, got.ProjectID)

	tasksA, err := taskRepo.ListByProject(ctx, projectA.ID, "")
	require.NoError(t, err)
	require.Len(t, tasksA, 1)
	tasksB, err := taskRepo.ListByProject(ctx, projectB.ID, "")
	require.NoError(t, err)
	require.Empty(t, tasksB, "deleted project must not receive a task")

	fresh := NewSlackService(repository.NewSettingsRepo(db), projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, userProjectRepo, repository.NewSlackTaskContextRepo(db), authRepo)
	freshProject, err := fresh.getActiveProject(ctx, "T1184", "U1184")
	require.NoError(t, err)
	require.Equal(t, projectA.ID, freshProject, "fresh services must retain the existing fallback")

	projectC := &models.Project{Name: "Later C"}
	require.NoError(t, projectRepo.Create(ctx, projectC))
	require.NoError(t, authRepo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID: projectC.ID, SlackUserID: "U1184", DisplayName: "Issue 1184", AddedBy: "test",
	}))
	require.NoError(t, svc.setActiveProject(ctx, "T1184", "U1184", projectC.ID))
	active, err := svc.getActiveProject(ctx, "T1184", "U1184")
	require.NoError(t, err)
	require.Equal(t, projectC.ID, active, "a later successful switch must still replace the fallback")
}

func TestSlackService_DeletedOnlyAuthorizedProjectFallsBackToDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectA, err := projectRepo.GetByID(ctx, "default")
	require.NoError(t, err)
	require.NotNil(t, projectA)
	projectB := &models.Project{Name: "Only Authorized B"}
	require.NoError(t, projectRepo.Create(ctx, projectB))

	authRepo := repository.NewSlackAuthRepo(db)
	require.NoError(t, authRepo.Create(ctx, &models.SlackAuthorizedUser{
		ProjectID: projectB.ID, SlackUserID: "U_ONLY_B", DisplayName: "Only B", AddedBy: "test",
	}))
	svc := NewSlackService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, authRepo)
	projectSvc := NewProjectService(projectRepo)
	svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
	require.NoError(t, svc.setActiveProject(ctx, "T_ONLY", "U_ONLY_B", projectB.ID))

	require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
	active, err := svc.getActiveProject(ctx, "T_ONLY", "U_ONLY_B")
	require.NoError(t, err)
	require.Equal(t, projectA.ID, active, "removing the only authorized project must use the normal default fallback")
	require.NotEqual(t, projectB.ID, active)
}

func TestDiscordService_LiveDeletedSelectedProjectFallsBackWithoutRestart(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectA, err := projectRepo.GetByID(ctx, "default")
	require.NoError(t, err)
	require.NotNil(t, projectA)
	projectB := &models.Project{Name: "Selected B"}
	require.NoError(t, projectRepo.Create(ctx, projectB))

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, DiscordSettingSendResponses, "true"))
	authRepo := repository.NewDiscordAuthRepo(db)
	userID := "1518288288572641184"
	for _, project := range []*models.Project{projectA, projectB} {
		require.NoError(t, authRepo.Create(ctx, &models.DiscordAuthorizedUser{
			ProjectID: project.ID, DiscordUserID: userID, DisplayName: "Issue 1184", AddedBy: "test",
		}))
	}
	userProjectRepo := repository.NewDiscordUserProjectRepo(db)
	svc := NewDiscordService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, authRepo, repository.NewDiscordTaskContextRepo(db))
	svc.SetDiscordUserProjectRepo(userProjectRepo)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(taskSvc)
	svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
	require.NoError(t, svc.setActiveProject(ctx, userID, projectB.ID))

	require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
	_, cached := svc.userProjects[userID]
	require.False(t, cached, "successful deletion must evict Discord's deleted project cache entry")

	var got *ChannelChatRunRequest
	svc.SetChannelChatRunner(func(_ context.Context, request ChannelChatRunRequest) { got = &request })
	svc.sendMessageFunc = func(string, string, string) (string, error) { return "ack", nil }
	svc.processIncomingMessage(discordIncomingMessage{
		ChannelID: "C1184", MessageID: "M1184", UserID: userID, Username: "issue1184", Text: "continue after delete", Source: models.TaskOriginDiscord,
	})
	require.NotNil(t, got)
	require.Equal(t, projectA.ID, got.ProjectID, "Discord must fall back to the live default project")
	require.NotEqual(t, projectB.ID, got.ProjectID)

	tasksA, err := taskRepo.ListByProject(ctx, projectA.ID, "")
	require.NoError(t, err)
	require.Len(t, tasksA, 1)
	tasksB, err := taskRepo.ListByProject(ctx, projectB.ID, "")
	require.NoError(t, err)
	require.Empty(t, tasksB, "deleted project must not receive a task")

	fresh := NewDiscordService(settingsRepo, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, taskSvc, llmSvc, workerSvc, authRepo, repository.NewDiscordTaskContextRepo(db))
	fresh.SetDiscordUserProjectRepo(userProjectRepo)
	require.Equal(t, projectA.ID, fresh.getActiveProject(ctx, userID), "fresh services must retain the existing fallback")
}

func TestTelegramService_LiveDeletedSelectedProjectFallsBackWithoutRestart(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projectA, err := projectRepo.GetByID(ctx, "default")
	require.NoError(t, err)
	require.NotNil(t, projectA)
	projectB := &models.Project{Name: "Selected B"}
	require.NoError(t, projectRepo.Create(ctx, projectB))

	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())
	workerSvc := NewWorkerService(llmSvc, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)
	authRepo := repository.NewTelegramAuthRepo(db)
	const userID int64 = 1184001
	for _, project := range []*models.Project{projectA, projectB} {
		require.NoError(t, authRepo.Create(ctx, &models.TelegramAuthorizedUser{
			ProjectID: project.ID, TelegramUserID: userID, TelegramUsername: "issue1184", DisplayName: "Issue 1184", AddedBy: "test",
		}))
	}
	userProjectRepo := repository.NewTelegramUserProjectRepo(db)
	svc := &TelegramService{
		projectRepo:             projectRepo,
		llmConfigRepo:           llmConfigRepo,
		taskRepo:                taskRepo,
		execRepo:                execRepo,
		scheduleRepo:            scheduleRepo,
		taskSvc:                 taskSvc,
		llmSvc:                  llmSvc,
		workerSvc:               workerSvc,
		telegramAuthRepo:        authRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
		userProjectVersions:     make(map[int64]uint64),
		sendMessageFunc:         func(int64, string) {},
	}
	projectSvc := NewProjectService(projectRepo)
	projectSvc.SetTaskService(taskSvc)
	svc.SetProjectCreationServices(projectSvc, nil, nil, nil)
	require.NoError(t, svc.setTelegramActiveProject(ctx, userID, projectB.ID))

	require.NoError(t, projectSvc.Delete(ctx, projectB.ID))
	_, cached, _ := svc.cachedTelegramActiveProject(userID)
	require.False(t, cached, "successful deletion must evict Telegram's deleted project cache entry")

	runnerDone := make(chan ChannelChatRunRequest, 1)
	svc.SetChannelChatRunner(func(_ context.Context, request ChannelChatRunRequest) { runnerDone <- request })
	update := tgbotapi.Update{UpdateID: 1184, Message: &tgbotapi.Message{
		MessageID: 1184,
		From:      &tgbotapi.User{ID: userID, UserName: "issue1184"},
		Chat:      &tgbotapi.Chat{ID: userID, Type: "private"},
		Text:      "continue after delete",
	}}
	require.True(t, svc.handleTelegramUpdate(ctx, update))
	var got ChannelChatRunRequest
	select {
	case got = <-runnerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Telegram chat runner was not called")
	}
	require.Equal(t, projectA.ID, got.ProjectID, "Telegram must fall back to the live default project")
	require.NotEqual(t, projectB.ID, got.ProjectID)

	tasksA, err := taskRepo.ListByProject(ctx, projectA.ID, "")
	require.NoError(t, err)
	require.Len(t, tasksA, 1)
	tasksB, err := taskRepo.ListByProject(ctx, projectB.ID, "")
	require.NoError(t, err)
	require.Empty(t, tasksB, "deleted project must not receive a task")

	fresh := &TelegramService{
		projectRepo:             projectRepo,
		telegramUserProjectRepo: userProjectRepo,
		userProjects:            make(map[int64]string),
		userProjectVersions:     make(map[int64]uint64),
	}
	require.Equal(t, projectA.ID, fresh.getActiveProject(userID), "fresh services must retain the existing fallback")
}

func TestProjectService_FailedDeletePreservesRegisteredChannelCache(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Delete failure target"}
	require.NoError(t, projectRepo.Create(ctx, project))

	svc := NewSlackService(repository.NewSettingsRepo(db), projectRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	projectSvc := NewProjectService(projectRepo)
	projectSvc.RegisterProjectSelectionCacheInvalidator(svc)
	require.NoError(t, svc.setActiveProject(ctx, "T_FAIL", "U_FAIL", project.ID))

	_, err := db.ExecContext(ctx, `
		CREATE TABLE memory_consolidation_runs (id TEXT PRIMARY KEY);
		CREATE TABLE memory_consolidation_schedules (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			last_run_id TEXT REFERENCES memory_consolidation_runs(id) ON DELETE SET NULL
		);
		INSERT INTO memory_consolidation_schedules(project_id) VALUES (?)`, project.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `DROP TABLE memory_consolidation_runs`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)

	deleteErr := projectSvc.Delete(ctx, project.ID)
	require.Error(t, deleteErr)
	require.Contains(t, deleteErr.Error(), "memory_consolidation_runs")
	require.Equal(t, project.ID, svc.userProjects[slackUserProjectKey("T_FAIL", "U_FAIL")], "failed deletion must not clear a valid cache entry")
	stored, err := projectRepo.GetByID(ctx, project.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
}
