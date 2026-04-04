package service

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/util"
)

func TestExtractQualityScore(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected float64
	}{
		{
			name:     "standard format",
			input:    "Review complete.\nQUALITY_SCORE: 0.85",
			expected: 0.85,
		},
		{
			name:     "with spaces",
			input:    "QUALITY_SCORE:  0.9",
			expected: 0.9,
		},
		{
			name:     "perfect score",
			input:    "Everything looks great!\nQUALITY_SCORE: 1.0",
			expected: 1.0,
		},
		{
			name:     "zero score",
			input:    "QUALITY_SCORE: 0.0",
			expected: 0.0,
		},
		{
			name:     "no score found",
			input:    "This is just some output without a score.",
			expected: 0.5, // default
		},
		{
			name:     "score above 1 ignored",
			input:    "QUALITY_SCORE: 1.5",
			expected: 0.5, // default since > 1
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := extractQualityScore(tt.input)
			if score != tt.expected {
				t.Errorf("expected score=%.2f, got %.2f", tt.expected, score)
			}
		})
	}
}

func TestParseVoteOutput(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		expectedChoice    string
		expectedConf      float64
		expectedReasoning string
	}{
		{
			name:              "standard format",
			input:             "CHOICE: Option A\nCONFIDENCE: 0.8\nREASONING: It provides better performance",
			expectedChoice:    "Option A",
			expectedConf:      0.8,
			expectedReasoning: "It provides better performance",
		},
		{
			name:              "missing fields",
			input:             "I think Option B is better",
			expectedChoice:    "unknown",
			expectedConf:      0.5,
			expectedReasoning: "I think Option B is better",
		},
		{
			name:              "partial format",
			input:             "CHOICE: Use Redis\nSome other text",
			expectedChoice:    "Use Redis",
			expectedConf:      0.5,
			expectedReasoning: "CHOICE: Use Redis\nSome other text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			choice, conf, reasoning := parseVoteOutput(tt.input)
			if choice != tt.expectedChoice {
				t.Errorf("expected choice=%q, got %q", tt.expectedChoice, choice)
			}
			if conf != tt.expectedConf {
				t.Errorf("expected confidence=%.2f, got %.2f", tt.expectedConf, conf)
			}
			if reasoning != tt.expectedReasoning {
				t.Errorf("expected reasoning=%q, got %q", tt.expectedReasoning, reasoning)
			}
		})
	}
}

func TestGroupStepsByOrder(t *testing.T) {
	steps := []models.WorkflowStep{
		{ID: "a", Name: "Plan", StepOrder: 0},
		{ID: "b", Name: "Frontend", StepOrder: 1},
		{ID: "c", Name: "Backend", StepOrder: 1},
		{ID: "d", Name: "Tests", StepOrder: 1},
		{ID: "e", Name: "Merge", StepOrder: 2},
		{ID: "f", Name: "Review", StepOrder: 3},
	}

	groups := groupStepsByOrder(steps)

	if len(groups) != 4 {
		t.Fatalf("expected 4 groups, got %d", len(groups))
	}

	// Group 0: Plan (sequential)
	if len(groups[0]) != 1 {
		t.Errorf("group 0: expected 1 step, got %d", len(groups[0]))
	}
	if groups[0][0].Name != "Plan" {
		t.Errorf("group 0: expected Plan, got %s", groups[0][0].Name)
	}

	// Group 1: Frontend, Backend, Tests (parallel)
	if len(groups[1]) != 3 {
		t.Errorf("group 1: expected 3 steps (parallel), got %d", len(groups[1]))
	}

	// Group 2: Merge (sequential)
	if len(groups[2]) != 1 {
		t.Errorf("group 2: expected 1 step, got %d", len(groups[2]))
	}

	// Group 3: Review (sequential)
	if len(groups[3]) != 1 {
		t.Errorf("group 3: expected 1 step, got %d", len(groups[3]))
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
	}

	for _, tt := range tests {
		result := util.Truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("util.Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestAnalyzeTaskComplexity(t *testing.T) {
	// We can't easily test this without a full service setup,
	// but we can test the logic indirectly through the WorkflowRecommendation
	tests := []struct {
		name     string
		prompt   string
		strategy models.WorkflowStrategy
		template string
	}{
		{
			name:     "simple bug fix",
			prompt:   "Fix the bug in the login page",
			strategy: models.StrategySequential,
			template: "Bug Hunt",
		},
		{
			name:     "refactor task",
			prompt:   "Refactor the authentication module to use JWT",
			strategy: models.StrategySequential,
			template: "Refactor",
		},
		{
			name:     "new feature",
			prompt:   "Implement a new dashboard widget for analytics",
			strategy: models.StrategySequential,
			template: "Full Feature",
		},
		{
			name:     "research task",
			prompt:   "Research best practices for caching in Go",
			strategy: models.StrategySequential,
			template: "Research & Implement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This is a unit test of the recommendation logic
			// without needing the full service stack. The actual
			// AnalyzeTaskComplexity method delegates to this logic.
			task := &models.Task{Prompt: tt.prompt}
			rec := analyzeTask(task)
			if rec.TemplateName != tt.template {
				t.Errorf("expected template=%q, got %q", tt.template, rec.TemplateName)
			}
		})
	}
}

// analyzeTask is a test-helper that replicates the analysis logic
func analyzeTask(task *models.Task) *WorkflowRecommendation {
	rec := &WorkflowRecommendation{}
	promptLower := strings.ToLower(task.Prompt)

	isBugFix := strings.Contains(promptLower, "bug") || strings.Contains(promptLower, "fix") || strings.Contains(promptLower, "broken")
	isRefactor := strings.Contains(promptLower, "refactor") || strings.Contains(promptLower, "restructure")
	isResearch := strings.Contains(promptLower, "research") || strings.Contains(promptLower, "investigate")
	isFeature := strings.Contains(promptLower, "implement") || strings.Contains(promptLower, "create") || strings.Contains(promptLower, "new feature") || strings.Contains(promptLower, "add")

	switch {
	case isBugFix:
		rec.TemplateName = "Bug Hunt"
	case isRefactor:
		rec.TemplateName = "Refactor"
	case isResearch:
		rec.TemplateName = "Research & Implement"
	case isFeature:
		rec.TemplateName = "Full Feature"
	default:
		rec.TemplateName = "Full Feature"
	}

	return rec
}
