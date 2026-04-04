package repository

import (
	"context"
	"encoding/json"
	"testing"

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
		Name:        "Test Agent",
		Provider:    "anthropic",
		Model:       "claude-3-5-sonnet-20241022",
		IsDefault:   true,
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
		Name:        "Test Agent",
		Provider:    "anthropic",
		Model:       "claude-3-5-sonnet-20241022",
		IsDefault:   true,
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
		Name:        "Test Agent",
		Provider:    "anthropic",
		Model:       "claude-3-5-sonnet-20241022",
		IsDefault:   true,
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
		Name:        "Test Agent",
		Provider:    "anthropic",
		Model:       "claude-3-5-sonnet-20241022",
		IsDefault:   true,
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
		Name:        "Test Agent",
		Provider:    "anthropic",
		Model:       "claude-3-5-sonnet-20241022",
		IsDefault:   true,
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
