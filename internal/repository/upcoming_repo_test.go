package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

// BenchmarkListRecentExecutions measures per-op allocations with 100 executions
// each carrying a ~50 KB output payload. The truncated SELECT introduced in
// issue #627 should reduce B/op by at least 5× compared to fetching full text.
func BenchmarkListRecentExecutions(b *testing.B) {
	db := testutil.NewTestDB(b)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)

	project := &models.Project{Name: "bench-project", RepoPath: "/tmp/bench"}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		b.Fatalf("creating project: %v", err)
	}
	agent := &models.LLMConfig{Name: "BenchAgent", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-20250514"}
	if err := agentRepo.Create(context.Background(), agent); err != nil {
		b.Fatalf("creating agent: %v", err)
	}

	// 50 KB output payload — representative of real execution output sizes.
	largeOutput := strings.Repeat("x", 50*1024)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Bench Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		b.Fatalf("creating task: %v", err)
	}

	for i := 0; i < 100; i++ {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    strings.Repeat("p", 10*1024),
		}
		if err := execRepo.Create(context.Background(), exec); err != nil {
			b.Fatalf("creating execution: %v", err)
		}
		if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, largeOutput, "", 100, 5000); err != nil {
			b.Fatalf("completing execution: %v", err)
		}
	}

	since := time.Now().UTC().Add(-1 * time.Hour)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		results, err := upcomingRepo.ListRecentExecutions(context.Background(), project.ID, since)
		if err != nil {
			b.Fatalf("ListRecentExecutions: %v", err)
		}
		if len(results) != 100 {
			b.Fatalf("expected 100 executions, got %d", len(results))
		}
		_ = results
	}
}

func createTestAgent(t *testing.T, llmConfigRepo *LLMConfigRepo) models.LLMConfig {
	t.Helper()
	a := &models.LLMConfig{
		Name:     "Test Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-20250514",
	}
	if err := llmConfigRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("creating test agent: %v", err)
	}
	return *a
}

func TestUpcomingRepo_ListRunningTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := createTestProject(t, projectRepo)

	// Create a running task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Running Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "Do something",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Create a pending task (should NOT appear)
	pending := &models.Task{
		ProjectID: project.ID,
		Title:     "Pending Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do something else",
	}
	if err := taskRepo.Create(context.Background(), pending); err != nil {
		t.Fatalf("creating pending task: %v", err)
	}

	results, err := upcomingRepo.ListRunningTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 running task, got %d", len(results))
	}
	if results[0].Task.Title != "Running Task" {
		t.Fatalf("expected 'Running Task', got %q", results[0].Task.Title)
	}
}

func TestUpcomingRepo_ListPendingActiveTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)

	project := createTestProject(t, projectRepo)
	agent := createTestAgent(t, agentRepo)

	// Create an active pending task with agent
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Pending Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Run this",
		AgentID:   &agent.ID,
		Priority:  5,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Create a backlog task (should NOT appear)
	backlog := &models.Task{
		ProjectID: project.ID,
		Title:     "Backlog Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "Later",
	}
	if err := taskRepo.Create(context.Background(), backlog); err != nil {
		t.Fatalf("creating backlog task: %v", err)
	}

	results, err := upcomingRepo.ListPendingActiveTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 pending active task, got %d", len(results))
	}
	if results[0].Task.Title != "Pending Active Task" {
		t.Fatalf("expected 'Pending Active Task', got %q", results[0].Task.Title)
	}
	if results[0].AgentName != "Test Agent" {
		t.Fatalf("expected agent name 'Test Agent', got %q", results[0].AgentName)
	}
}

func TestUpcomingRepo_ListRunningTasks_PromptPreviewBounded(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := createTestProject(t, projectRepo)

	shortPrompt := "short prompt"
	longPrompt := strings.Repeat("x", 500)

	shortTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Short Prompt Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    shortPrompt,
	}
	if err := taskRepo.Create(context.Background(), shortTask); err != nil {
		t.Fatalf("creating short prompt task: %v", err)
	}

	longTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Long Prompt Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    longPrompt,
	}
	if err := taskRepo.Create(context.Background(), longTask); err != nil {
		t.Fatalf("creating long prompt task: %v", err)
	}

	emptyTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Empty Prompt Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "",
	}
	if err := taskRepo.Create(context.Background(), emptyTask); err != nil {
		t.Fatalf("creating empty prompt task: %v", err)
	}

	results, err := upcomingRepo.ListRunningTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 running tasks, got %d", len(results))
	}

	byTitle := make(map[string]string)
	for _, r := range results {
		byTitle[r.Task.Title] = r.Task.Prompt
	}

	// Task with prompt shorter than the 200-char preview bound displays in full.
	if got := byTitle["Short Prompt Task"]; got != shortPrompt {
		t.Fatalf("expected full short prompt %q, got %q", shortPrompt, got)
	}

	// Task with prompt longer than the bound displays the same truncated text
	// that truncatePrompt(prompt, 200) would have produced against the full
	// original prompt.
	wantLong := longPrompt[:200]
	if got := byTitle["Long Prompt Task"]; got != wantLong {
		t.Fatalf("expected bounded prompt preview of length %d, got %q (len %d)", len(wantLong), got, len(got))
	}

	// Task with empty prompt renders the "no prompt" UI branch (empty string).
	if got := byTitle["Empty Prompt Task"]; got != "" {
		t.Fatalf("expected empty prompt to remain empty, got %q", got)
	}
}

func TestUpcomingRepo_ListPendingActiveTasks_PromptPreviewBounded(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)

	project := createTestProject(t, projectRepo)
	longPrompt := strings.Repeat("y", 300)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Pending Long Prompt",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    longPrompt,
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	results, err := upcomingRepo.ListPendingActiveTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 pending active task, got %d", len(results))
	}
	if got, want := results[0].Task.Prompt, longPrompt[:200]; got != want {
		t.Fatalf("expected bounded prompt preview %q, got %q", want, got)
	}
}

func TestUpcomingRepo_ListUpcomingScheduledTasks_PromptPreviewAndScheduleFields(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)

	project := createTestProject(t, projectRepo)
	ctx := context.Background()
	now := time.Now().UTC()
	longPrompt := strings.Repeat("z", 400)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Scheduled Long Prompt",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    longPrompt,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	nextRun := now.Add(1 * time.Hour)
	sched := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          now,
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &nextRun,
	}
	if err := scheduleRepo.Create(ctx, sched); err != nil {
		t.Fatalf("creating schedule: %v", err)
	}

	results, err := upcomingRepo.ListUpcomingScheduledTasks(ctx, project.ID, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 scheduled task, got %d", len(results))
	}

	// Bounded prompt projection.
	if got, want := results[0].Task.Prompt, longPrompt[:200]; got != want {
		t.Fatalf("expected bounded prompt preview %q, got %q", want, got)
	}

	// Schedule fields still join correctly alongside the bounded prompt projection.
	if results[0].Schedule == nil {
		t.Fatalf("expected schedule to be populated")
	}
	if results[0].Schedule.RepeatType != models.RepeatDaily {
		t.Fatalf("expected repeat type daily, got %q", results[0].Schedule.RepeatType)
	}
	if results[0].Schedule.RepeatInterval != 1 {
		t.Fatalf("expected repeat interval 1, got %d", results[0].Schedule.RepeatInterval)
	}
	if !results[0].Schedule.Enabled {
		t.Fatalf("expected schedule enabled")
	}
	if results[0].NextRun == nil || !results[0].NextRun.Equal(nextRun) {
		t.Fatalf("expected next run %v, got %v", nextRun, results[0].NextRun)
	}
}

func TestUpcomingRepo_ListRecentExecutions(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)

	project := createTestProject(t, projectRepo)
	agent := createTestAgent(t, agentRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Completed Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "Do it",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Create a completed execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Do it",
	}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatalf("creating execution: %v", err)
	}
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "Done!", "", 100, 5000); err != nil {
		t.Fatalf("completing execution: %v", err)
	}

	since := time.Now().UTC().Add(-1 * time.Hour)
	results, err := upcomingRepo.ListRecentExecutions(context.Background(), project.ID, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(results))
	}
	if results[0].TaskTitle != "Completed Task" {
		t.Fatalf("expected task title 'Completed Task', got %q", results[0].TaskTitle)
	}
	if results[0].AgentName != "Test Agent" {
		t.Fatalf("expected agent name 'Test Agent', got %q", results[0].AgentName)
	}
}

func TestUpcomingRepo_ListRecentExecutions_NullAgentConfigID(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)

	project := createTestProject(t, projectRepo)
	agent := createTestAgent(t, agentRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Historical Task",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "Reflect on this",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Reflect on this",
	}
	if err := execRepo.Create(context.Background(), exec); err != nil {
		t.Fatalf("creating execution: %v", err)
	}
	if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "Done", "", 0, 1000); err != nil {
		t.Fatalf("completing execution: %v", err)
	}
	if err := agentRepo.Delete(context.Background(), agent.ID); err != nil {
		t.Fatalf("deleting agent config: %v", err)
	}

	results, err := upcomingRepo.ListRecentExecutions(context.Background(), project.ID, time.Now().UTC().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("expected NULL agent_config_id to scan without error, got %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(results))
	}
	if results[0].Execution.AgentConfigID != "" {
		t.Fatalf("expected empty agent config ID fallback, got %q", results[0].Execution.AgentConfigID)
	}
	if results[0].AgentName != "" {
		t.Fatalf("expected empty agent name for missing config, got %q", results[0].AgentName)
	}
}

func TestUpcomingRepo_GetHistorySummary(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)

	project := createTestProject(t, projectRepo)
	agent := createTestAgent(t, agentRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do it",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Create 2 successful and 1 failed execution
	for i := 0; i < 2; i++ {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    "Do it",
		}
		if err := execRepo.Create(context.Background(), exec); err != nil {
			t.Fatalf("creating execution: %v", err)
		}
		if err := execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "Done", "", 100, 3000); err != nil {
			t.Fatalf("completing execution: %v", err)
		}
	}

	failedExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Do it",
	}
	if err := execRepo.Create(context.Background(), failedExec); err != nil {
		t.Fatalf("creating execution: %v", err)
	}
	if err := execRepo.Complete(context.Background(), failedExec.ID, models.ExecFailed, "", "error occurred", 50, 1000); err != nil {
		t.Fatalf("completing execution: %v", err)
	}

	since := time.Now().UTC().Add(-1 * time.Hour)
	summary, err := upcomingRepo.GetHistorySummary(context.Background(), project.ID, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.TotalExecutions != 3 {
		t.Fatalf("expected 3 total executions, got %d", summary.TotalExecutions)
	}
	if summary.SuccessCount != 2 {
		t.Fatalf("expected 2 successes, got %d", summary.SuccessCount)
	}
	if summary.FailureCount != 1 {
		t.Fatalf("expected 1 failure, got %d", summary.FailureCount)
	}
}

func TestUpcomingRepo_GetTaskSummary(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)

	project := createTestProject(t, projectRepo)
	ctx := context.Background()
	now := time.Now().UTC()

	// Create tasks with different categories, statuses, and priorities
	tasks := []models.Task{
		{ProjectID: project.ID, Title: "Active Pending Urgent", Category: models.CategoryActive, Status: models.StatusPending, Priority: 4, Prompt: "p"},
		{ProjectID: project.ID, Title: "Active Running High", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 3, Prompt: "p"},
		{ProjectID: project.ID, Title: "Backlog Normal", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, Prompt: "p"},
		{ProjectID: project.ID, Title: "Backlog Low", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 1, Prompt: "p"},
		{ProjectID: project.ID, Title: "Completed Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, Prompt: "p"},
		{ProjectID: project.ID, Title: "Failed Task", Category: models.CategoryActive, Status: models.StatusFailed, Priority: 2, Prompt: "p"},
		{ProjectID: project.ID, Title: "Scheduled Task", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2, Prompt: "p"},
	}

	for i := range tasks {
		if err := taskRepo.Create(ctx, &tasks[i]); err != nil {
			t.Fatalf("creating task %q: %v", tasks[i].Title, err)
		}
	}

	// Create a schedule for the scheduled task with next_run in the past (overdue)
	pastTime := now.Add(-1 * time.Hour)
	overdueSchedule := &models.Schedule{
		TaskID:         tasks[6].ID,
		RunAt:          now.Add(-2 * time.Hour),
		RepeatType:     models.RepeatDaily,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &pastTime,
	}
	if err := scheduleRepo.Create(ctx, overdueSchedule); err != nil {
		t.Fatalf("creating overdue schedule: %v", err)
	}

	summary, err := upcomingRepo.GetTaskSummary(ctx, project.ID, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check priority counts (only non-completed/cancelled tasks in active/backlog/scheduled)
	if summary.UrgentCount != 1 {
		t.Errorf("expected UrgentCount=1, got %d", summary.UrgentCount)
	}
	if summary.HighCount != 1 {
		t.Errorf("expected HighCount=1, got %d", summary.HighCount)
	}
	// Normal: backlog(1) + failed(1) + scheduled(1) = 3
	if summary.NormalCount != 3 {
		t.Errorf("expected NormalCount=3, got %d", summary.NormalCount)
	}
	if summary.LowCount != 1 {
		t.Errorf("expected LowCount=1, got %d", summary.LowCount)
	}
	// TotalPending = sum of priority counts = 1+1+3+1 = 6
	if summary.TotalPending != 6 {
		t.Errorf("expected TotalPending=6, got %d", summary.TotalPending)
	}

	// Check status counts
	// Pending: active(1) + backlog(2) + scheduled(1) = 4
	if summary.PendingCount != 4 {
		t.Errorf("expected PendingCount=4, got %d", summary.PendingCount)
	}
	if summary.RunningCount != 1 {
		t.Errorf("expected RunningCount=1, got %d", summary.RunningCount)
	}
	if summary.CompletedCount != 1 {
		t.Errorf("expected CompletedCount=1, got %d", summary.CompletedCount)
	}
	if summary.FailedCount != 1 {
		t.Errorf("expected FailedCount=1, got %d", summary.FailedCount)
	}

	// Check category counts
	// Active: 3 (pending + running + failed)
	if summary.ActiveCount != 3 {
		t.Errorf("expected ActiveCount=3, got %d", summary.ActiveCount)
	}
	if summary.BacklogCount != 2 {
		t.Errorf("expected BacklogCount=2, got %d", summary.BacklogCount)
	}
	if summary.ScheduledCount != 1 {
		t.Errorf("expected ScheduledCount=1, got %d", summary.ScheduledCount)
	}

	// Check overdue count
	if summary.OverdueCount != 1 {
		t.Errorf("expected OverdueCount=1, got %d", summary.OverdueCount)
	}
}

func TestUpcomingRepo_GetTaskSummary_Empty(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)
	now := time.Now().UTC()

	summary, err := upcomingRepo.GetTaskSummary(context.Background(), project.ID, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.TotalPending != 0 {
		t.Errorf("expected TotalPending=0, got %d", summary.TotalPending)
	}
	if summary.OverdueCount != 0 {
		t.Errorf("expected OverdueCount=0, got %d", summary.OverdueCount)
	}
}

func TestUpcomingRepo_EmptyProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	upcomingRepo := NewUpcomingRepo(db)
	projectRepo := NewProjectRepo(db)

	project := createTestProject(t, projectRepo)

	// All queries should return empty results without errors
	running, err := upcomingRepo.ListRunningTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(running) != 0 {
		t.Fatalf("expected 0 running tasks, got %d", len(running))
	}

	pending, err := upcomingRepo.ListPendingActiveTasks(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending tasks, got %d", len(pending))
	}

	since := time.Now().UTC().Add(-1 * time.Hour)
	execs, err := upcomingRepo.ListRecentExecutions(context.Background(), project.ID, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(execs) != 0 {
		t.Fatalf("expected 0 executions, got %d", len(execs))
	}

	summary, err := upcomingRepo.GetHistorySummary(context.Background(), project.ID, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary.TotalExecutions != 0 {
		t.Fatalf("expected 0 total, got %d", summary.TotalExecutions)
	}
}
