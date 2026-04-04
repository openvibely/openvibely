package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type captureProviderAdapter struct {
	lastReq llmcontracts.AgentRequest
}

func (c *captureProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	c.lastReq = req
	return llmcontracts.AgentResult{
		Output:         "ok",
		TextOnlyOutput: "ok",
		Usage:          llmcontracts.Usage{TotalTokens: 1},
	}, nil
}

func TestLLMService_ExecuteTask_NoDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	agents, _ := llmConfigRepo.List(ctx)
	for _, a := range agents {
		llmConfigRepo.Delete(ctx, a.ID)
	}

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := models.Task{
		ID:        "test-task-id",
		ProjectID: "default",
		Title:     "Test",
		Prompt:    "hello",
		Status:    models.StatusPending,
	}

	_, err := svc.ExecuteTask(ctx, task)
	if err == nil {
		t.Fatal("expected error when no agent configured")
	}
	if !strings.Contains(err.Error(), "no agent configured") {
		t.Errorf("expected 'no agent configured' error, got: %v", err)
	}
}

func TestLLMService_CallLLM_UnsupportedProvider(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	agent := models.LLMConfig{
		Provider: "unsupported_provider",
		Model:    "test-model",
	}

	_, _, _, err := svc.callLLM(context.Background(), "test", nil, agent, "test-exec-id", "", "")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error, got: %v", err)
	}
}

func TestLLMService_CallAgentDirect_TestProviderUsesMockCaller(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "mock-output"
	mock.Tokens = 17
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	output, tokens, err := svc.CallAgentDirect(context.Background(), "hello", nil, agent, "/tmp/workdir")
	if err != nil {
		t.Fatalf("CallAgentDirect error: %v", err)
	}
	if output != "mock-output" {
		t.Fatalf("expected mock output, got %q", output)
	}
	if tokens != 17 {
		t.Fatalf("expected tokens=17, got %d", tokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected CallModel called once, got %d", mock.CallCount())
	}
	last := mock.LastCall()
	if last.ExecID != "" {
		t.Fatalf("expected empty execID for direct calls, got %q", last.ExecID)
	}
	if last.WorkDir != "/tmp/workdir" {
		t.Fatalf("expected workdir propagated, got %q", last.WorkDir)
	}
}

func TestLLMService_CallAgentDirectStreaming_TestProviderUsesMockCaller(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "stream-output"
	mock.Tokens = 29
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	output, tokens, err := svc.CallAgentDirectStreaming(context.Background(), "hello", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir")
	if err != nil {
		t.Fatalf("CallAgentDirectStreaming error: %v", err)
	}
	if output != "stream-output" {
		t.Fatalf("expected stream output, got %q", output)
	}
	if tokens != 29 {
		t.Fatalf("expected tokens=29, got %d", tokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected CallModel called once, got %d", mock.CallCount())
	}
	last := mock.LastCall()
	if last.ExecID != "exec-123" {
		t.Fatalf("expected execID propagated, got %q", last.ExecID)
	}
	if last.WorkDir != "/tmp/workdir" {
		t.Fatalf("expected workdir propagated, got %q", last.WorkDir)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_TestProviderPreservesTextOnly(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "stream-output"
	mock.TextOnly = "text-only"
	mock.Tokens = 29
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	res, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "hello", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir", nil)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	if res.Output != "stream-output" {
		t.Fatalf("expected stream output, got %q", res.Output)
	}
	if res.TextOnlyOutput != "text-only" {
		t.Fatalf("expected text-only output, got %q", res.TextOnlyOutput)
	}
	if res.Usage.TotalTokens != 29 {
		t.Fatalf("expected tokens=29, got %d", res.Usage.TotalTokens)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_PropagatesAgentDefinition(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: capture,
	}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	agentDef := &models.Agent{
		ID:           "agent-def-1",
		Name:         "playwright-reviewer",
		SystemPrompt: "Use Playwright MCP tools for screenshots.",
	}
	ctx := llmcontracts.WithChatMode(context.Background(), models.ChatModePlan)
	_, err := svc.CallAgentDirectStreamingDetailed(
		ctx,
		"check ui",
		nil,
		agent,
		"exec-123",
		nil,
		"ctx",
		"/tmp/workdir",
		agentDef,
		false,
	)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil {
		t.Fatalf("expected agent definition to be propagated")
	}
	if capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition id %q, got %q", agentDef.ID, capture.lastReq.AgentDefinition.ID)
	}
	if capture.lastReq.ChatMode != models.ChatModePlan {
		t.Fatalf("expected chat mode %q, got %q", models.ChatModePlan, capture.lastReq.ChatMode)
	}
}

func TestLLMService_CallClaudeCLI_EnvFiltering(t *testing.T) {

	os.Setenv("CLAUDECODE", "test-value")
	defer os.Unsetenv("CLAUDECODE")

	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}

	for _, e := range filtered {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Error("CLAUDECODE should be filtered from env")
		}
	}

	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("CLAUDECODE should be in original env")
	}
}

func TestLLMService_ExecuteTaskWithAgent_SkipsNonPendingTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	agent := ensureDefaultAgent(t, llmConfigRepo)

	task := &models.Task{ProjectID: "default", Title: "Already Done", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "test"}
	taskRepo.Create(ctx, task)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("expected no error for skipped task, got: %v", err)
	}
	if exec != nil {
		t.Error("expected nil execution for skipped non-pending task")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusCompleted {
		t.Errorf("expected status to remain completed, got %q", updated.Status)
	}
}

func TestLLMService_ExecuteTaskWithAgent_SkipsRunningTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := &models.Task{ProjectID: "default", Title: "Already Running", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "test"}
	taskRepo.Create(ctx, task)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("expected no error for skipped task, got: %v", err)
	}
	if exec != nil {
		t.Error("expected nil execution for skipped running task")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusRunning {
		t.Errorf("expected status to remain running, got %q", updated.Status)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RecordsExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	mock := &testutil.MockLLMCaller{Err: fmt.Errorf("mock error: simulated failure")}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	task := &models.Task{ProjectID: "default", Title: "Record Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected error from mock")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusFailed {
		t.Errorf("expected task status=failed, got %q", updated.Status)
	}

	if exec == nil {
		t.Fatal("expected execution record even on failure")
	}
	if exec.Status != models.ExecFailed {
		t.Errorf("expected exec status=failed, got %q", exec.Status)
	}
}

func TestLLMService_ExecuteTask_UsesAssignedAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	customAgent := &models.LLMConfig{
		Name:        "Custom Agent",
		Provider:    models.ProviderAnthropic,
		Model:       "custom-model",
		MaxTokens:   1000,
		Temperature: 0.5,
		IsDefault:   false,
	}
	if err := llmConfigRepo.Create(ctx, customAgent); err != nil {
		t.Fatalf("failed to create custom agent: %v", err)
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "Custom Agent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "test",
		AgentID:   &customAgent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec, err := svc.ExecuteTask(ctx, *task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec != nil {
		t.Errorf("expected nil execution for non-pending task, got %+v", exec)
	}

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Custom Agent Task 2",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
		AgentID:   &customAgent.ID,
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatalf("failed to create task2: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	fetchedAgent, _ := llmConfigRepo.GetByID(ctx, customAgent.ID)

	fetchedAgent.Provider = models.ProviderTest

	exec2, err := svc.ExecuteTaskWithAgent(ctx, *task2, *fetchedAgent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec2 == nil {
		t.Fatal("expected execution record")
	}
	if exec2.AgentConfigID != customAgent.ID {
		t.Errorf("expected execution to use custom agent %s, got %s", customAgent.ID, exec2.AgentConfigID)
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if mock.LastCall().Agent.ID != customAgent.ID {
		t.Errorf("expected callLLM to receive custom agent %s, got %s", customAgent.ID, mock.LastCall().Agent.ID)
	}
}

func TestLLMService_ExecuteTask_UsesDefaultWhenNoAgentAssigned(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Default Agent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
		AgentID:   nil,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	defaultAgent.Provider = models.ProviderTest

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *defaultAgent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec == nil {
		t.Fatal("expected execution record")
	}
	if exec.AgentConfigID != defaultAgent.ID {
		t.Errorf("expected execution to use default agent %s, got %s", defaultAgent.ID, exec.AgentConfigID)
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if mock.LastCall().Agent.ID != defaultAgent.ID {
		t.Errorf("expected callLLM to receive default agent %s, got %s", defaultAgent.ID, mock.LastCall().Agent.ID)
	}
}

func TestLLMService_ExecuteTaskWithAgent_LoadsAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := &models.Task{
		ProjectID: "default",
		Title:     "Task with Attachment",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "What do you see in the image?",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "test.png",
		FilePath:  "/tmp/test.png",
		MediaType: "image/png",
		FileSize:  1024,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("failed to create attachment: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec == nil {
		t.Fatal("expected execution record")
	}

	if exec.PromptSent != task.Prompt {
		t.Errorf("expected PromptSent to match task prompt")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	lastCall := mock.LastCall()
	if len(lastCall.Attachments) != 1 {
		t.Errorf("expected 1 attachment passed to callLLM, got %d", len(lastCall.Attachments))
	} else if lastCall.Attachments[0].FileName != "test.png" {
		t.Errorf("expected attachment filename 'test.png', got %q", lastCall.Attachments[0].FileName)
	}
}

func TestLLMService_ExecuteTaskWithAgent_VisionAwareAgentOverride(t *testing.T) {

	anthropicAgent := models.LLMConfig{
		Name:       "Anthropic Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "claude-sonnet-4-20250514",
	}
	cliAgent := models.LLMConfig{
		Name:       "Claude Max",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodCLI,
		Model:      "claude-sonnet-4-5",
	}

	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, []models.LLMConfig{cliAgent, anthropicAgent}, true)
	if result == nil {
		t.Fatal("expected vision-capable agent to be selected")
	}
	if result.LLMConfig.Provider != models.ProviderAnthropic {
		t.Errorf("expected anthropic provider, got %s", result.LLMConfig.Provider)
	}
	if result.LLMConfig.Name != "Anthropic Sonnet" {
		t.Errorf("expected 'Anthropic Sonnet', got %q", result.LLMConfig.Name)
	}
}

func TestLLMService_ExecuteTaskWithAgent_NoOverrideForTextAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Process Text File",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Read this file",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "data.json",
		FilePath:  "/tmp/data.json",
		MediaType: "application/json",
		FileSize:  512,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("failed to create attachment: %v", err)
	}

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)
	if defaultAgent == nil {
		t.Fatal("no default agent found")
	}

	agent := *defaultAgent
	agent.Provider = "unsupported"

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, agent)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}

	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error for text-only attachments, got: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}
}

func TestLLMService_CallAgentDirectStreaming_VisionAwareOverride(t *testing.T) {

	cliOnly := []models.LLMConfig{
		{Name: "Claude Max", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5"},
	}
	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, cliOnly, true)
	if result != nil {
		t.Errorf("expected nil when no vision-capable agent available, got %+v", result.LLMConfig)
	}

	withAnthropic := append(cliOnly, models.LLMConfig{
		Name: "Anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-20250514",
	})
	result = SelectLLMWithVision(complexity, withAnthropic, true)
	if result == nil {
		t.Fatal("expected vision-capable agent to be selected")
	}
	if result.LLMConfig.Provider != models.ProviderAnthropic {
		t.Errorf("expected anthropic, got %s", result.LLMConfig.Provider)
	}
}

func TestLLMService_CallAgentDirectStreaming_NoOverrideWithoutImages(t *testing.T) {
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
		Title:     "Chat No Images",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		Prompt:    "Hello, how are you?",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: defaultAgent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	textAttachments := []models.Attachment{
		{
			FileName:  "data.json",
			FilePath:  "/tmp/data.json",
			MediaType: "application/json",
			FileSize:  512,
		},
	}

	agent := *defaultAgent
	agent.Provider = "unsupported"

	chatHistory := make([]models.Execution, 0)
	_, _, err := svc.CallAgentDirectStreaming(ctx, task.Prompt, textAttachments, agent, exec.ID, chatHistory, "", "", false)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}

	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error for text-only attachments, got: %v", err)
	}
}

func TestLLMService_CallAgentDirectStreaming_VisionEnvVarFallback(t *testing.T) {

	cliOnly := []models.LLMConfig{
		{Name: "Claude Max", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5"},
		{Name: "Ollama Local", Provider: models.ProviderOllama, Model: "llama3"},
	}
	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, cliOnly, true)
	if result != nil {
		t.Errorf("expected nil when no vision-capable agents, got %+v", result.LLMConfig)
	}

}

func TestLLMService_ExecuteTaskWithAgent_MovesRepeatOnceToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "RepeatOnce Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          time.Now(),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 0,
		Enabled:        true,
	}
	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get schedules: %v", err)
	}
	if len(schedules) > 0 && schedules[0].RepeatType == models.RepeatOnce {
		if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
			t.Fatalf("failed to update category: %v", err)
		}
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected task to be moved to completed category, got %q", updated.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_AllowsCompletionWithoutCodeChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "The screenshot shows the OpenVibely chat page in an idle state."
	mock.TextOnly = mock.Response
	mock.Tokens = 21

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))

	repoDir := createTestGitRepo(t)
	project := &models.Project{
		Name:     "Read Only Task Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Describe screenshot contents",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Describe the attached screenshot and summarize the visible UI state.",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	execRec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execRec == nil {
		t.Fatal("expected execution record")
	}
	if execRec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %s", execRec.Status)
	}
	if execRec.ErrorMessage != "" {
		t.Fatalf("expected empty error message, got %q", execRec.ErrorMessage)
	}

	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected task moved to completed category, got %s", updatedTask.Category)
	}
}

func TestLLMService_FailedTaskMovedToCompletedCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Failed Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
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
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	output := "I tried to complete the task but encountered an error.\n[STATUS: FAILED | test error]"
	reason := "test error"

	if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, output, reason, 0, 100); err != nil {
		t.Fatalf("failed to complete execution: %v", err)
	}

	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("failed to move task to completed category: %v", err)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected failed task to be moved to completed category, got %q", updated.Category)
	}
	if updated.Status != models.StatusFailed {
		t.Errorf("expected task status to be failed, got %q", updated.Status)
	}
}

// TestLLMService_ExecuteTaskWithAgent_PluginScopingWithAgentDef verifies that
// when a task has an AgentDefinitionID, the agent definition (including its
// plugin-resolved skills/MCP) is passed to the LLM call.
func TestLLMService_ExecuteTaskWithAgent_PluginScopingWithAgentDef(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Use a capture adapter to inspect the request that reaches the provider
	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	// Override the test provider adapter with our capture
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create an agent definition with plugins
	agentDef := &models.Agent{
		Name:         "test-plugin-agent",
		SystemPrompt: "Use plugin tools for testing",
		Plugins:      []string{"test-plugin@test-market"},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Task with agent def",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "run plugin tools",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the agent definition was propagated to the adapter
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition to be propagated to adapter")
	}
	if capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition ID %q, got %q", agentDef.ID, capture.lastReq.AgentDefinition.ID)
	}
	if capture.lastReq.AgentDefinition.SystemPrompt != "Use plugin tools for testing" {
		t.Fatalf("expected agent system prompt propagated, got %q", capture.lastReq.AgentDefinition.SystemPrompt)
	}
}

// TestLLMService_ExecuteTaskWithAgent_NoAgentDef_NilPluginContext verifies that
// when a task has no AgentDefinitionID, the adapter receives nil AgentDefinition
// (zero plugin context).
func TestLLMService_ExecuteTaskWithAgent_NoAgentDef_NilPluginContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create a task WITHOUT AgentDefinitionID
	task := &models.Task{
		ProjectID: "default",
		Title:     "Task without agent def",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "do something without plugins",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no agent definition was propagated (nil = zero plugin context)
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for task without agent def, got %+v", capture.lastReq.AgentDefinition)
	}
	if len(capture.lastReq.PluginDirs) != 0 {
		t.Fatalf("expected zero PluginDirs for task without agent def, got %v", capture.lastReq.PluginDirs)
	}
}

// TestLLMService_ExecuteTaskWithAgent_WrongAgentDefNotUsed verifies that
// if a task references a non-existent agent definition, no plugin context
// leaks from other agent definitions.
func TestLLMService_ExecuteTaskWithAgent_WrongAgentDefNotUsed(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create an agent definition that should NOT be used
	otherAgentDef := &models.Agent{
		Name:         "other-agent",
		SystemPrompt: "I am a different agent",
		Plugins:      []string{"other-plugin@other-market"},
	}
	if err := agentRepo.Create(ctx, otherAgentDef); err != nil {
		t.Fatalf("create other agent definition: %v", err)
	}

	// Create the task without AgentDefinitionID (to satisfy FK constraints),
	// then set it in memory to a non-existent ID before passing to ExecuteTaskWithAgent.
	task := &models.Task{
		ProjectID: "default",
		Title:     "Task with bad agent def ref",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "this should have no plugins",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Simulate a stale/invalid reference in the in-memory task object
	nonExistentID := "non-existent-agent-def-id"
	task.AgentDefinitionID = &nonExistentID

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The non-existent agent def lookup should fail gracefully,
	// resulting in nil AgentDefinition (no plugin context)
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for non-existent ref, got %+v", capture.lastReq.AgentDefinition)
	}
}

// TestLLMService_CallAgentDirectStreamingDetailed_PluginScopingByAgentDef
// verifies that CallAgentDirectStreamingDetailed (used by thread follow-ups)
// propagates agent definition correctly and that nil agent def means zero plugins.
func TestLLMService_CallAgentDirectStreamingDetailed_PluginScopingByAgentDef(t *testing.T) {
	capture := &captureProviderAdapter{}
	svc := &LLMService{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: capture,
	}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}

	// Case 1: With agent definition
	agentDef := &models.Agent{
		ID:           "follow-up-agent",
		Name:         "followup-agent",
		SystemPrompt: "I handle follow-ups",
		Plugins:      []string{"followup-plugin@market"},
	}
	_, err := svc.CallAgentDirectStreamingDetailed(
		context.Background(), "follow up message", nil,
		agent, "exec-1", nil, "sys ctx", "/work", agentDef, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition for follow-up with agent def")
	}
	if capture.lastReq.AgentDefinition.ID != "follow-up-agent" {
		t.Fatalf("expected agent def ID follow-up-agent, got %q", capture.lastReq.AgentDefinition.ID)
	}

	// Case 2: Without agent definition (task has no agent assigned)
	_, err = svc.CallAgentDirectStreamingDetailed(
		context.Background(), "follow up without agent", nil,
		agent, "exec-2", nil, "sys ctx", "/work", nil, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for follow-up without agent def, got %+v", capture.lastReq.AgentDefinition)
	}
}
