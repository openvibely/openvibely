package service

import (
	"context"
	"strings"
	"testing"
	"time"

	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMService_CallAgentDirectStreaming_TaskFollowupFlag(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)
	if defaultAgent == nil {
		t.Fatal("no default agent found")
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "Task Followup Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Fix the bug in main.go",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: defaultAgent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Please also update the tests",
		IsFollowup:    true,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	chatHistory := make([]models.Execution, 0)

	agent := *defaultAgent
	agent.Provider = "unsupported"

	_, _, err := svc.CallAgentDirectStreaming(ctx, "fix the tests", nil, agent, exec.ID, chatHistory, "", "", false)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error, got: %v", err)
	}

	_, _, err = svc.CallAgentDirectStreaming(ctx, "fix the tests", nil, agent, exec.ID, chatHistory, "", "", true)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error with isTaskFollowup=true, got: %v", err)
	}
}

func TestChatHistoryIncludesFailedExecutions(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat History Test",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Tell me about task chaining",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	output := "Task chaining would allow one task to trigger another. For example, an Opus plan task could kick off a Sonnet code task."
	if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, output, "max turns exceeded", 0, 5000); err != nil {
		t.Fatalf("failed to complete execution: %v", err)
	}

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}

	history := []models.Execution{*updatedExec}
	var historyText strings.Builder
	for _, h := range history {
		if h.PromptSent != "" {
			historyText.WriteString("User: ")
			historyText.WriteString(h.PromptSent)
			historyText.WriteString("\n")
		}

		if h.Output != "" && (h.Status == models.ExecCompleted || h.Status == models.ExecFailed) {
			cleaned := llmoutput.CleanChatOutput(h.Output)
			if cleaned != "" {
				historyText.WriteString("Assistant: ")
				historyText.WriteString(cleaned)
				historyText.WriteString("\n")
			}
		}
	}

	result := historyText.String()
	if !strings.Contains(result, "Tell me about task chaining") {
		t.Error("history should contain user prompt from failed execution")
	}
	if !strings.Contains(result, "Task chaining would allow one task to trigger another") {
		t.Error("history should contain assistant output from failed execution")
	}
}
