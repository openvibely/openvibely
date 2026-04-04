package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAnalyzeComplexity_SimpleTask(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
	}{
		{"rename", "Rename the variable foo to bar"},
		{"typo fix", "Fix the typo in the readme"},
		{"simple config", "Update the config file to set the port"},
		{"remove unused", "Remove unused imports from main.go"},
		{"add comment", "Add a comment to the helper function"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnalyzeComplexity(tt.prompt)
			if result.Level != ComplexitySimple {
				t.Errorf("expected simple complexity for %q, got %s (score=%d, reasons=%v)",
					tt.prompt, result.Level, result.Score, result.Reasons)
			}
		})
	}
}

func TestAnalyzeComplexity_ComplexTask(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
	}{
		{
			"architecture redesign",
			"Redesign the entire authentication architecture to support OAuth2, SAML, and JWT. This requires changes across multiple files including the database schema, API handlers, middleware, and frontend components. Consider security trade-offs and plan the migration strategy.",
		},
		{
			"multi-step planning",
			`Implement a distributed task queue system with the following requirements:
1. Design the message broker interface
2. Implement Redis-backed message storage
3. Add worker pool with configurable concurrency
4. Create retry logic with exponential backoff
5. Add dead letter queue for failed messages
6. Implement health checks and monitoring
7. Write comprehensive integration tests`,
		},
		{
			"system design keywords",
			"We need to architect a scalable microservice for handling concurrent distributed transactions with performance optimization and comprehensive security audit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnalyzeComplexity(tt.prompt)
			if result.Level != ComplexityComplex {
				t.Errorf("expected complex complexity for %q, got %s (score=%d, reasons=%v)",
					tt.prompt, result.Level, result.Score, result.Reasons)
			}
		})
	}
}

func TestAnalyzeComplexity_ModerateTask(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
	}{
		{
			"implement feature",
			"Implement a new API endpoint for user profile updates that allows users to change their display name and email address. Add the handler to parse the form values, create a service method with validation, and write the database query to update the user record.",
		},
		{
			"fix bug with context",
			"Fix the bug in the task handler where updating a task's category doesn't trigger the worker pool submission. Debug the issue and add proper error handling.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AnalyzeComplexity(tt.prompt)
			if result.Level != ComplexityModerate {
				t.Errorf("expected moderate complexity for %q, got %s (score=%d, reasons=%v)",
					tt.prompt, result.Level, result.Score, result.Reasons)
			}
		})
	}
}

func TestAnalyzeComplexity_ReturnsReasons(t *testing.T) {
	result := AnalyzeComplexity("Redesign the architecture of the authentication system across multiple files")
	if len(result.Reasons) == 0 {
		t.Error("expected at least one reason in complexity result")
	}
}

func TestAnalyzeComplexity_ScoreRange(t *testing.T) {
	prompts := []string{
		"fix typo",
		"Implement a new feature",
		"Redesign the entire architecture with distributed systems and microservices planning",
	}

	for _, prompt := range prompts {
		result := AnalyzeComplexity(prompt)
		if result.Score < 0 || result.Score > 100 {
			t.Errorf("score %d out of range [0,100] for prompt %q", result.Score, prompt)
		}
	}
}

func TestAnalyzeComplexity_StepCounting(t *testing.T) {
	prompt := `Do the following:
1. Create the model
2. Add the repository
3. Implement the service
4. Add the handler
5. Write tests`

	result := AnalyzeComplexity(prompt)
	// Should detect multi-step task
	if result.Score < 50 {
		t.Errorf("expected higher score for multi-step task, got %d", result.Score)
	}
}

func TestSelectLLM_NoConfigs(t *testing.T) {
	complexity := ComplexityResult{Level: ComplexityModerate, Score: 50}
	result := SelectLLM(complexity, nil)
	if result != nil {
		t.Error("expected nil result for empty configs list")
	}
}

func TestSelectLLM_SingleConfig(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "1", Name: "Only Model", Model: "claude-3-sonnet", Provider: models.ProviderAnthropic},
	}
	complexity := ComplexityResult{Level: ComplexityComplex, Score: 80}
	result := SelectLLM(complexity, configs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.LLMConfig.ID != "1" {
		t.Errorf("expected config '1', got %q", result.LLMConfig.ID)
	}
	if result.Reason != "only model available" {
		t.Errorf("expected reason 'only model available', got %q", result.Reason)
	}
}

func TestSelectLLM_ComplexTaskSelectsOpus(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "1", Name: "Haiku", Model: "claude-3-haiku", Provider: models.ProviderAnthropic},
		{ID: "2", Name: "Sonnet", Model: "claude-3-sonnet", Provider: models.ProviderAnthropic},
		{ID: "3", Name: "Opus", Model: "claude-3-opus", Provider: models.ProviderAnthropic},
	}
	complexity := ComplexityResult{Level: ComplexityComplex, Score: 85}
	result := SelectLLM(complexity, configs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.LLMConfig.ID != "3" {
		t.Errorf("expected Opus config (id=3), got %q (%s)", result.LLMConfig.ID, result.LLMConfig.Name)
	}
}

func TestSelectLLM_SimpleTaskSelectsHaiku(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "1", Name: "Haiku", Model: "claude-3-haiku", Provider: models.ProviderAnthropic},
		{ID: "2", Name: "Sonnet", Model: "claude-3-sonnet", Provider: models.ProviderAnthropic},
		{ID: "3", Name: "Opus", Model: "claude-3-opus", Provider: models.ProviderAnthropic},
	}
	complexity := ComplexityResult{Level: ComplexitySimple, Score: 20}
	result := SelectLLM(complexity, configs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.LLMConfig.ID != "1" {
		t.Errorf("expected Haiku config (id=1), got %q (%s)", result.LLMConfig.ID, result.LLMConfig.Name)
	}
}

func TestSelectLLM_ModerateTaskSelectsSonnet(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "1", Name: "Haiku", Model: "claude-3-haiku", Provider: models.ProviderAnthropic},
		{ID: "2", Name: "Sonnet", Model: "claude-3-sonnet", Provider: models.ProviderAnthropic},
		{ID: "3", Name: "Opus", Model: "claude-3-opus", Provider: models.ProviderAnthropic},
	}
	complexity := ComplexityResult{Level: ComplexityModerate, Score: 50}
	result := SelectLLM(complexity, configs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.LLMConfig.ID != "2" {
		t.Errorf("expected Sonnet config (id=2), got %q (%s)", result.LLMConfig.ID, result.LLMConfig.Name)
	}
}

func TestSelectLLM_FallbackToDefault(t *testing.T) {
	// Configs with model names that don't match any tier keywords
	configs := []models.LLMConfig{
		{ID: "1", Name: "Custom A", Model: "custom-model-a", Provider: models.ProviderAnthropic},
		{ID: "2", Name: "Custom B", Model: "custom-model-b", Provider: models.ProviderAnthropic, IsDefault: true},
	}
	// Both configs classify as "moderate" (default), so for a complex task,
	// findLLMByTier for "complex" returns nil, then it tries "moderate" and finds the first one
	complexity := ComplexityResult{Level: ComplexityComplex, Score: 85}
	result := SelectLLM(complexity, configs)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should find a moderate-tier config as fallback (both are moderate, picks first)
	if result.LLMConfig.ID != "1" {
		t.Errorf("expected first moderate config as fallback, got %q", result.LLMConfig.ID)
	}
}

func TestSelectLLM_GPTModels(t *testing.T) {
	configs := []models.LLMConfig{
		{ID: "1", Name: "GPT-3.5", Model: "gpt-3.5-turbo", Provider: models.ProviderOpenAI},
		{ID: "2", Name: "GPT-4", Model: "gpt-4-turbo", Provider: models.ProviderOpenAI},
		{ID: "3", Name: "GPT-4o", Model: "gpt-4o", Provider: models.ProviderOpenAI},
	}

	// Complex -> gpt-4o
	result := SelectLLM(ComplexityResult{Level: ComplexityComplex, Score: 80}, configs)
	if result.LLMConfig.ID != "3" {
		t.Errorf("expected GPT-4o for complex task, got %q", result.LLMConfig.Name)
	}

	// Simple -> gpt-3.5
	result = SelectLLM(ComplexityResult{Level: ComplexitySimple, Score: 20}, configs)
	if result.LLMConfig.ID != "1" {
		t.Errorf("expected GPT-3.5 for simple task, got %q", result.LLMConfig.Name)
	}
}

func TestClassifyModel(t *testing.T) {
	tests := []struct {
		model    string
		expected ComplexityLevel
	}{
		{"claude-3-opus", ComplexityComplex},
		{"claude-opus-4", ComplexityComplex},
		{"gpt-4o", ComplexityComplex},
		{"o1-preview", ComplexityComplex},
		{"claude-3-sonnet", ComplexityModerate},
		{"claude-3.5-sonnet", ComplexityModerate},
		{"gpt-4-turbo", ComplexityModerate},
		{"claude-3-haiku", ComplexitySimple},
		{"gpt-3.5-turbo", ComplexitySimple},
		{"gpt-4o-mini", ComplexitySimple},
		{"gemini-flash", ComplexitySimple},
		{"unknown-model", ComplexityModerate}, // default
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := classifyModel(tt.model)
			if result != tt.expected {
				t.Errorf("classifyModel(%q) = %s, want %s", tt.model, result, tt.expected)
			}
		})
	}
}

func TestFormatSelectionSummary(t *testing.T) {
	result := &LLMSelectionResult{
		LLMConfig: &models.LLMConfig{
			Name:  "Claude Opus",
			Model: "claude-3-opus",
		},
		Complexity: ComplexityResult{
			Level:   ComplexityComplex,
			Score:   85,
			Reasons: []string{"long prompt", "architecture keywords"},
		},
		Reason: "complex task matched to advanced model",
	}

	summary := FormatSelectionSummary(result)
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	if !containsString(summary, "Claude Opus") {
		t.Errorf("expected summary to contain model name, got %q", summary)
	}
	if !containsString(summary, "complex") {
		t.Errorf("expected summary to contain complexity level, got %q", summary)
	}
	if !containsString(summary, "complex task matched to advanced model") {
		t.Errorf("expected summary to contain reason, got %q", summary)
	}
}

func TestFormatSelectionSummary_Nil(t *testing.T) {
	summary := FormatSelectionSummary(nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil result, got %q", summary)
	}
}

func TestCountKeywordHits(t *testing.T) {
	text := "redesign the architecture with a migration plan"
	hits := countKeywordHits(text, complexKeywords)
	if hits < 3 {
		t.Errorf("expected at least 3 keyword hits, got %d", hits)
	}
}

func TestCountSteps(t *testing.T) {
	text := `Steps:
1. First step
2. Second step
- Bullet point
* Another bullet
Regular line`

	count := countSteps(text)
	if count != 4 {
		t.Errorf("expected 4 steps, got %d", count)
	}
}

func TestCountSteps_NoSteps(t *testing.T) {
	text := "Just a regular paragraph with no numbered or bulleted items."
	count := countSteps(text)
	if count != 0 {
		t.Errorf("expected 0 steps, got %d", count)
	}
}

// containsString checks if s contains substr (using strings.Contains equivalent)
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSelectLLMWithVision_FiltersNonAnthropicProviders(t *testing.T) {
	configs := []models.LLMConfig{
		{
			ID:       "anthropic-sonnet",
			Name:     "Claude Sonnet",
			Provider: models.ProviderAnthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
		{
			ID:         "claude-max",
			Name:       "Claude Max CLI",
			Provider:   models.ProviderAnthropic,
			AuthMethod: models.AuthMethodCLI,
			Model:      "claude-max",
		},
	}

	complexity := ComplexityResult{
		Level: ComplexitySimple,
		Score: 30,
	}

	// With vision required, should only select Anthropic API key/OAuth provider (not CLI)
	result := SelectLLMWithVision(complexity, configs, true)
	if result == nil {
		t.Fatal("expected result, got nil")
	}

	if result.LLMConfig.Provider != models.ProviderAnthropic {
		t.Errorf("expected Anthropic provider when vision required, got %s", result.LLMConfig.Provider)
	}

	if result.LLMConfig.ID != "anthropic-sonnet" {
		t.Errorf("expected anthropic-sonnet config, got %s", result.LLMConfig.ID)
	}

	if !containsString(result.Reason, "vision") {
		t.Errorf("expected reason to mention vision, got %q", result.Reason)
	}
}

func TestSelectLLMWithVision_NoAnthropicProvidersAvailable(t *testing.T) {
	configs := []models.LLMConfig{
		{
			ID:         "claude-max",
			Name:       "Claude Max CLI",
			Provider:   models.ProviderAnthropic,
			AuthMethod: models.AuthMethodCLI,
			Model:      "claude-max",
		},
	}

	complexity := ComplexityResult{
		Level: ComplexitySimple,
		Score: 30,
	}

	// With vision required but only CLI providers (no vision support), should return nil
	result := SelectLLMWithVision(complexity, configs, true)
	if result != nil {
		t.Errorf("expected nil when no vision-capable providers available, got %+v", result)
	}
}

func TestSelectLLMWithVision_NoVisionRequired(t *testing.T) {
	configs := []models.LLMConfig{
		{
			ID:       "anthropic-sonnet",
			Name:     "Claude Sonnet",
			Provider: models.ProviderAnthropic,
			Model:    "claude-3-5-sonnet-20241022",
		},
		{
			ID:         "claude-max",
			Name:       "Claude Max CLI",
			Provider:   models.ProviderAnthropic,
			AuthMethod: models.AuthMethodCLI,
			Model:      "claude-max",
		},
	}

	complexity := ComplexityResult{
		Level: ComplexitySimple,
		Score: 30,
	}

	// Without vision required, any provider can be selected
	result := SelectLLMWithVision(complexity, configs, false)
	if result == nil {
		t.Fatal("expected result, got nil")
	}

	// Should be able to select any provider (doesn't matter which)
	if result.LLMConfig == nil {
		t.Error("expected LLMConfig, got nil")
	}
}
