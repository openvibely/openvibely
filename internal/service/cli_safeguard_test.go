package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

// TestCLISafeguards verifies that real CLI calls are blocked during tests
func TestCLISafeguards(t *testing.T) {
	// Verify GO_TESTING is set (this should be set by testutil.init())
	if os.Getenv("GO_TESTING") == "" {
		t.Fatal("GO_TESTING environment variable should be set during tests")
	}

	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	tests := []struct {
		name       string
		agent      models.LLMConfig
		wantErrMsg string
	}{
		{
			name: "Block Claude CLI calls",
			agent: models.LLMConfig{
				Name:       "Claude Max (CLI)",
				Provider:   models.ProviderAnthropic,
				AuthMethod: models.AuthMethodCLI,
				Model:      "claude-sonnet-4-5",
				MaxTokens:  4096,
			},
			wantErrMsg: "blocked in test mode",
		},
		{
			name: "Block Codex CLI calls",
			agent: models.LLMConfig{
				Name:       "GPT Codex (CLI)",
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodCLI,
				Model:      "gpt-5.3-codex",
				MaxTokens:  4096,
			},
			wantErrMsg: "blocked in test mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create LLM service WITHOUT setting mock (this would trigger real calls)
			svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)

			// Try to call the agent - should be blocked by safeguard
			_, _, err := svc.CallAgentDirect(ctx, "Hello", nil, tt.agent, "")
			if err == nil {
				t.Fatal("Expected error from safeguard, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Errorf("Expected error containing %q, got: %v", tt.wantErrMsg, err)
			}
		})
	}
}

// TestCLISafeguardsWithMockSet verifies safeguard triggers via GO_TESTING even when mock is set
func TestCLISafeguardsWithMockSet(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	agent := models.LLMConfig{
		Name:       "Claude Max (CLI)",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodCLI,
		Model:      "claude-sonnet-4-5",
		MaxTokens:  4096,
	}

	// Create LLM service WITH mock set (should still block CLI agent)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	// Try to call CLI agent - should be blocked even with mock set
	_, _, err := svc.CallAgentDirect(ctx, "Hello", nil, agent, "")
	if err == nil {
		t.Fatal("Expected error from safeguard, got nil")
	}
	if !strings.Contains(err.Error(), "blocked in test mode") {
		t.Errorf("Expected error containing 'blocked in test mode', got: %v", err)
	}
}

// TestProviderTestStillWorks verifies that ProviderTest with mock still works
func TestProviderTestStillWorks(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	agent := models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderTest,
		Model:     "test-model",
		MaxTokens: 4096,
	}

	// Create LLM service WITH mock
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Mock response"
	mock.Tokens = 42
	svc.SetLLMCaller(mock)

	// This should work fine
	output, tokens, err := svc.CallAgentDirect(ctx, "Hello", nil, agent, "")
	if err != nil {
		t.Fatalf("ProviderTest with mock should work, got error: %v", err)
	}
	if output == "" {
		t.Error("Expected non-empty output from mock")
	}
	if tokens == 0 {
		t.Error("Expected non-zero tokens from mock")
	}
}
