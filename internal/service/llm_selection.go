package service

import (
	"log"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// ComplexityLevel represents the assessed complexity of a task.
type ComplexityLevel string

const (
	ComplexitySimple   ComplexityLevel = "simple"
	ComplexityModerate ComplexityLevel = "moderate"
	ComplexityComplex  ComplexityLevel = "complex"
)

// ComplexityResult holds the analysis outcome: the level, a score, and the reasoning.
type ComplexityResult struct {
	Level   ComplexityLevel `json:"level"`
	Score   int             `json:"score"`   // 0-100, higher = more complex
	Reasons []string        `json:"reasons"` // Human-readable reasons for the assessment
}

// LLMSelectionResult holds the selected model config and the reasoning behind the choice.
type LLMSelectionResult struct {
	LLMConfig  *models.LLMConfig `json:"llm_config"`
	Complexity ComplexityResult  `json:"complexity"`
	Reason     string            `json:"reason"` // Summary of why this model was chosen
}

// complexKeywords are words/phrases that indicate a complex task.
var complexKeywords = []string{
	"architect", "architecture", "redesign", "refactor", "migration",
	"multi-file", "multiple files", "across files", "system design",
	"performance optimization", "security audit", "database schema",
	"plan", "planning", "strategy", "evaluate", "trade-off", "tradeoff",
	"complex", "comprehensive", "full stack", "end-to-end", "integration",
	"microservice", "distributed", "scalable", "concurrent", "parallel",
}

// moderateKeywords are words/phrases that indicate a moderate task.
var moderateKeywords = []string{
	"implement", "feature", "add", "create", "build", "develop",
	"fix bug", "debug", "resolve", "update", "modify", "change",
	"endpoint", "api", "handler", "service", "component",
	"test", "testing", "unit test", "integration test",
	"database", "query", "migration", "model",
}

// simpleKeywords are words/phrases that indicate a simple task.
var simpleKeywords = []string{
	"rename", "typo", "comment", "documentation", "readme",
	"log", "logging", "print", "format", "formatting",
	"config", "configuration", "env", "environment variable",
	"single line", "one line", "simple", "minor", "small",
	"cleanup", "clean up", "remove unused", "delete unused",
}

// modelTierKeywords maps model name substrings to complexity tiers for matching.
var modelTierComplex = []string{"opus", "o1", "o3", "gpt-4o"}
var modelTierModerate = []string{"sonnet", "gpt-4", "claude-3.5"}
var modelTierSimple = []string{"haiku", "gpt-3.5", "gpt-4o-mini", "flash"}

// AnalyzeComplexity assesses the complexity of a task prompt.
func AnalyzeComplexity(prompt string) ComplexityResult {
	lower := strings.ToLower(prompt)
	words := strings.Fields(prompt)
	wordCount := len(words)

	var reasons []string
	score := 50 // Start at moderate baseline

	// 1. Prompt length heuristic
	if wordCount > 200 {
		score += 20
		reasons = append(reasons, "long prompt (200+ words)")
	} else if wordCount > 100 {
		score += 10
		reasons = append(reasons, "medium-length prompt (100+ words)")
	} else if wordCount < 20 {
		score -= 15
		reasons = append(reasons, "short prompt (under 20 words)")
	}

	// 2. Keyword matching
	complexHits := countKeywordHits(lower, complexKeywords)
	moderateHits := countKeywordHits(lower, moderateKeywords)
	simpleHits := countKeywordHits(lower, simpleKeywords)

	if complexHits > 0 {
		score += complexHits * 10
		reasons = append(reasons, "contains complex-task keywords")
	}
	if simpleHits > 0 {
		score -= simpleHits * 8
		reasons = append(reasons, "contains simple-task keywords")
	}
	if moderateHits > 0 && complexHits == 0 && simpleHits == 0 {
		// Only moderate keywords, stays near baseline
		reasons = append(reasons, "contains standard development keywords")
	}

	// 3. Multi-step indicators (numbered lists, bullet points)
	stepCount := countSteps(prompt)
	if stepCount >= 5 {
		score += 15
		reasons = append(reasons, "multi-step task (5+ steps)")
	} else if stepCount >= 3 {
		score += 8
		reasons = append(reasons, "multi-step task (3+ steps)")
	}

	// 4. Code block presence (suggests implementation details provided = potentially simpler)
	codeBlocks := strings.Count(prompt, "```")
	if codeBlocks >= 2 {
		score -= 5
		reasons = append(reasons, "includes code examples")
	}

	// 5. Question count (more questions = more ambiguity = more complexity)
	questionMarks := strings.Count(prompt, "?")
	if questionMarks >= 3 {
		score += 10
		reasons = append(reasons, "multiple questions/considerations")
	}

	// Clamp score
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}

	// Determine level from score
	level := ComplexityModerate
	if score >= 70 {
		level = ComplexityComplex
	} else if score <= 35 {
		level = ComplexitySimple
	}

	if len(reasons) == 0 {
		reasons = append(reasons, "default assessment")
	}

	return ComplexityResult{
		Level:   level,
		Score:   score,
		Reasons: reasons,
	}
}

// llmWithTier pairs an LLM config with its classified complexity tier.
type llmWithTier struct {
	config *models.LLMConfig
	tier   ComplexityLevel
}

// SelectLLM picks the best model config for the given complexity from the available configs.
// Returns nil if no suitable config is found (caller should fall back to default).
func SelectLLM(complexity ComplexityResult, configs []models.LLMConfig) *LLMSelectionResult {
	return SelectLLMWithVision(complexity, configs, false)
}

// SelectLLMWithVision picks the best model config for the given complexity from the available configs.
// If requiresVision is true, only Anthropic API providers (which support vision) are considered.
// Returns nil if no suitable config is found (caller should fall back to default).
func SelectLLMWithVision(complexity ComplexityResult, configs []models.LLMConfig, requiresVision bool) *LLMSelectionResult {
	if len(configs) == 0 {
		return nil
	}

	// Filter for vision-capable providers if needed
	var filteredConfigs []models.LLMConfig
	if requiresVision {
		for _, cfg := range configs {
			// Vision requires multimodal API access (API key or OAuth), not CLI
			if cfg.Provider == models.ProviderAnthropic && !cfg.IsAnthropicCLI() {
				filteredConfigs = append(filteredConfigs, cfg)
			}
		}
		if len(filteredConfigs) == 0 {
			// No vision-capable providers available
			log.Printf("[llm-selection] vision required but no vision-capable Anthropic providers available")
			return nil
		}
		configs = filteredConfigs
	}

	// If only one config, use it
	if len(configs) == 1 {
		reason := "only model available"
		if requiresVision {
			reason = "only vision-capable model available"
		}
		return &LLMSelectionResult{
			LLMConfig:  &configs[0],
			Complexity: complexity,
			Reason:     reason,
		}
	}

	// Classify configs into tiers based on their model name
	var classified []llmWithTier
	for i := range configs {
		tier := classifyModel(configs[i].Model)
		classified = append(classified, llmWithTier{config: &configs[i], tier: tier})
	}

	// Find the best match for the complexity level
	var selected *models.LLMConfig
	var reason string

	visionSuffix := ""
	if requiresVision {
		visionSuffix = " (vision-capable)"
	}

	switch complexity.Level {
	case ComplexityComplex:
		// Prefer complex-tier, then moderate, then any
		selected = findLLMByTier(classified, ComplexityComplex)
		if selected != nil {
			reason = "complex task matched to advanced model" + visionSuffix
		} else {
			selected = findLLMByTier(classified, ComplexityModerate)
			if selected != nil {
				reason = "complex task, using best available (mid-tier) model" + visionSuffix
			}
		}

	case ComplexityModerate:
		// Prefer moderate-tier, then complex, then simple
		selected = findLLMByTier(classified, ComplexityModerate)
		if selected != nil {
			reason = "moderate task matched to mid-tier model" + visionSuffix
		} else {
			selected = findLLMByTier(classified, ComplexityComplex)
			if selected != nil {
				reason = "moderate task, using advanced model (no mid-tier available)" + visionSuffix
			} else {
				selected = findLLMByTier(classified, ComplexitySimple)
				if selected != nil {
					reason = "moderate task, using lightweight model (only option)" + visionSuffix
				}
			}
		}

	case ComplexitySimple:
		// Prefer simple-tier, then moderate, then any
		selected = findLLMByTier(classified, ComplexitySimple)
		if selected != nil {
			reason = "simple task matched to lightweight model" + visionSuffix
		} else {
			selected = findLLMByTier(classified, ComplexityModerate)
			if selected != nil {
				reason = "simple task, using mid-tier model (no lightweight available)" + visionSuffix
			}
		}
	}

	// Fallback: use default config
	if selected == nil {
		for i := range configs {
			if configs[i].IsDefault {
				selected = &configs[i]
				reason = "no tier match found, using default model"
				break
			}
		}
	}

	// Final fallback: first config
	if selected == nil {
		selected = &configs[0]
		reason = "no tier match found, using first available model"
	}

	log.Printf("[llm-selection] complexity=%s score=%d selected=%s (%s) reason=%q",
		complexity.Level, complexity.Score, selected.Name, selected.Model, reason)

	return &LLMSelectionResult{
		LLMConfig:  selected,
		Complexity: complexity,
		Reason:     reason,
	}
}

// FormatSelectionSummary produces a human-readable summary of model selection.
func FormatSelectionSummary(result *LLMSelectionResult) string {
	if result == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[Auto-selected: ")
	sb.WriteString(result.LLMConfig.Name)
	sb.WriteString(" (")
	sb.WriteString(result.LLMConfig.Model)
	sb.WriteString(") | Complexity: ")
	sb.WriteString(string(result.Complexity.Level))
	sb.WriteString(" | ")
	sb.WriteString(result.Reason)
	sb.WriteString("]")
	return sb.String()
}

// classifyModel determines the tier of a model based on its name.
// Simple/lightweight models are checked first because their keywords (e.g. "mini")
// may overlap with complex model keywords (e.g. "gpt-4o" vs "gpt-4o-mini").
func classifyModel(model string) ComplexityLevel {
	lower := strings.ToLower(model)

	// Check simple first (e.g. "gpt-4o-mini" should match simple, not complex via "gpt-4o")
	for _, kw := range modelTierSimple {
		if strings.Contains(lower, kw) {
			return ComplexitySimple
		}
	}
	for _, kw := range modelTierComplex {
		if strings.Contains(lower, kw) {
			return ComplexityComplex
		}
	}
	for _, kw := range modelTierModerate {
		if strings.Contains(lower, kw) {
			return ComplexityModerate
		}
	}

	// Default to moderate
	return ComplexityModerate
}

func findLLMByTier(classified []llmWithTier, target ComplexityLevel) *models.LLMConfig {
	for _, c := range classified {
		if c.tier == target {
			return c.config
		}
	}
	return nil
}

// countKeywordHits counts how many keywords from the list appear in the text.
func countKeywordHits(text string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			count++
		}
	}
	return count
}

// countSteps counts numbered list items or bullet points as steps.
func countSteps(text string) int {
	lines := strings.Split(text, "\n")
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		// Numbered lists: "1.", "2.", etc.
		if len(trimmed) >= 2 && trimmed[0] >= '1' && trimmed[0] <= '9' && trimmed[1] == '.' {
			count++
			continue
		}
		// Bullet points
		if trimmed[0] == '-' || trimmed[0] == '*' {
			count++
		}
	}
	return count
}
