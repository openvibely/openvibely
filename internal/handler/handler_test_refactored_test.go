package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// TestHandler_GetTask_HTMX_Refactored demonstrates the refactored test using new helpers
func TestHandler_GetTask_HTMX_Refactored(t *testing.T) {
	tc := NewTestContext(t)

	// Create test data using fluent builders
	project := tc.CreateProject().WithName("Test Project").Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Test Task").
		WithPriority(1).
		Build()

	// Make HTMX request using fluent HTTP client
	rec := tc.HTMX().Get("/tasks/" + task.ID).Execute()

	// Assert using fluent assertions
	tc.Assert(rec).
		StatusCode(http.StatusOK).
		Contains("task-detail-content").
		Contains(task.Title).
		NotContains("task_detail_modal")
}

// TestHandler_GetTaskExecutions_Refactored demonstrates refactored execution test
func TestHandler_GetTaskExecutions_Refactored(t *testing.T) {
	tc := NewTestContext(t)

	// Create test data with builders
	agent := tc.CreateLLMConfig().
		WithName("test-agent").
		WithProvider(models.ProviderAnthropic).
		WithModel("claude-3-5-sonnet-20241022").
		Build()

	project := tc.CreateProject().WithName("Test Project").Build()

	task := tc.CreateTask(project.ID).
		WithTitle("Test Task").
		WithPrompt("Test Prompt").
		WithStatus(models.StatusRunning).
		Build()

	tc.CreateExecution(task.ID, agent.ID).
		WithOutput("Working on it...").
		Build()

	// Make request and assert
	rec := tc.HTMX().Get(fmt.Sprintf("/tasks/%s/executions", task.ID)).Execute()

	tc.Assert(rec).
		StatusCode(http.StatusOK).
		Contains("Execution History").
		Contains(`id="task-execution-history"`).
		Contains("hx-trigger").
		Contains("/executions")

	// Custom assertion for execution status
	body := rec.Body.String()
	if !containsAny(body, "loading-spinner", "Model is working") {
		t.Errorf("expected execution status in response")
	}
}

// TestHandler_GetTask_StatusIndicator_Refactored shows test case table refactoring
func TestHandler_GetTask_StatusIndicator_Refactored(t *testing.T) {
	tc := NewTestContext(t)
	agent := tc.CreateLLMConfig().Build()
	project := tc.CreateProject().Build()

	cases := []struct {
		name       string
		taskSetup  func(*TaskBuilder) *TaskBuilder
		execSetup  func(*ExecutionBuilder) *ExecutionBuilder
		wantTexts  []string
		wantAbsent []string
	}{
		{
			name: "completed_shows_success",
			taskSetup: func(b *TaskBuilder) *TaskBuilder {
				return b.WithStatus(models.StatusCompleted).
					WithCategory(models.CategoryCompleted)
			},
			execSetup: func(b *ExecutionBuilder) *ExecutionBuilder {
				return b.WithStatus(models.ExecCompleted).
					WithOutput("Done!")
			},
			wantTexts:  []string{"Task completed", "text-success"},
			wantAbsent: nil,
		},
		{
			name: "failed_shows_error",
			taskSetup: func(b *TaskBuilder) *TaskBuilder {
				return b.WithStatus(models.StatusFailed).
					WithCategory(models.CategoryCompleted)
			},
			execSetup: func(b *ExecutionBuilder) *ExecutionBuilder {
				return b.WithStatus(models.ExecFailed).
					WithError("something went wrong")
			},
			wantTexts:  []string{"Task failed", "text-error"},
			wantAbsent: nil,
		},
		{
			name: "running_no_indicator",
			taskSetup: func(b *TaskBuilder) *TaskBuilder {
				return b.WithStatus(models.StatusRunning).
					WithCategory(models.CategoryActive)
			},
			execSetup: func(b *ExecutionBuilder) *ExecutionBuilder {
				return b.WithStatus(models.ExecRunning)
			},
			wantTexts:  nil,
			wantAbsent: []string{"Task completed", "Task failed"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Create task with custom setup
			taskBuilder := testCase.taskSetup(tc.CreateTask(project.ID).
				WithTitle(testCase.name).
				WithPrompt("Do something").
				WithPriority(2))
			task := taskBuilder.Build()

			// Create execution if task is not pending
			if task.Status != models.StatusPending {
				execBuilder := testCase.execSetup(tc.CreateExecution(task.ID, agent.ID))
				execBuilder.Build()
			}

			// Make request and verify
			rec := tc.HTMX().Get("/tasks/" + task.ID + "/thread").Execute()

			assertions := tc.Assert(rec).StatusCode(http.StatusOK)

			for _, want := range testCase.wantTexts {
				assertions.Contains(want)
			}
			for _, absent := range testCase.wantAbsent {
				assertions.NotContains(absent)
			}
		})
	}
}

// TestCreateModel_AnthropicAPIKey_Refactored shows form submission refactoring
func TestCreateModel_AnthropicAPIKey_Refactored(t *testing.T) {
	tc := NewTestContext(t)

	form := url.Values{
		"name":                {"My Anthropic Model"},
		"provider":            {"anthropic"},
		"anthropic_auth_type": {"api_key"},
		"model":               {"claude-sonnet-4-5-20250929"},
		"api_key":             {"sk-ant-test-key"},
		"max_tokens":          {"4096"},
		"temperature":         {"0"},
	}

	// Make regular (non-HTMX) POST request
	rec := tc.HTTP().Post("/models").WithForm(form).Execute()

	// Assert redirect
	tc.Assert(rec).
		StatusCode(http.StatusSeeOther).
		Location("/models")

	// Verify model was created
	configs, err := tc.llmConfigRepo.List(testContext())
	if err != nil {
		t.Fatalf("list error: %v", err)
	}

	// Find our created model
	var found *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "My Anthropic Model" {
			found = &configs[i]
			break
		}
	}

	if found == nil {
		t.Fatal("created model not found")
	}
	if found.Provider != models.ProviderAnthropic {
		t.Errorf("provider = %q, want %q", found.Provider, models.ProviderAnthropic)
	}
	if found.APIKey != "sk-ant-test-key" {
		t.Errorf("api_key not saved correctly")
	}
}

// TestHandler_TasksPage_NoDialogContainer_Refactored shows simple GET test refactoring
func TestHandler_TasksPage_NoDialogContainer_Refactored(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()

	rec := tc.HTTP().Get("/tasks?project_id=" + project.ID).Execute()

	tc.Assert(rec).
		StatusCode(http.StatusOK).
		NotContains(`id="task-dialog-container"`).
		NotContains(`task_detail_modal`)
}

// TestHandler_ScheduleCreation_Refactored demonstrates schedule testing
func TestHandler_ScheduleCreation_Refactored(t *testing.T) {
	tc := NewTestContext(t)

	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithCategory(models.CategoryScheduled).
		Build()

	// Create schedule for tomorrow at 3 PM
	runAt := time.Now().Add(24 * time.Hour).Round(time.Hour).Add(15 * time.Hour)

	schedule := tc.CreateSchedule(task.ID).
		WithRunAt(runAt).
		WithRepeatType(models.RepeatDaily).
		WithRepeatInterval(2).
		Build()

	// Verify schedule properties
	if schedule.RepeatType != models.RepeatDaily {
		t.Errorf("expected daily repeat, got %v", schedule.RepeatType)
	}
	if schedule.RepeatInterval != 2 {
		t.Errorf("expected interval 2, got %d", schedule.RepeatInterval)
	}
}

// TestHandler_TaskThreadFollowup_Refactored shows complex interaction test
func TestHandler_TaskThreadFollowup_Refactored(t *testing.T) {
	tc := NewTestContext(t)

	// Setup test data
	agent := tc.CreateLLMConfig().Build()
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Completed Task").
		WithStatus(models.StatusCompleted).
		WithCategory(models.CategoryCompleted).
		Build()

	// Create previous execution
	tc.CreateExecution(task.ID, agent.ID).
		WithStatus(models.ExecCompleted).
		WithOutput("Task completed successfully").
		Build()

	// Send follow-up message
	form := url.Values{
		"message": {"Can you explain what you did?"},
	}

	rec := tc.HTMX().Post(fmt.Sprintf("/tasks/%s/thread", task.ID)).
		WithForm(form).
		Execute()

	// Verify response
	tc.Assert(rec).
		StatusCode(http.StatusOK).
		Contains("chat-bubble-user-msg").
		Contains("Can you explain what you did?")

	// Verify task was reactivated (status is "queued" because the transition to
	// "running" happens asynchronously in processStreamingResponse goroutine)
	updatedTask, err := tc.taskRepo.GetByID(testContext(), task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}

	if updatedTask.Status != models.StatusQueued {
		t.Errorf("expected task to be queued after follow-up, got %v", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryActive {
		t.Errorf("expected task to be active after follow-up, got %v", updatedTask.Category)
	}
}

// Helper functions
func testContext() context.Context {
	return context.Background()
}

func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
