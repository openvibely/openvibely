package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestExecutionRepo_GetSuccessFailureRates(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)

	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Create executions with different statuses
	exec1 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt1",
	}
	if err := repo.Create(ctx, exec1); err != nil {
		t.Fatal(err)
	}

	exec2 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecFailed,
		PromptSent:    "prompt2",
	}
	if err := repo.Create(ctx, exec2); err != nil {
		t.Fatal(err)
	}

	exec3 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt3",
	}
	if err := repo.Create(ctx, exec3); err != nil {
		t.Fatal(err)
	}

	// Get success/failure rates
	rates, err := repo.GetSuccessFailureRates(ctx, project.ID, "day", "", "")
	if err != nil {
		t.Fatalf("GetSuccessFailureRates failed: %v", err)
	}

	if len(rates) == 0 {
		t.Fatal("Expected at least one rate entry")
	}

	// Check the rates
	rate := rates[0]
	if rate.TotalCount != 3 {
		t.Errorf("Expected TotalCount=3, got %d", rate.TotalCount)
	}
	if rate.SuccessCount != 2 {
		t.Errorf("Expected SuccessCount=2, got %d", rate.SuccessCount)
	}
	if rate.FailureCount != 1 {
		t.Errorf("Expected FailureCount=1, got %d", rate.FailureCount)
	}
	expectedRate := float64(2) / float64(3) * 100
	if rate.SuccessRate < expectedRate-0.1 || rate.SuccessRate > expectedRate+0.1 {
		t.Errorf("Expected SuccessRate≈%.2f, got %.2f", expectedRate, rate.SuccessRate)
	}
}

func TestExecutionRepo_GetAvgExecutionTimeByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)

	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create tasks
	task1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Task 1",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatal(err)
	}

	task2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Task 2",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatal(err)
	}

	// Create executions with different durations
	exec1 := &models.Execution{
		TaskID:        task1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt1",
	}
	if err := repo.Create(ctx, exec1); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, exec1.ID, models.ExecCompleted, "output", "", 100, 1000); err != nil {
		t.Fatal(err)
	}

	exec2 := &models.Execution{
		TaskID:        task1.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt2",
	}
	if err := repo.Create(ctx, exec2); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, exec2.ID, models.ExecCompleted, "output", "", 100, 2000); err != nil {
		t.Fatal(err)
	}

	// Get average execution times
	times, err := repo.GetAvgExecutionTimeByTask(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByTask failed: %v", err)
	}

	if len(times) == 0 {
		t.Fatal("Expected at least one time entry")
	}

	// Find task1 in results
	var task1Time *AvgExecutionTime
	for i := range times {
		if times[i].ID == task1.ID {
			task1Time = &times[i]
			break
		}
	}

	if task1Time == nil {
		t.Fatal("Task 1 not found in results")
	}

	expectedAvg := float64(1500) // (1000 + 2000) / 2
	if task1Time.AvgMs < expectedAvg-1 || task1Time.AvgMs > expectedAvg+1 {
		t.Errorf("Expected AvgMs≈%.2f, got %.2f", expectedAvg, task1Time.AvgMs)
	}
	if task1Time.Count != 2 {
		t.Errorf("Expected Count=2, got %d", task1Time.Count)
	}
	if task1Time.MinMs != 1000 {
		t.Errorf("Expected MinMs=1000, got %d", task1Time.MinMs)
	}
	if task1Time.MaxMs != 2000 {
		t.Errorf("Expected MaxMs=2000, got %d", task1Time.MaxMs)
	}
}

func TestExecutionRepo_GetExecutionTrendsByHour(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)

	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create a task
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Create executions
	exec1 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt1",
	}
	if err := repo.Create(ctx, exec1); err != nil {
		t.Fatal(err)
	}

	// Get trends
	trends, err := repo.GetExecutionTrendsByHour(ctx, project.ID, "", "")
	if err != nil {
		t.Fatalf("GetExecutionTrendsByHour failed: %v", err)
	}

	// Should have at least one trend entry
	if len(trends) == 0 {
		t.Fatal("Expected at least one trend entry")
	}

	// Total count should equal the number of executions we created
	totalCount := 0
	for _, trend := range trends {
		totalCount += trend.Count
		if trend.Hour < 0 || trend.Hour > 23 {
			t.Errorf("Invalid hour: %d", trend.Hour)
		}
	}
	if totalCount != 1 {
		t.Errorf("Expected total count=1, got %d", totalCount)
	}
}

func TestExecutionRepo_GetMostFrequentTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)

	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create tasks
	task1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Frequent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatal(err)
	}

	task2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Rare Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatal(err)
	}

	// Create executions (3 for task1, 1 for task2)
	for i := 0; i < 3; i++ {
		exec := &models.Execution{
			TaskID:        task1.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecCompleted,
			PromptSent:    "prompt",
		}
		if err := repo.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
	}

	exec := &models.Execution{
		TaskID:        task2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt",
	}
	if err := repo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}

	// Get most frequent tasks
	frequencies, err := repo.GetMostFrequentTasks(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetMostFrequentTasks failed: %v", err)
	}

	if len(frequencies) != 2 {
		t.Fatalf("Expected 2 frequencies, got %d", len(frequencies))
	}

	// First should be task1 (most frequent)
	if frequencies[0].TaskID != task1.ID {
		t.Errorf("Expected first task to be %s, got %s", task1.ID, frequencies[0].TaskID)
	}
	if frequencies[0].ExecutionCount != 3 {
		t.Errorf("Expected ExecutionCount=3 for task1, got %d", frequencies[0].ExecutionCount)
	}

	// Second should be task2
	if frequencies[1].TaskID != task2.ID {
		t.Errorf("Expected second task to be %s, got %s", task2.ID, frequencies[1].TaskID)
	}
	if frequencies[1].ExecutionCount != 1 {
		t.Errorf("Expected ExecutionCount=1 for task2, got %d", frequencies[1].ExecutionCount)
	}
}

func TestExecutionRepo_GetFailedTaskPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)

	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Test Project", RepoPath: "/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Create tasks
	task1 := &models.Task{
		ProjectID: project.ID,
		Title:     "Failing Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task1); err != nil {
		t.Fatal(err)
	}

	task2 := &models.Task{
		ProjectID: project.ID,
		Title:     "Successful Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test",
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatal(err)
	}

	// Create failed executions for task1
	for i := 0; i < 2; i++ {
		exec := &models.Execution{
			TaskID:        task1.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    "prompt",
		}
		if err := repo.Create(ctx, exec); err != nil {
			t.Fatal(err)
		}
		if err := repo.Complete(ctx, exec.ID, models.ExecFailed, "", "Test error", 0, 1000); err != nil {
			t.Fatal(err)
		}
	}

	// Create successful execution for task2
	exec := &models.Execution{
		TaskID:        task2.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "prompt",
	}
	if err := repo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}
	if err := repo.Complete(ctx, exec.ID, models.ExecCompleted, "success", "", 0, 1000); err != nil {
		t.Fatal(err)
	}

	// Get failed task patterns
	patterns, err := repo.GetFailedTaskPatterns(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns failed: %v", err)
	}

	// Should only have task1 (the failing one)
	if len(patterns) != 1 {
		t.Fatalf("Expected 1 pattern, got %d", len(patterns))
	}

	pattern := patterns[0]
	if pattern.TaskID != task1.ID {
		t.Errorf("Expected TaskID=%s, got %s", task1.ID, pattern.TaskID)
	}
	if pattern.FailureCount != 2 {
		t.Errorf("Expected FailureCount=2, got %d", pattern.FailureCount)
	}
	if pattern.LastError != "Test error" {
		t.Errorf("Expected LastError='Test error', got '%s'", pattern.LastError)
	}
}

func TestFailedTaskPatterns_SharedTaskLevelLatestErrorSemantics(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	execRepo := NewExecutionRepo(db)
	insightsRepo := NewInsightsRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Shared Failure Patterns", RepoPath: "/shared-failures"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherProject := &models.Project{Name: "Other Failure Patterns", RepoPath: "/other-failures"}
	if err := projectRepo.Create(ctx, otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	agent := &models.LLMConfig{Name: "Failure Agent", Provider: models.ProviderTest, Model: "test-model"}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	makeTask := func(projectID, title string) *models.Task {
		t.Helper()
		task := &models.Task{ProjectID: projectID, Title: title, Prompt: "prompt", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", title, err)
		}
		return task
	}
	createFailure := func(task *models.Task, errMsg, startedAt string) {
		t.Helper()
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "prompt"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create failed execution for %s: %v", task.Title, err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); err != nil {
			t.Fatalf("complete failed execution for %s: %v", task.Title, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, startedAt, exec.ID); err != nil {
			t.Fatalf("set started_at for %s: %v", task.Title, err)
		}
	}

	taskA := makeTask(project.ID, "Flaky task A")
	for _, errMsg := range []string{"old error A", "middle error A", "latest tie error A"} {
		createFailure(taskA, errMsg, "2026-01-03T10:00:00Z")
	}
	taskB := makeTask(project.ID, "Flaky task B")
	createFailure(taskB, "old error B", "2026-01-02T10:00:00Z")
	createFailure(taskB, "latest error B", "2026-01-02T11:00:00Z")
	taskC := makeTask(project.ID, "Rare fail C")
	createFailure(taskC, "only once C", "2026-01-01T10:00:00Z")

	otherTask := makeTask(otherProject.ID, "Other project noisy task")
	for i := 0; i < 5; i++ {
		createFailure(otherTask, "other project error", "2026-01-04T10:00:00Z")
	}

	counter.Reset()
	counter.SetEnabled(true)
	analyticsPatterns, err := execRepo.GetFailedTaskPatterns(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("analytics GetFailedTaskPatterns: %v", err)
	}
	insightPatterns, err := insightsRepo.GetFailedTaskPatterns(ctx, project.ID, 2)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("insights GetFailedTaskPatterns: %v", err)
	}

	statements := counter.Statements()
	if len(statements) != 2 {
		t.Fatalf("recorded statements: got %d, want 2: %#v", len(statements), statements)
	}
	if statements[0] != statements[1] {
		t.Fatalf("analytics and insights used different failed-task-pattern SQL\nanalytics: %s\ninsights: %s", statements[0], statements[1])
	}

	if len(analyticsPatterns) != 3 {
		t.Fatalf("analytics patterns: got %d, want 3: %#v", len(analyticsPatterns), analyticsPatterns)
	}
	if analyticsPatterns[0].TaskID != taskA.ID || analyticsPatterns[0].TaskTitle != taskA.Title || analyticsPatterns[0].FailureCount != 3 {
		t.Fatalf("first analytics pattern = %#v, want task A with 3 failures", analyticsPatterns[0])
	}
	if analyticsPatterns[0].LastError != "latest tie error A" {
		t.Fatalf("task A latest error: got %q, want latest tie error A", analyticsPatterns[0].LastError)
	}
	if _, err := time.Parse(time.RFC3339, analyticsPatterns[0].LastFailedAt); err != nil {
		t.Fatalf("analytics LastFailedAt %q is not RFC3339: %v", analyticsPatterns[0].LastFailedAt, err)
	}
	if analyticsPatterns[1].TaskID != taskB.ID || analyticsPatterns[1].FailureCount != 2 || analyticsPatterns[1].LastError != "latest error B" {
		t.Fatalf("second analytics pattern = %#v, want task B with latest error", analyticsPatterns[1])
	}
	if analyticsPatterns[2].TaskID != taskC.ID || analyticsPatterns[2].FailureCount != 1 {
		t.Fatalf("third analytics pattern = %#v, want below-threshold task C", analyticsPatterns[2])
	}

	if len(insightPatterns) != 2 {
		t.Fatalf("insight patterns: got %d, want 2: %#v", len(insightPatterns), insightPatterns)
	}
	for i, insightPattern := range insightPatterns {
		analyticsPattern := analyticsPatterns[i]
		if insightPattern.Title != analyticsPattern.TaskTitle || insightPattern.FailCount != analyticsPattern.FailureCount || insightPattern.LastError != analyticsPattern.LastError {
			t.Fatalf("insight pattern %d = %#v, want analytics projection %#v", i, insightPattern, analyticsPattern)
		}
	}

	limitedPatterns, err := execRepo.GetFailedTaskPatterns(ctx, project.ID, 2)
	if err != nil {
		t.Fatalf("analytics limited GetFailedTaskPatterns: %v", err)
	}
	if len(limitedPatterns) != 2 || limitedPatterns[0].TaskID != taskA.ID || limitedPatterns[1].TaskID != taskB.ID {
		t.Fatalf("limited analytics patterns = %#v, want task A then task B", limitedPatterns)
	}
}

// TestExecutionRepo_AnalyticsEmptyResults verifies that all analytics methods
// return empty slices (not nil) when no data exists. This is critical because
// nil slices marshal to JSON "null" instead of "[]", which causes the JavaScript
// frontend to crash when calling .map() on null.
func TestExecutionRepo_AnalyticsEmptyResults(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	projectRepo := NewProjectRepo(db)

	ctx := context.Background()

	// Create a project with no executions
	project := &models.Project{Name: "Empty Project", RepoPath: "/empty"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Test GetSuccessFailureRates returns empty slice, not nil
	rates, err := repo.GetSuccessFailureRates(ctx, project.ID, "day", "", "")
	if err != nil {
		t.Fatalf("GetSuccessFailureRates error: %v", err)
	}
	if rates == nil {
		t.Error("GetSuccessFailureRates returned nil, expected empty slice")
	}
	if len(rates) != 0 {
		t.Errorf("GetSuccessFailureRates expected 0 results, got %d", len(rates))
	}

	// Test GetAvgExecutionTimeByTask returns empty slice, not nil
	times, err := repo.GetAvgExecutionTimeByTask(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByTask error: %v", err)
	}
	if times == nil {
		t.Error("GetAvgExecutionTimeByTask returned nil, expected empty slice")
	}

	// Test GetAvgExecutionTimeByAgent returns empty slice, not nil
	agentTimes, err := repo.GetAvgExecutionTimeByAgent(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetAvgExecutionTimeByAgent error: %v", err)
	}
	if agentTimes == nil {
		t.Error("GetAvgExecutionTimeByAgent returned nil, expected empty slice")
	}

	// Test GetExecutionTrendsByHour returns empty slice, not nil
	trends, err := repo.GetExecutionTrendsByHour(ctx, project.ID, "", "")
	if err != nil {
		t.Fatalf("GetExecutionTrendsByHour error: %v", err)
	}
	if trends == nil {
		t.Error("GetExecutionTrendsByHour returned nil, expected empty slice")
	}

	// Test GetAgentUsageByProject returns empty slice, not nil
	usage, err := repo.GetAgentUsageByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetAgentUsageByProject error: %v", err)
	}
	if usage == nil {
		t.Error("GetAgentUsageByProject returned nil, expected empty slice")
	}

	// Test GetMostFrequentTasks returns empty slice, not nil
	freqs, err := repo.GetMostFrequentTasks(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetMostFrequentTasks error: %v", err)
	}
	if freqs == nil {
		t.Error("GetMostFrequentTasks returned nil, expected empty slice")
	}

	// Test GetFailedTaskPatterns returns empty slice, not nil
	patterns, err := repo.GetFailedTaskPatterns(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns error: %v", err)
	}
	if patterns == nil {
		t.Error("GetFailedTaskPatterns returned nil, expected empty slice")
	}

	// Verify JSON marshaling produces "[]" not "null" for all results
	// This is the actual bug: nil slices marshal to "null" which crashes JavaScript
	for _, tc := range []struct {
		name string
		data interface{}
	}{
		{"SuccessFailureRates", rates},
		{"AvgExecutionTimeByTask", times},
		{"AvgExecutionTimeByAgent", agentTimes},
		{"ExecutionTrendsByHour", trends},
		{"AgentUsageByProject", usage},
		{"MostFrequentTasks", freqs},
		{"FailedTaskPatterns", patterns},
	} {
		jsonBytes, err := json.Marshal(tc.data)
		if err != nil {
			t.Fatalf("json.Marshal(%s) error: %v", tc.name, err)
		}
		if string(jsonBytes) == "null" {
			t.Errorf("%s marshals to 'null', expected '[]'", tc.name)
		}
		if string(jsonBytes) != "[]" {
			t.Errorf("%s marshals to %q, expected '[]'", tc.name, string(jsonBytes))
		}
	}
}

// TestExecutionRepo_SuccessFailureRates_LocaltimePeriod verifies that
// GetSuccessFailureRates buckets executions by the server's local calendar
// day (via SQLite 'localtime'), not raw UTC. Regression test for the bug
// where strftime(?, e.started_at) used UTC, causing dates to show 6/7
// when the local date was 6/6.
func TestExecutionRepo_SuccessFailureRates_LocaltimePeriod(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "TZ Test Project", RepoPath: "/tz-test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	agent := &models.LLMConfig{Name: "TZ Agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "TZ Task",
		Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "x"}
	if err := repo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}

	// Override started_at to a known UTC instant; the expected period must be
	// the LOCAL date for that instant, not the UTC date.
	knownUTC := time.Now().UTC()
	expectedPeriod := knownUTC.In(time.Local).Format("2006-01-02")
	if _, err := db.ExecContext(ctx,
		`UPDATE executions SET started_at = ? WHERE id = ?`,
		knownUTC.Format("2006-01-02T15:04:05Z"), exec.ID,
	); err != nil {
		t.Fatalf("UPDATE started_at: %v", err)
	}

	rates, err := repo.GetSuccessFailureRates(ctx, project.ID, "day", "", "")
	if err != nil {
		t.Fatalf("GetSuccessFailureRates: %v", err)
	}
	if len(rates) == 0 {
		t.Fatal("expected at least one rate entry")
	}
	if rates[0].Period != expectedPeriod {
		t.Errorf("GetSuccessFailureRates period: got %q, want %q (local date for UTC %v)",
			rates[0].Period, expectedPeriod, knownUTC)
	}
}

// TestExecutionRepo_ExecutionTrendsByHour_LocaltimeHour verifies that
// GetExecutionTrendsByHour buckets executions by the server's local hour
// (via SQLite 'localtime'), not raw UTC hour.
func TestExecutionRepo_ExecutionTrendsByHour_LocaltimeHour(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Hour TZ Project", RepoPath: "/hour-tz"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	agent := &models.LLMConfig{Name: "Hour Agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Hour Task",
		Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "x"}
	if err := repo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}

	// Override started_at to a known UTC instant; the expected hour must be
	// the LOCAL hour for that instant.
	knownUTC := time.Now().UTC()
	expectedHour := knownUTC.In(time.Local).Hour()
	if _, err := db.ExecContext(ctx,
		`UPDATE executions SET started_at = ? WHERE id = ?`,
		knownUTC.Format("2006-01-02T15:04:05Z"), exec.ID,
	); err != nil {
		t.Fatalf("UPDATE started_at: %v", err)
	}

	trends, err := repo.GetExecutionTrendsByHour(ctx, project.ID, "", "")
	if err != nil {
		t.Fatalf("GetExecutionTrendsByHour: %v", err)
	}
	if len(trends) == 0 {
		t.Fatal("expected at least one trend entry")
	}
	if trends[0].Hour != expectedHour {
		t.Errorf("GetExecutionTrendsByHour hour: got %d, want %d (local hour for UTC %v)",
			trends[0].Hour, expectedHour, knownUTC)
	}
}

// TestExecutionRepo_FailedTaskPatterns_LastFailedAtISO8601 verifies that
// GetFailedTaskPatterns returns LastFailedAt as an ISO 8601 string
// (e.g. "2006-01-02T15:04:05Z") so that JavaScript's new Date() can parse
// it without returning "Invalid Date". Regression test for the bug where
// MAX(e.started_at) returned SQLite's bare "YYYY-MM-DD HH:MM:SS" format.
func TestExecutionRepo_FailedTaskPatterns_LastFailedAtISO8601(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "ISO Project", RepoPath: "/iso-test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	agent := &models.LLMConfig{Name: "ISO Agent", Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "ISO Task",
		Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecFailed, PromptSent: "x", ErrorMessage: "boom",
	}
	if err := repo.Create(ctx, exec); err != nil {
		t.Fatal(err)
	}

	patterns, err := repo.GetFailedTaskPatterns(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("GetFailedTaskPatterns: %v", err)
	}
	if len(patterns) == 0 {
		t.Fatal("expected at least one failed task pattern")
	}

	got := patterns[0].LastFailedAt
	// Must contain 'T' (ISO 8601 separator) and end with 'Z' so that
	// JavaScript's new Date(s) succeeds in all browsers.
	if len(got) == 0 {
		t.Fatal("LastFailedAt is empty")
	}
	if got[len(got)-1] != 'Z' {
		t.Errorf("LastFailedAt %q does not end with 'Z'; JavaScript new Date() will return Invalid Date", got)
	}
	if !strings.Contains(got, "T") {
		t.Errorf("LastFailedAt %q missing 'T' separator; JavaScript new Date() will return Invalid Date", got)
	}
	// Confirm it parses as a valid RFC3339 timestamp.
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("LastFailedAt %q does not parse as RFC3339: %v", got, err)
	}
}
