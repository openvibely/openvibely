package models

import (
	"encoding/json"
	"strings"
	"time"
)

// PatternCategory represents the category of a prompt pattern
type PatternCategory string

const (
	CategoryDebugging     PatternCategory = "debugging"
	CategoryTesting       PatternCategory = "testing"
	CategoryRefactoring   PatternCategory = "refactoring"
	CategoryDocumentation PatternCategory = "documentation"
	CategoryCodeReview    PatternCategory = "code_review"
	CategoryOptimization  PatternCategory = "optimization"
	CategoryFeature       PatternCategory = "feature"
	CategoryCustom        PatternCategory = "custom"
)

// AllPatternCategories returns all pattern categories for UI display
func AllPatternCategories() []PatternCategory {
	return []PatternCategory{
		CategoryDebugging,
		CategoryTesting,
		CategoryRefactoring,
		CategoryDocumentation,
		CategoryCodeReview,
		CategoryOptimization,
		CategoryFeature,
		CategoryCustom,
	}
}

// PromptPattern represents a reusable prompt template
type PromptPattern struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	TemplateText string          `json:"template_text"`
	Variables    string          `json:"variables"` // JSON array
	Category     PatternCategory `json:"category"`
	IsBuiltin    bool            `json:"is_builtin"`
	UsageCount   int             `json:"usage_count"`
	LastUsedAt   *time.Time      `json:"last_used_at,omitempty"`
	Tags         string          `json:"tags"` // JSON array
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ParseVariables returns the list of variable names
func (p *PromptPattern) ParseVariables() ([]string, error) {
	if p.Variables == "" || p.Variables == "[]" {
		return nil, nil
	}
	var vars []string
	if err := json.Unmarshal([]byte(p.Variables), &vars); err != nil {
		return nil, err
	}
	return vars, nil
}

// SetVariables serializes variable names to JSON
func (p *PromptPattern) SetVariables(vars []string) error {
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	p.Variables = string(b)
	return nil
}

// ParseTags returns the list of tags
func (p *PromptPattern) ParseTags() ([]string, error) {
	if p.Tags == "" || p.Tags == "[]" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(p.Tags), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// SetTags serializes tags to JSON
func (p *PromptPattern) SetTags(tags []string) error {
	b, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	p.Tags = string(b)
	return nil
}

// ApplyVariables substitutes variable placeholders with actual values
// Returns the rendered prompt text
func (p *PromptPattern) ApplyVariables(values map[string]string) string {
	result := p.TemplateText
	for key, value := range values {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// ExtractVariables parses the template and extracts all {{variable}} placeholders
func (p *PromptPattern) ExtractVariables() []string {
	vars := make(map[string]bool)
	text := p.TemplateText

	for {
		start := strings.Index(text, "{{")
		if start == -1 {
			break
		}
		end := strings.Index(text[start:], "}}")
		if end == -1 {
			break
		}

		varName := strings.TrimSpace(text[start+2 : start+end])
		if varName != "" {
			vars[varName] = true
		}
		text = text[start+end+2:]
	}

	result := make([]string, 0, len(vars))
	for v := range vars {
		result = append(result, v)
	}
	return result
}

// PatternUsageHistory tracks when and how a pattern was used
type PatternUsageHistory struct {
	ID               string    `json:"id"`
	PatternID        string    `json:"pattern_id"`
	TaskID           string    `json:"task_id"`
	VariablesApplied string    `json:"variables_applied"` // JSON object
	ResultStatus     string    `json:"result_status"`
	CreatedAt        time.Time `json:"created_at"`
}

// ParseVariablesApplied returns the map of variable values used
func (h *PatternUsageHistory) ParseVariablesApplied() (map[string]string, error) {
	if h.VariablesApplied == "" || h.VariablesApplied == "{}" {
		return make(map[string]string), nil
	}
	var vars map[string]string
	if err := json.Unmarshal([]byte(h.VariablesApplied), &vars); err != nil {
		return nil, err
	}
	return vars, nil
}

// SetVariablesApplied serializes variable values to JSON
func (h *PatternUsageHistory) SetVariablesApplied(vars map[string]string) error {
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	h.VariablesApplied = string(b)
	return nil
}

// PatternDashboardData aggregates data for the pattern library page
type PatternDashboardData struct {
	AllPatterns        []PromptPattern `json:"all_patterns"`
	RecentlyUsed       []PromptPattern `json:"recently_used"`
	MostPopular        []PromptPattern `json:"most_popular"`
	ByCategory         map[string]int  `json:"by_category"`
	TotalPatterns      int             `json:"total_patterns"`
	TotalUsage         int             `json:"total_usage"`
	CustomPatternCount int             `json:"custom_pattern_count"`
	BuiltinCount       int             `json:"builtin_count"`
}

// PatternStats holds summary statistics
type PatternStats struct {
	TotalPatterns      int             `json:"total_patterns"`
	CustomPatterns     int             `json:"custom_patterns"`
	BuiltinPatterns    int             `json:"builtin_patterns"`
	TotalUsages        int             `json:"total_usages"`
	CategoriesUsed     int             `json:"categories_used"`
	MostUsedCategory   PatternCategory `json:"most_used_category"`
	AvgUsagePerPattern float64         `json:"avg_usage_per_pattern"`
}
