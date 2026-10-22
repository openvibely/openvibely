package service

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func TestBuildChannelChatContextUsesCompactTaskProjection(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	ctx := context.Background()
	taskRepo := db.Tasks
	taskSvc := NewTaskService(taskRepo, nil, nil)
	scheduleRepo := db.Schedules
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
	stats := counter.Snapshot()
	if stats.Requests != 2 || stats.TaskContextReads != 1 || stats.ReadTextBytes > 10000 {
		t.Fatalf("expected two bounded context reads, got %+v", stats)
	}

}

func TestChannelChatContextCompactTaskProjectionMatchesFullContext(t *testing.T) {
	db := teststores.New(t)
	ctx := context.Background()
	taskRepo := db.Tasks
	taskSvc := NewTaskService(taskRepo, nil, nil)
	scheduleRepo := db.Schedules

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
	for i, task := range []*models.Task{long, malformed, root, chat} {
		if err := db.Fixtures.SetTaskDisplayOrder(ctx, task.ID, i+1); err != nil {
			t.Fatal(err)
		}
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
	env := teststores.New(t)
	ctx := context.Background()
	taskRepo := env.Tasks
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
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	ctx := context.Background()
	taskRepo := db.Tasks
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
	for i, task := range []*models.Task{backlog, running, failed, cancelled} {
		if err := db.Fixtures.SetTaskDisplayOrder(ctx, task.ID, []int{5, 20, 100, 101}[i]); err != nil {
			t.Fatal(err)
		}
	}

	executionRepo := db.Executions
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
	stats := counter.Snapshot()
	if stats.TaskContextReads != 2 || stats.TaskWrites != 1 || stats.ExecutionWrites != 1 {
		t.Fatalf("expected two context reads and one batched write each for tasks and executions, got %+v", stats)
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

}

func TestChannelChatIngressUsesCompactSelectionAndSelectedDetail(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	repo := db.LLMConfigs
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, repo, "Channel Text Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	task := &models.Task{ID: "channel-text-task", Title: "Channel text", Category: models.CategoryChat, Status: models.StatusPending}
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:  "slack",
		ProjectID: "project-1",
		Message:   "hello channel",
		DownloadAttachments: func(context.Context) (channelChatIngressDownloadResult, error) {
			return channelChatIngressDownloadResult{AttachmentContext: "Attached file: /uploads/report.pdf"}, nil
		},
		Source:        models.TaskOriginSlack,
		LLMConfigRepo: repo,
		TaskRepo:      db.Tasks,
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

	if runnerRequest.AttachmentContext != "Attached file: /uploads/report.pdf" || strings.Contains(runnerRequest.SystemContext, "/uploads/report.pdf") {
		t.Fatal("attachment reference must reach the runner separately from system context")
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
	stats := counter.Snapshot()
	if stats.ModelReads != 2 || stats.ModelSelectionReads != 1 {
		t.Fatalf("unexpected physical model reads: %+v", stats)
	}
}

func TestChannelChatIngressQueuesUsingCompactModelIDWithoutHydration(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	repo := db.LLMConfigs
	ctx := context.Background()
	model := seedChannelRichModel(t, ctx, repo, "Channel Queue Default", models.ProviderOpenAICompatible, models.AuthMethodAPIKey, true)
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
	stats := counter.Snapshot()
	if stats.ModelReads != 1 || stats.ModelSelectionReads != 1 || stats.ReadTextBytes > 4096 {
		t.Fatalf("unexpected physical model reads: %+v", stats)
	}
}

func TestChannelChatIngressImageSelectionExcludesTextOnlyModels(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	repo := db.LLMConfigs
	ctx := context.Background()
	teststores.ClearLLMConfigs(t, db.LLMConfigs)
	textOnly := &models.LLMConfig{
		Name:       "Text-only model",
		Provider:   models.ProviderOpenAICompatible,
		Model:      "text-only",
		AuthMethod: models.AuthMethodAPIKey,
	}
	vision := seedChannelRichModel(t, ctx, repo, "Anthropic Vision", models.ProviderAnthropic, models.AuthMethodAPIKey, true)
	if err := repo.Create(ctx, textOnly); err != nil {
		t.Fatalf("create text-only model: %v", err)
	}
	counter.SetEnabled(true)

	var runnerRequest ChannelChatRunRequest
	started := runChannelChatIngress(ctx, channelChatIngressOptions{
		Platform:      "telegram",
		ProjectID:     "project-1",
		Message:       "inspect the image",
		Source:        models.TaskOriginTelegram,
		LLMConfigRepo: repo,
		TaskRepo:      db.Tasks,
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
	stats := counter.Snapshot()
	if stats.ModelReads != 2 || stats.ModelSelectionReads != 1 {
		t.Fatalf("unexpected physical model reads: %+v", stats)
	}
}

func TestChannelChatAgentSelectionSkipsOAuthRetiredDefault(t *testing.T) {
	env := teststores.New(t)
	repo := env.LLMConfigs
	ctx := context.Background()
	teststores.ClearLLMConfigs(t, env.LLMConfigs)
	retired := &models.LLMConfig{Name: "Retired", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, IsDefault: true}
	if err := repo.Create(ctx, retired); err != nil {
		t.Fatal(err)
	}
	if _, err := selectChannelChatAgentOptions(ctx, repo, "hello", false); err == nil || !strings.Contains(err.Error(), "no supported models") {
		t.Fatalf("retired-only selection error = %v", err)
	}
	supported := &models.LLMConfig{Name: "Custom", Provider: models.ProviderOpenAICompatible, Model: "custom"}
	if err := repo.Create(ctx, supported); err != nil {
		t.Fatal(err)
	}
	// Requiring vision exercises the channel's default/first-model fallback.
	selection, err := selectChannelChatAgentOptions(ctx, repo, "hello", true)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Agent == nil || selection.Agent.ID != supported.ID {
		t.Fatalf("selected %v, want supported model %s", selection, supported.ID)
	}
}

func TestChannelChatAgentSelectionWithNoModelsUsesCompactQuery(t *testing.T) {
	db := teststores.New(t, teststores.WithIOCounting())
	counter := db.IO
	repo := db.LLMConfigs
	ctx := context.Background()
	teststores.ClearLLMConfigs(t, db.LLMConfigs)
	counter.SetEnabled(true)

	if _, err := selectChannelChatAgentOptions(ctx, repo, "hello", false); err == nil || err.Error() != "no agents configured" {
		t.Fatalf("selection error = %v, want no agents configured", err)
	}
	counter.SetEnabled(false)
	stats := counter.Snapshot()
	if stats.ModelReads != 1 || stats.ModelSelectionReads != 1 || stats.ReadTextBytes > 4096 {
		t.Fatalf("expected one physical model read, got %+v", stats)
	}

}

func BenchmarkChannelChatTaskContextProjection(b *testing.B) {
	for _, taskCount := range []int{20, 300} {
		b.Run(fmt.Sprintf("Compact/%d", taskCount), func(b *testing.B) {
			fixture := newChannelTaskContextBenchmarkFixture(b, taskCount)
			fixture.assertTwoContextReads(b)
			fixture.benchmark(b)
		})
	}
}

func TestChannelChatTaskContextUsesCompactProjection(t *testing.T) {
	for _, taskCount := range []int{20, 300} {
		t.Run(fmt.Sprintf("%d_tasks", taskCount), func(t *testing.T) {
			fixture := newChannelTaskContextBenchmarkFixture(t, taskCount)
			fixture.assertTwoContextReads(t)
			fixture.mustLoad(t)
		})
	}
}

type channelTaskContextBenchmarkFixture struct {
	ctx          context.Context
	counter      storefixture.IOCounter
	taskSvc      *TaskService
	scheduleRepo persistence.ScheduleStore
}

func newChannelTaskContextBenchmarkFixture(tb testing.TB, taskCount int) *channelTaskContextBenchmarkFixture {
	tb.Helper()
	db := teststores.New(tb, teststores.WithIOCounting())
	counter := db.IO
	ctx := context.Background()
	taskRepo := db.Tasks
	scheduleRepo := db.Schedules
	payload := strings.Repeat("payload", 128*1024/len("payload"))
	chainPayload := strings.Repeat("chain-payload", 128*1024/len("chain-payload"))
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
		scheduleRepo: scheduleRepo,
	}
}

func (f *channelTaskContextBenchmarkFixture) mustLoad(tb testing.TB) {
	tb.Helper()
	contextText, err := f.load()
	if err != nil {
		tb.Fatalf("load benchmark context: %v", err)
	}
	if contextText == "" {
		tb.Fatal("benchmark context is empty")
	}
}

func (f *channelTaskContextBenchmarkFixture) load() (string, error) {
	tasks, err := f.taskSvc.ListChatContextByProject(f.ctx, "default")
	if err != nil {
		return "", err
	}
	schedules, err := f.scheduleRepo.ListByProject(f.ctx, "default")
	if err != nil {
		return "", err
	}
	return BuildChatContextWithAgentDefinitions(tasks, nil, nil, schedules, time.Date(2026, time.January, 12, 12, 0, 0, 0, time.UTC)), nil
}

func (f *channelTaskContextBenchmarkFixture) assertTwoContextReads(tb testing.TB) {
	tb.Helper()
	f.counter.Reset()
	f.counter.SetEnabled(true)
	if _, err := f.load(); err != nil {
		tb.Fatalf("load benchmark context: %v", err)
	}
	f.counter.SetEnabled(false)
	if got := f.counter.Snapshot().Requests; got != 2 {
		tb.Fatalf("context query count = %d, want 2", got)
	}
}

func (f *channelTaskContextBenchmarkFixture) benchmark(b *testing.B) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contextText, err := f.load()
		if err != nil {
			b.Fatal(err)
		}
		if contextText == "" {
			b.Fatal("benchmark context is empty")
		}
	}
}
func BenchmarkChannelModelLoads(b *testing.B) {
	db := teststores.New(b)
	repo := db.LLMConfigs
	ctx := context.Background()
	seedChannelRichModels(b, ctx, repo, 50)

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

func TestChannelModelLoadingUsesCompactProjectionOnLargeFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping channel model-loading large fixture in short mode")
	}
	db := teststores.New(t)
	repo := db.LLMConfigs
	ctx := context.Background()
	seedChannelRichModels(t, ctx, repo, 50)

	configs, err := repo.ListChatSelectionOptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 50 || configs[0].ID == "" {
		t.Fatal("compact selection fixture returned an invalid catalog")
	}
	selected, err := repo.GetByID(ctx, configs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.APIKey == "" || BuildModelContextString(configs) == "" {
		t.Fatal("compact path did not retain selected detail and context")
	}
}

func seedChannelRichModel(tb testing.TB, ctx context.Context, repo persistence.LLMConfigStore, name string, provider models.LLMProvider, authMethod models.AuthMethod, isDefault bool) *models.LLMConfig {
	tb.Helper()
	teststores.ClearLLMConfigs(tb, repo)
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
	if provider == models.ProviderAnthropic {
		model.Model = "claude-sonnet-5"
	}
	if err := repo.Create(ctx, model); err != nil {
		tb.Fatalf("create rich model: %v", err)
	}
	return model
}

func seedChannelRichModels(tb testing.TB, ctx context.Context, repo persistence.LLMConfigStore, count int) {
	tb.Helper()
	teststores.ClearLLMConfigs(tb, repo)
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
