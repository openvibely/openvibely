package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestBuildChannelChatContextUsesCompactTaskProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(taskRepo, nil, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Inbound context task",
		Category:    models.CategoryBacklog,
		Priority:    2,
		Status:      models.StatusPending,
		Prompt:      strings.Repeat("prompt payload ", 4096),
		ChainConfig: `{"enabled":true,"trigger":"on_completion","child_title":"Inbound child"}`,
		SwarmConfig: strings.Repeat("swarm payload ", 4096),
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create context task: %v", err)
	}
	runAt := time.Now().UTC().Add(time.Hour)
	if err := scheduleRepo.Create(ctx, &models.Schedule{
		TaskID:         task.ID,
		RunAt:          runAt,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &runAt,
	}); err != nil {
		t.Fatalf("create context schedule: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	got := buildChannelChatContext(ctx, channelChatContextOptions{
		ProjectID:    "default",
		TaskSvc:      taskSvc,
		ScheduleRepo: scheduleRepo,
	})
	counter.SetEnabled(false)
	if !strings.Contains(got, task.Title) || !strings.Contains(got, "Inbound child") || !strings.Contains(got, "Scheduled tasks in this project:") {
		t.Fatalf("shared context lost task or schedule details: %s", got)
	}
	statements := counter.Statements()
	if len(statements) != 2 {
		t.Fatalf("shared context statements = %#v, want exactly task plus schedule reads", statements)
	}
	var taskStatement string
	for _, statement := range statements {
		if strings.Contains(strings.ToLower(statement), "substr(t.prompt") {
			taskStatement = strings.ToLower(statement)
			break
		}
	}
	if taskStatement == "" {
		t.Fatalf("shared context did not use compact task query: %#v", statements)
	}
	for _, forbidden := range []string{"swarm_config", "worktree_path", "merge_target_branch", "lineage_depth", "completed_at"} {
		if strings.Contains(taskStatement, forbidden) {
			t.Fatalf("shared context task query selected full-only column %q: %s", forbidden, taskStatement)
		}
	}
}

func TestChannelChatContextCompactTaskProjectionMatchesFullContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(taskRepo, nil, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	root := &models.Task{
		ProjectID:   "default",
		Title:       "Compact root",
		Category:    models.CategoryBacklog,
		Priority:    3,
		Status:      models.StatusPending,
		Tag:         "feature",
		Prompt:      "short prompt",
		ChainConfig: `{"enabled":true,"trigger":"on_completion","child_title":"Generated child"}`,
	}
	long := &models.Task{
		ProjectID:   "default",
		Title:       "Compact long",
		Category:    models.CategoryScheduled,
		Priority:    2,
		Status:      models.StatusRunning,
		Prompt:      strings.Repeat("long prompt ", 100),
		ChainConfig: `{"enabled":true,"trigger":"on_planning_complete","child_title":"Long child"}`,
	}
	malformed := &models.Task{
		ProjectID:    "default",
		Title:        "Compact malformed",
		Category:     models.CategoryCompleted,
		Priority:     1,
		Status:       models.StatusCompleted,
		Prompt:       "malformed chain",
		ChainConfig:  `{"enabled":`,
		ParentTaskID: &root.ID,
	}
	chat := &models.Task{
		ProjectID: "default",
		Title:     "Compact chat task",
		Category:  models.CategoryChat,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "chat task must remain excluded",
	}
	for _, task := range []*models.Task{root, long, malformed, chat} {
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", task.Title, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = CASE title WHEN 'Compact long' THEN 1 WHEN 'Compact malformed' THEN 2 WHEN 'Compact root' THEN 3 ELSE 4 END WHERE project_id = 'default'`); err != nil {
		t.Fatalf("set deterministic task display order: %v", err)
	}
	runAt := time.Date(2026, time.January, 12, 9, 30, 0, 0, time.UTC)
	for _, task := range []*models.Task{root, long} {
		nextRun := runAt.Add(time.Hour)
		if err := scheduleRepo.Create(ctx, &models.Schedule{
			TaskID:         task.ID,
			RunAt:          runAt,
			RepeatType:     models.RepeatDaily,
			RepeatInterval: 1,
			Enabled:        task == root,
			NextRun:        &nextRun,
		}); err != nil {
			t.Fatalf("create schedule for %q: %v", task.Title, err)
		}
	}

	fullTasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list full task context fixture: %v", err)
	}
	compactTasks, err := taskSvc.ListChatContextByProject(ctx, "default")
	if err != nil {
		t.Fatalf("list compact task context fixture: %v", err)
	}
	schedules, err := scheduleRepo.ListByProject(ctx, "default")
	if err != nil {
		t.Fatalf("list schedule context fixture: %v", err)
	}
	want := BuildChatContextWithAgentDefinitions(fullTasks, nil, nil, schedules, runAt)
	got := BuildChatContextWithAgentDefinitions(compactTasks, nil, nil, schedules, runAt)
	if got != want {
		t.Fatalf("compact context changed model-facing bytes\nwant:\n%s\ngot:\n%s", want, got)
	}
	if strings.Contains(got, chat.Title) || !strings.Contains(got, "Generated child") || !strings.Contains(got, "tag:feature") || !strings.Contains(got, "parent:"+root.ID) {
		t.Fatalf("compact context lost task filtering or annotations: %s", got)
	}
}

func TestChannelChatContextCompactChainProjectionMatchesJSONUnmarshalEdgeCases(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(taskRepo, nil, nil)

	cases := []struct {
		name        string
		chainConfig string
	}{
		{name: "null scalar fields", chainConfig: `{"enabled":true,"trigger":null,"child_title":null}`},
		{name: "case insensitive keys", chainConfig: `{"ENABLED":true,"TRIGGER":"on_completion","CHILD_TITLE":"Case child"}`},
		{name: "wrong known scalar type", chainConfig: `{"enabled":true,"child_model":123}`},
		{name: "wrong nested scalar type", chainConfig: `{"enabled":true,"child_chain_config":{"child_model":123}}`},
		{name: "null nested chain", chainConfig: `{"enabled":true,"child_chain_config":null}`},
		{name: "duplicate null preserves scalar values", chainConfig: `{"enabled":true,"trigger":"on_completion","child_title":"Duplicate child","enabled":null,"trigger":null,"child_title":null}`},
		{name: "duplicate non-null overwrites scalar values", chainConfig: `{"enabled":true,"trigger":"on_completion","child_title":"First child","enabled":false,"trigger":"on_planning_complete","child_title":"Second child"}`},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &models.Task{
				ProjectID:    "default",
				Title:        tc.name,
				Category:     models.CategoryBacklog,
				Priority:     2,
				Status:       models.StatusPending,
				Prompt:       "edge-case prompt",
				ChainConfig:  tc.chainConfig,
				DisplayOrder: i,
			}
			if err := taskRepo.Create(ctx, task); err != nil {
				t.Fatalf("create edge-case task: %v", err)
			}
			fullTasks, err := taskRepo.ListByProject(ctx, "default", "")
			if err != nil {
				t.Fatalf("list full edge-case tasks: %v", err)
			}
			compactTasks, err := taskSvc.ListChatContextByProject(ctx, "default")
			if err != nil {
				t.Fatalf("list compact edge-case tasks: %v", err)
			}
			want := BuildChatContextWithAgentDefinitions(fullTasks, nil, nil, nil, time.Unix(0, 0))
			got := BuildChatContextWithAgentDefinitions(compactTasks, nil, nil, nil, time.Unix(0, 0))
			if got != want {
				t.Fatalf("compact chain context changed model-facing bytes\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func TestListChatContextByProjectNormalizesActiveTerminalTasks(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	taskSvc := NewTaskService(taskRepo, nil, nil)

	backlog := &models.Task{
		ProjectID: "default",
		Title:     "Existing backlog task",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "backlog prompt",
	}
	failed := &models.Task{
		ProjectID: "default",
		Title:     "Active failed task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusFailed,
		Prompt:    "failed prompt",
	}
	cancelled := &models.Task{
		ProjectID: "default",
		Title:     "Active cancelled task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusCancelled,
		Prompt:    "cancelled prompt",
	}
	running := &models.Task{
		ProjectID: "default",
		Title:     "Active running task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusRunning,
		Prompt:    "running prompt",
	}
	for _, task := range []*models.Task{backlog, failed, cancelled, running} {
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", task.Title, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = CASE title
		WHEN 'Existing backlog task' THEN 5
		WHEN 'Active running task' THEN 20
		WHEN 'Active failed task' THEN 100
		WHEN 'Active cancelled task' THEN 101
	END WHERE project_id = ?`, "default"); err != nil {
		t.Fatalf("set deterministic display order: %v", err)
	}
	executionRepo := repository.NewExecutionRepo(db)
	queuedExecutions := make([]*models.Execution, 0, 2)
	for _, task := range []*models.Task{failed, cancelled} {
		execution := &models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: task.Prompt}
		if err := executionRepo.Create(ctx, execution); err != nil {
			t.Fatalf("create queued execution for %q: %v", task.Title, err)
		}
		queuedExecutions = append(queuedExecutions, execution)
	}

	counter.Reset()
	counter.SetEnabled(true)
	got, err := taskSvc.ListChatContextByProject(ctx, "default")
	counter.SetEnabled(false)
	statements := counter.Statements()
	compactReads := 0
	categoryUpdates := 0
	executionUpdates := 0
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		switch {
		case strings.Contains(normalized, "substr(t.prompt, 1, 501)"):
			compactReads++
		case strings.Contains(normalized, "update tasks") && strings.Contains(normalized, "returning id, title"):
			categoryUpdates++
		case strings.Contains(normalized, "update executions") && strings.Contains(normalized, "task_id in (?,?)"):
			executionUpdates++
		}
		if strings.Contains(normalized, "where id = ? and project_id = ? and category = ?") {
			t.Fatalf("compact normalization used a per-task category update: %s", statement)
		}
		if strings.Contains(normalized, "where task_id = ? and status = 'queued'") {
			t.Fatalf("compact normalization used a per-task execution update: %s", statement)
		}
	}
	if compactReads != 2 || categoryUpdates != 1 || executionUpdates != 1 {
		t.Fatalf("compact terminal normalization statements = %#v, want two compact reads, one batch category update, and one batch execution update", statements)
	}
	if err != nil {
		t.Fatalf("ListChatContextByProject: %v", err)
	}
	wantOrder := []string{"Existing backlog task", "Active failed task", "Active cancelled task", "Active running task"}
	if len(got) != len(wantOrder) {
		t.Fatalf("compact context tasks = %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Title != want {
			t.Fatalf("compact context order[%d] = %q, want %q", i, got[i].Title, want)
		}
	}
	byID := make(map[string]models.Task, len(got))
	for _, task := range got {
		byID[task.ID] = task
	}
	for _, task := range []*models.Task{failed, cancelled} {
		if got := byID[task.ID].Category; got != models.CategoryBacklog {
			t.Errorf("projected terminal task %q category = %q, want backlog", task.Title, got)
		}
		persisted, err := taskRepo.GetByID(ctx, task.ID)
		if err != nil {
			t.Fatalf("reload terminal task %q: %v", task.Title, err)
		}
		if persisted.Category != models.CategoryBacklog {
			t.Errorf("persisted terminal task %q category = %q, want backlog", task.Title, persisted.Category)
		}
	}
	for _, execution := range queuedExecutions {
		persisted, err := executionRepo.GetByID(ctx, execution.ID)
		if err != nil {
			t.Fatalf("reload queued execution %q: %v", execution.ID, err)
		}
		if persisted.Status != models.ExecCancelled || persisted.ErrorMessage != "Task left the reserved running lane" {
			t.Fatalf("queued execution %q = status %q/error %q, want cancelled/reserved-lane error", execution.ID, persisted.Status, persisted.ErrorMessage)
		}
	}
	if got := byID[running.ID].Category; got != models.CategoryActive {
		t.Errorf("projected running task category = %q, want active", got)
	}
	fullTasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("reload full task context: %v", err)
	}
	wantContext := BuildChatContextWithAgentDefinitions(fullTasks, nil, nil, nil, time.Unix(0, 0))
	gotContext := BuildChatContextWithAgentDefinitions(got, nil, nil, nil, time.Unix(0, 0))
	if gotContext != wantContext {
		t.Fatalf("compact normalized context changed model-facing bytes\nwant:\n%s\ngot:\n%s", wantContext, gotContext)
	}
	for _, statement := range counter.Statements() {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "select id, project_id, title, category, priority, status, prompt") {
			t.Fatalf("compact normalization hydrated the full task projection: %s", statement)
		}
	}
}

func TestChannelChatIngressUsesCompactSelectionAndSelectedDetail(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, db, repo, "Channel Text Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	task := &models.Task{ID: "channel-text-task", Title: "Channel text", Category: models.CategoryChat, Status: models.StatusPending}
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "slack",
		ProjectID:     "project-1",
		Message:       "hello channel",
		Source:        models.TaskOriginSlack,
		LLMConfigRepo: repo,
		TaskRepo:      repository.NewTaskRepo(db, nil),
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: task,
			CreateDurableFirstTurn: func(_ context.Context, task *models.Task, execution *models.Execution, _ []models.ChatAttachment) (bool, error) {
				execution.ID = "channel-text-execution"
				execution.TaskID = task.ID
				return false, nil
			},
			ChannelChatRunner: func(_ context.Context, request ChannelChatRunRequest) {
				runnerRequest = request
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}

	if runnerRequest.Agent.ID != model.ID {
		t.Fatalf("runner selected model ID = %q, want %q", runnerRequest.Agent.ID, model.ID)
	}
	if runnerRequest.Agent.APIKey != "channel-api-key" || runnerRequest.Agent.BaseURL != "https://channel.example/v1" ||
		runnerRequest.Agent.ExtraBodyJSON == "" || runnerRequest.Agent.CustomAuthStateJSON == "" || runnerRequest.Agent.MixtureConfigJSON == "" {
		t.Fatalf("runner did not receive full selected model configuration: %#v", runnerRequest.Agent)
	}
	for _, want := range []string{model.ID, model.Name, model.Model, string(model.Provider), "(default)"} {
		if !strings.Contains(runnerRequest.SystemContext, want) {
			t.Fatalf("runner context missing %q: %s", want, runnerRequest.SystemContext)
		}
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 2 {
		t.Fatalf("agent_configs statements = %#v, want one compact selection and one selected detail query", modelStatements)
	}
	assertChannelCompactStatement(t, modelStatements)
}

func TestChannelChatIngressQueuesUsingCompactModelIDWithoutHydration(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, db, repo, "Channel Queue Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
	counter.SetEnabled(true)

	var queued *models.ThreadInput
	runnerCalled := false
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "discord",
		ProjectID:     "project-1",
		Message:       "queue this",
		Source:        models.TaskOriginDiscord,
		LLMConfigRepo: repo,
		FindActiveExecution: func(context.Context, string) (*models.Execution, error) {
			return &models.Execution{ID: "active-channel-execution"}, nil
		},
		CreateQueuedInput: func(_ context.Context, input *models.ThreadInput) (bool, error) {
			queued = input
			return false, nil
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			ChannelChatRunner: func(_ context.Context, _ ChannelChatRunRequest) {
				runnerCalled = true
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}
	if runnerCalled {
		t.Fatal("queued channel message invoked the provider runner")
	}
	if queued == nil || queued.AgentConfigID != model.ID {
		t.Fatalf("queued input = %#v, want selected compact model ID %q", queued, model.ID)
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 1 {
		t.Fatalf("agent_configs statements = %#v, want one compact selection query", modelStatements)
	}
	assertChannelCompactStatement(t, modelStatements)
}

func TestChannelChatIngressImageSelectionExcludesLegacyAnthropicCLI(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	legacy := &models.LLMConfig{
		Name:       "Legacy Anthropic CLI",
		Provider:   models.ProviderAnthropic,
		Model:      "claude-cli",
		AuthMethod: models.AuthMethodCLI,
	}
	vision := seedChannelRichModel(t, ctx, db, repo, "Anthropic Vision", models.ProviderAnthropic, models.AuthMethodAPIKey, true)
	if err := repo.Create(ctx, legacy); err != nil {
		t.Fatalf("create legacy model: %v", err)
	}
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "telegram",
		ProjectID:     "project-1",
		Message:       "inspect the image",
		Source:        models.TaskOriginTelegram,
		LLMConfigRepo: repo,
		TaskRepo:      repository.NewTaskRepo(db, nil),
		DownloadAttachments: func(context.Context) (channelChatIngressDownloadResult, error) {
			return channelChatIngressDownloadResult{ImageAttachments: []models.Attachment{{FileName: "image.png", MediaType: "image/png"}}}, nil
		},
		FirstTurn: channelChatIngressFirstTurnOptions{
			Task: &models.Task{ID: "channel-image-task", Title: "Channel image", Category: models.CategoryChat, Status: models.StatusPending},
			CreateDurableFirstTurn: func(_ context.Context, _ *models.Task, execution *models.Execution, _ []models.ChatAttachment) (bool, error) {
				execution.ID = "channel-image-execution"
				return false, nil
			},
			ChannelChatRunner: func(_ context.Context, request ChannelChatRunRequest) {
				runnerRequest = request
			},
		},
	})
	if !started {
		t.Fatal("runChannelChatIngress returned false")
	}
	if runnerRequest.Agent.ID != vision.ID {
		t.Fatalf("image runner selected model ID = %q, want vision model %q", runnerRequest.Agent.ID, vision.ID)
	}
	if runnerRequest.Agent.APIKey != "channel-api-key" || len(runnerRequest.ImageAttachments) != 1 {
		t.Fatalf("image runner did not receive hydrated vision model and image: %#v", runnerRequest)
	}

	counter.SetEnabled(false)
	modelStatements := channelAgentConfigStatements(counter.Statements())
	if len(modelStatements) != 2 {
		t.Fatalf("agent_configs statements = %#v, want compact vision selection plus selected detail query", modelStatements)
	}
	assertChannelVisionCompactStatement(t, modelStatements)
}

func TestChannelChatAgentSelectionWithNoModelsUsesCompactQuery(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	counter.SetEnabled(true)

	if _, err := selectChannelChatAgentOptions(ctx, repo, "hello", false); err == nil || err.Error() != "no agents configured" {
		t.Fatalf("selection error = %v, want no agents configured", err)
	}
	counter.SetEnabled(false)
	statements := channelAgentConfigStatements(counter.Statements())
	if len(statements) != 1 {
		t.Fatalf("agent_configs statements = %#v, want one compact query", statements)
	}
	assertChannelCompactStatement(t, statements)
}

func BenchmarkChannelChatTaskContextProjection(b *testing.B) {
	for _, taskCount := range []int{20, 300} {
		b.Run(fmt.Sprintf("Full/%d", taskCount), func(b *testing.B) {
			fixture := newChannelTaskContextBenchmarkFixture(b, taskCount)
			fixture.assertTwoContextReads(b, true)
			fixture.benchmark(b, true)
		})
		b.Run(fmt.Sprintf("Compact/%d", taskCount), func(b *testing.B) {
			fixture := newChannelTaskContextBenchmarkFixture(b, taskCount)
			fixture.assertTwoContextReads(b, false)
			fixture.benchmark(b, false)
		})
	}
}

type channelTaskContextBenchmarkFixture struct {
	ctx          context.Context
	counter      *testutil.SQLStatementCounter
	taskSvc      *TaskService
	taskRepo     *repository.TaskRepo
	scheduleRepo *repository.ScheduleRepo
}

func newChannelTaskContextBenchmarkFixture(tb testing.TB, taskCount int) *channelTaskContextBenchmarkFixture {
	tb.Helper()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	payload := strings.Repeat("payload", 32*1024/len("payload"))
	chainPayload := strings.Repeat("chain-payload", 32*1024/len("chain-payload"))
	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID:   "default",
			Title:       fmt.Sprintf("Benchmark task %03d", i),
			Category:    models.CategoryBacklog,
			Priority:    2,
			Status:      models.StatusPending,
			Prompt:      payload,
			ChainConfig: chainPayload,
			SwarmConfig: payload,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			tb.Fatalf("create benchmark task %d: %v", i, err)
		}
		if i%3 == 0 {
			runAt := time.Date(2026, time.January, 12, 9, 30, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
			if err := scheduleRepo.Create(ctx, &models.Schedule{
				TaskID:         task.ID,
				RunAt:          runAt,
				RepeatType:     models.RepeatDaily,
				RepeatInterval: 1,
				Enabled:        true,
				NextRun:        &runAt,
			}); err != nil {
				tb.Fatalf("create benchmark schedule %d: %v", i, err)
			}
		}
	}
	return &channelTaskContextBenchmarkFixture{
		ctx:          ctx,
		counter:      counter,
		taskSvc:      NewTaskService(taskRepo, nil, nil),
		taskRepo:     taskRepo,
		scheduleRepo: scheduleRepo,
	}
}

func (f *channelTaskContextBenchmarkFixture) load(full bool) (string, error) {
	var (
		tasks []models.Task
		err   error
	)
	if full {
		tasks, err = f.taskSvc.ListByProject(f.ctx, "default", "")
	} else {
		tasks, err = f.taskSvc.ListChatContextByProject(f.ctx, "default")
	}
	if err != nil {
		return "", err
	}
	schedules, err := f.scheduleRepo.ListByProject(f.ctx, "default")
	if err != nil {
		return "", err
	}
	return BuildChatContextWithAgentDefinitions(tasks, nil, nil, schedules, time.Date(2026, time.January, 12, 12, 0, 0, 0, time.UTC)), nil
}

func (f *channelTaskContextBenchmarkFixture) assertTwoContextReads(tb testing.TB, full bool) {
	tb.Helper()
	f.counter.Reset()
	f.counter.SetEnabled(true)
	if _, err := f.load(full); err != nil {
		tb.Fatalf("load benchmark context: %v", err)
	}
	f.counter.SetEnabled(false)
	if got := len(f.counter.Statements()); got != 2 {
		tb.Fatalf("context query count = %d, want 2", got)
	}
}

func (f *channelTaskContextBenchmarkFixture) benchmark(b *testing.B, full bool) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contextText, err := f.load(full)
		if err != nil {
			b.Fatal(err)
		}
		if contextText == "" {
			b.Fatal("benchmark context is empty")
		}
	}
}
func BenchmarkChannelModelLoads(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	seedChannelRichModels(b, ctx, db, repo, 50)

	b.Run("FullListTwice", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			result := SelectLLM(AnalyzeComplexity("hello channel"), configs)
			if result == nil || result.LLMConfig == nil {
				b.Fatal("full selection returned no model")
			}
			contextConfigs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if got := BuildModelContextString(contextConfigs); got == "" {
				b.Fatal("full context was empty")
			}
		}
	})

	b.Run("CompactSelectionSelectedDetail", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			selection, err := selectChannelChatAgentOptions(ctx, repo, "hello channel", false)
			if err != nil {
				b.Fatal(err)
			}
			selected, err := hydrateSelectedChannelChatAgent(ctx, repo, selection)
			if err != nil {
				b.Fatal(err)
			}
			if selected.APIKey == "" || BuildModelContextString(selection.AvailableModels) == "" {
				b.Fatal("compact path did not retain selected detail and context")
			}
		}
	})
}

func TestChannelModelLoadingProjectionMeetsPerformanceBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-shaped channel model-loading performance guard in short mode")
	}
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()
	seedChannelRichModels(t, ctx, db, repo, 50)

	full := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || BuildModelContextString(configs) == "" {
				b.Fatal("full selection fixture returned an invalid catalog")
			}
			contextConfigs, err := repo.List(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if BuildModelContextString(contextConfigs) == "" {
				b.Fatal("full context fixture returned an empty catalog")
			}
		}
	})
	compact := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			configs, err := repo.ListChatSelectionOptions(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if len(configs) != 50 || configs[0].ID == "" {
				b.Fatal("compact selection fixture returned an invalid catalog")
			}
			selected, err := repo.GetByID(ctx, configs[0].ID)
			if err != nil {
				b.Fatal(err)
			}
			if selected == nil || selected.APIKey == "" || BuildModelContextString(configs) == "" {
				b.Fatal("compact path did not retain selected detail and context")
			}
		}
	})

	t.Logf("full catalog twice: %d ns/op, %d B/op, %d allocs/op", full.NsPerOp(), full.AllocedBytesPerOp(), full.AllocsPerOp())
	t.Logf("compact selection + selected detail: %d ns/op, %d B/op, %d allocs/op", compact.NsPerOp(), compact.AllocedBytesPerOp(), compact.AllocsPerOp())
	if testing.CoverMode() != "" {
		return
	}
	// The compact path hydrates one full selected model after the projection. Keep
	// the guard tight while allowing the provider-aware compaction scalar fields
	// now stored on the full model record.
	if compact.NsPerOp() > (200*1000) || compact.AllocedBytesPerOp() > 304*1024 {
		t.Fatalf("compact channel model loading exceeded budget: %d ns/op, %d B/op", compact.NsPerOp(), compact.AllocedBytesPerOp())
	}
	if full.NsPerOp()/compact.NsPerOp() < 50 {
		t.Fatalf("compact channel model loading latency improvement = %.1fx, want at least 50x", float64(full.NsPerOp())/float64(compact.NsPerOp()))
	}
	if full.AllocedBytesPerOp()/compact.AllocedBytesPerOp() < 40 {
		t.Fatalf("compact channel model loading allocation improvement = %.1fx, want at least 40x", float64(full.AllocedBytesPerOp())/float64(compact.AllocedBytesPerOp()))
	}
}

func seedChannelRichModel(tb testing.TB, ctx context.Context, db *sql.DB, repo *repository.LLMConfigRepo, name string, provider models.LLMProvider, authMethod models.AuthMethod, isDefault bool) *models.LLMConfig {
	tb.Helper()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		tb.Fatalf("clear model configs: %v", err)
	}
	largePayload := strings.Repeat("x", 56*1024)
	model := &models.LLMConfig{
		Name:                 name,
		Provider:             provider,
		Model:                "rich-channel-model",
		AuthMethod:           authMethod,
		APIKey:               "channel-api-key",
		OAuthAccessToken:     "channel-oauth-token",
		OAuthRefreshToken:    "channel-refresh-token",
		OAuthClientID:        "channel-client-id",
		OAuthClientSecret:    "channel-client-secret",
		BaseURL:              "https://channel.example/v1",
		ExtraBodyJSON:        `{"large":"` + largePayload + `"}`,
		CustomAuthConfigJSON: `{"secret":"channel-custom-auth"}`,
		CustomAuthStateJSON:  `{"token":"channel-custom-state"}`,
		MixtureConfigJSON:    `{"large":"` + largePayload + `"}`,
		IsDefault:            isDefault,
	}
	if err := repo.Create(ctx, model); err != nil {
		tb.Fatalf("create rich model: %v", err)
	}
	return model
}

func seedChannelRichModels(tb testing.TB, ctx context.Context, db *sql.DB, repo *repository.LLMConfigRepo, count int) {
	tb.Helper()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		tb.Fatalf("clear model configs: %v", err)
	}
	largePayload := strings.Repeat("x", 56*1024)
	for i := 0; i < count; i++ {
		model := &models.LLMConfig{
			Name:                 fmt.Sprintf("Channel Rich %02d", i),
			Provider:             models.ProviderOpenAICompatible,
			Model:                fmt.Sprintf("rich-channel-model-%02d", i),
			AuthMethod:           models.AuthMethodAPIKey,
			APIKey:               fmt.Sprintf("channel-api-key-%02d", i),
			BaseURL:              "https://channel.example/v1",
			ExtraBodyJSON:        `{"large":"` + largePayload + `"}`,
			CustomAuthConfigJSON: `{"secret":"channel-custom-auth"}`,
			CustomAuthStateJSON:  `{"token":"channel-custom-state"}`,
			MixtureConfigJSON:    `{"large":"` + largePayload + `"}`,
			IsDefault:            i == 0,
		}
		if err := repo.Create(ctx, model); err != nil {
			tb.Fatalf("create rich model %d: %v", i, err)
		}
	}
}

func channelAgentConfigStatements(statements []string) []string {
	var modelStatements []string
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "from agent_configs") {
			modelStatements = append(modelStatements, normalized)
		}
	}
	return modelStatements
}

func assertChannelCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	compact := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if projection == "select id, name, provider, model, is_default" {
			compact++
			continue
		}
		if !strings.Contains(statement, "where id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if compact != 1 {
		t.Fatalf("compact chat selection query count = %d, statements = %#v", compact, statements)
	}
}

func assertChannelVisionCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	vision := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if strings.HasPrefix(projection, "select id, name, provider, model, auth_method, is_default,") {
			vision++
			for _, forbidden := range []string{
				"oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url",
				"base_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json",
			} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("vision compact query selected forbidden column %q: %s", forbidden, statement)
				}
			}
			continue
		}
		if !strings.Contains(statement, "where id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if vision != 1 {
		t.Fatalf("compact vision selection query count = %d, statements = %#v", vision, statements)
	}
}
