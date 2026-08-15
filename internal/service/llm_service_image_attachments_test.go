package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMService_ReconcileMissingTaskAttachmentsRemovesBrokenMetadata(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, events.NewBroadcaster())
	attachmentRepo := repository.NewAttachmentRepo(db)
	project := &models.Project{Name: "Broken attachment project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task with historical broken screenshot", Prompt: "still execute", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	missing := &models.Attachment{TaskID: task.ID, FileName: "Screenshot 2026-07-10 at 9.49.00\u202fPM.png", FilePath: filepath.Join(t.TempDir(), "missing.png"), MediaType: "image/png", FileSize: 10}
	if err := attachmentRepo.Create(ctx, missing); err != nil {
		t.Fatal(err)
	}

	svc := NewLLMService(repository.NewLLMConfigRepo(db), repository.NewExecutionRepo(db), taskRepo, projectRepo, nil, attachmentRepo)
	valid := svc.reconcileMissingTaskAttachments(ctx, task.ID, []models.Attachment{*missing})
	if len(valid) != 0 {
		t.Fatalf("expected broken attachment to be excluded from execution, got %#v", valid)
	}
	remaining, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected broken metadata cleanup, got %#v", remaining)
	}
}

func TestLLMService_ImageAttachments_TextOnlyAgentRoutesToVisionAgent(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create a text-only agent (no vision support)
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Compatible",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Create a vision-capable API agent
	apiAgent := &models.LLMConfig{
		Name:       "Claude API with Vision",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-api-key",
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, apiAgent); err != nil {
		t.Fatalf("Failed to create API agent: %v", err)
	}

	// Create task with image attachment
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze Screenshot",
		Prompt:    "What do you see in this screenshot?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		AgentID:   &textOnlyAgent.ID, // Explicitly use text-only agent
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file for testing
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "screenshot.png")
	if err := os.WriteFile(imgPath, []byte("fake PNG content"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  100,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Mock LLM caller
	mockCaller := testutil.NewMockLLMCaller()
	svc.SetLLMCaller(mockCaller)

	// Load the task with attachments
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	// Trigger vision routing
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "Test", task.ID)

	// Verify that the agent was switched to a vision-capable agent
	if !visionDecision.Changed {
		t.Errorf("Expected vision routing to switch agent, but it didn't. Reason: %s, Detail: %s", visionDecision.Reason, visionDecision.Detail)
	}

	if visionDecision.Agent.ID != apiAgent.ID {
		t.Errorf("Expected agent to be switched to API agent %s, but got %s", apiAgent.ID, visionDecision.Agent.ID)
	}

	// Now test the case where no vision agent is available
	// Delete the vision-capable agent
	if err := llmConfigRepo.Delete(ctx, apiAgent.ID); err != nil {
		t.Fatalf("Failed to delete API agent: %v", err)
	}

	// Clear any environment variable fallback
	oldAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "")
	defer os.Setenv("ANTHROPIC_API_KEY", oldAPIKey)

	// Try vision routing again
	visionDecision2 := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "Test", task.ID)

	// Verify that no agent was switched but a warning reason is provided
	if visionDecision2.Changed {
		t.Errorf("Expected no agent switch when no vision agents available, but agent was switched")
	}

	if visionDecision2.Reason != "no_vision_fallback_available" {
		t.Errorf("Expected reason 'no_vision_fallback_available', got %s", visionDecision2.Reason)
	}

	// The detail should explain the situation
	if !strings.Contains(visionDecision2.Detail, "vision-capable") {
		t.Errorf("Expected detail to mention vision-capable agents, got: %s", visionDecision2.Detail)
	}
}

func TestLLMService_ImageAttachments_VisionRouting_Integration(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create a text-only agent (no vision) as default
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Default",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Create a vision-capable API agent
	apiAgent := &models.LLMConfig{
		Name:       "Claude API Vision",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, apiAgent); err != nil {
		t.Fatalf("Failed to create API agent: %v", err)
	}

	// Create task with image attachment (mimics user report)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze screenshot from user report",
		Prompt:    "What do you see in this screenshot?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		// No explicit agent assigned - should use default text-only model.
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "Screen Shot 2026-03-21 at 9.14.18 PM.png")
	if err := os.WriteFile(imgPath, []byte("fake PNG"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "Screen Shot 2026-03-21 at 9.14.18 PM.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  8,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Mock LLM caller
	mockCaller := testutil.NewMockLLMCaller()
	svc.SetLLMCaller(mockCaller)

	// Load the task
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	// Load attachments
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	// Execute vision routing
	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "ExecuteTaskWithAgent", task.ID)

	// CRITICAL: The agent should be switched to a vision-capable agent
	if !visionDecision.Changed {
		t.Errorf("BUG REPRODUCED: Vision routing did NOT switch from text-only to vision-capable agent. Reason: %s, Detail: %s", visionDecision.Reason, visionDecision.Detail)
	}

	if visionDecision.Agent.Provider != models.ProviderAnthropic || visionDecision.Agent.AuthMethod != models.AuthMethodAPIKey {
		t.Errorf("BUG REPRODUCED: Expected switch to API agent, but got provider=%s auth=%s", visionDecision.Agent.Provider, visionDecision.Agent.AuthMethod)
	}

	// Verify the selected agent supports vision
	if visionDecision.Agent.IsAnthropicCLI() {
		t.Error("BUG REPRODUCED: Selected agent is still a non-vision legacy CLI config")
	}

	// Success: Agent was properly switched to vision-capable agent
	t.Logf("SUCCESS: Vision routing correctly switched from text-only agent to vision-capable agent: %s (provider=%s, auth=%s)",
		visionDecision.Agent.Name, visionDecision.Agent.Provider, visionDecision.Agent.AuthMethod)
}

func TestLLMService_ImageAttachments_NoVisionAgent_ClearError(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create ONLY a text-only agent (no vision agents available)
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Compatible",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Clear environment fallback
	oldAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "")
	defer os.Setenv("ANTHROPIC_API_KEY", oldAPIKey)

	// Create task with image attachment
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze screenshot",
		Prompt:    "What's in this image?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(imgPath, []byte("PNG"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "image.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  3,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Load task and attachments
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	// Execute vision routing
	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "ExecuteTaskWithAgent", task.ID)

	// Agent should NOT change (no vision agents available)
	if visionDecision.Changed {
		t.Error("Expected no agent change when no vision agents available")
	}

	// Reason should clearly indicate why no vision routing happened
	if visionDecision.Reason != "no_vision_fallback_available" {
		t.Errorf("Expected reason 'no_vision_fallback_available', got: %s", visionDecision.Reason)
	}

	// Detail should mention vision-capable agents
	if !strings.Contains(visionDecision.Detail, "vision-capable") {
		t.Errorf("Expected detail to mention vision-capable agents, got: %s", visionDecision.Detail)
	}

	t.Logf("SUCCESS: When no vision agents available, system provides clear warning: %s", visionDecision.Detail)
}
