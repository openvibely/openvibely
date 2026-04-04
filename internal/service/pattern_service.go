package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type PatternService struct {
	patternRepo *repository.PatternRepo
	taskRepo    *repository.TaskRepo
}

func NewPatternService(
	patternRepo *repository.PatternRepo,
	taskRepo *repository.TaskRepo,
) *PatternService {
	return &PatternService{
		patternRepo: patternRepo,
		taskRepo:    taskRepo,
	}
}

// GetDashboard returns aggregated data for the pattern library page
func (s *PatternService) GetDashboard(ctx context.Context, projectID string) (*models.PatternDashboardData, error) {
	allPatterns, err := s.patternRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list patterns: %w", err)
	}

	recentlyUsed, err := s.patternRepo.ListRecentlyUsed(ctx, projectID, 10)
	if err != nil {
		return nil, fmt.Errorf("list recently used: %w", err)
	}

	mostPopular, err := s.patternRepo.ListMostPopular(ctx, projectID, 10)
	if err != nil {
		return nil, fmt.Errorf("list most popular: %w", err)
	}

	byCategory, err := s.patternRepo.CountByCategory(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count by category: %w", err)
	}

	totalUsage := 0
	customCount := 0
	builtinCount := 0
	for _, p := range allPatterns {
		totalUsage += p.UsageCount
		if p.IsBuiltin {
			builtinCount++
		} else {
			customCount++
		}
	}

	return &models.PatternDashboardData{
		AllPatterns:        allPatterns,
		RecentlyUsed:       recentlyUsed,
		MostPopular:        mostPopular,
		ByCategory:         byCategory,
		TotalPatterns:      len(allPatterns),
		TotalUsage:         totalUsage,
		CustomPatternCount: customCount,
		BuiltinCount:       builtinCount,
	}, nil
}

// CreatePattern creates a new custom pattern
func (s *PatternService) CreatePattern(ctx context.Context, p *models.PromptPattern) error {
	// Auto-extract variables from template
	extractedVars := p.ExtractVariables()
	if err := p.SetVariables(extractedVars); err != nil {
		return fmt.Errorf("set variables: %w", err)
	}

	// Check for duplicate title
	exists, err := s.patternRepo.ExistsByTitle(ctx, p.ProjectID, p.Title)
	if err != nil {
		return fmt.Errorf("check duplicate: %w", err)
	}
	if exists {
		return fmt.Errorf("pattern with title %q already exists", p.Title)
	}

	if err := s.patternRepo.Create(ctx, p); err != nil {
		return fmt.Errorf("create pattern: %w", err)
	}

	return nil
}

// UpdatePattern updates an existing pattern (built-ins cannot be updated)
func (s *PatternService) UpdatePattern(ctx context.Context, p *models.PromptPattern) error {
	existing, err := s.patternRepo.GetByID(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("get pattern: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("pattern not found")
	}
	if existing.IsBuiltin {
		return fmt.Errorf("cannot modify built-in patterns")
	}

	// Auto-extract variables from updated template
	extractedVars := p.ExtractVariables()
	if err := p.SetVariables(extractedVars); err != nil {
		return fmt.Errorf("set variables: %w", err)
	}

	return s.patternRepo.Update(ctx, p)
}

// DeletePattern removes a pattern (built-ins cannot be deleted)
func (s *PatternService) DeletePattern(ctx context.Context, id string) error {
	pattern, err := s.patternRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get pattern: %w", err)
	}
	if pattern == nil {
		return fmt.Errorf("pattern not found")
	}
	if pattern.IsBuiltin {
		return fmt.Errorf("cannot delete built-in patterns")
	}

	return s.patternRepo.Delete(ctx, id)
}

// ApplyPattern renders a pattern with variable values and creates a task
func (s *PatternService) ApplyPattern(ctx context.Context, patternID string, variableValues map[string]string, taskTitle string, projectID string, agentID *string, category models.TaskCategory) (*models.Task, error) {
	pattern, err := s.patternRepo.GetByID(ctx, patternID)
	if err != nil {
		return nil, fmt.Errorf("get pattern: %w", err)
	}
	if pattern == nil {
		return nil, fmt.Errorf("pattern not found")
	}

	// Validate all required variables are provided
	requiredVars, err := pattern.ParseVariables()
	if err != nil {
		return nil, fmt.Errorf("parse variables: %w", err)
	}
	for _, v := range requiredVars {
		if _, ok := variableValues[v]; !ok {
			return nil, fmt.Errorf("missing required variable: %s", v)
		}
	}

	// Apply variable substitution
	renderedPrompt := pattern.ApplyVariables(variableValues)

	// Create task with rendered prompt
	task := &models.Task{
		ProjectID:   projectID,
		Title:       taskTitle,
		Prompt:      renderedPrompt,
		Category:    category,
		Priority:    2, // Default to normal priority
		Status:      models.StatusPending,
		AgentID:     agentID,
		ChainConfig: "{}",
	}

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	// Track usage
	if err := s.patternRepo.IncrementUsage(ctx, patternID); err != nil {
		// Log but don't fail
		fmt.Printf("failed to increment pattern usage: %v\n", err)
	}

	// Create usage history
	varsJSON, _ := json.Marshal(variableValues)
	history := &models.PatternUsageHistory{
		PatternID:        patternID,
		TaskID:           task.ID,
		VariablesApplied: string(varsJSON),
		ResultStatus:     "unknown",
	}
	if err := s.patternRepo.CreateUsageHistory(ctx, history); err != nil {
		// Log but don't fail
		fmt.Printf("failed to create usage history: %v\n", err)
	}

	return task, nil
}

// GetPattern retrieves a single pattern by ID
func (s *PatternService) GetPattern(ctx context.Context, id string) (*models.PromptPattern, error) {
	return s.patternRepo.GetByID(ctx, id)
}

// ListByCategory returns patterns in a specific category
func (s *PatternService) ListByCategory(ctx context.Context, projectID string, category models.PatternCategory) ([]models.PromptPattern, error) {
	return s.patternRepo.ListByCategory(ctx, projectID, category)
}

// Search finds patterns by keyword
func (s *PatternService) Search(ctx context.Context, projectID, query string) ([]models.PromptPattern, error) {
	return s.patternRepo.Search(ctx, projectID, query, 50)
}

// ExportPatterns exports custom patterns as JSON
func (s *PatternService) ExportPatterns(ctx context.Context, projectID string) ([]byte, error) {
	patterns, err := s.patternRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list patterns: %w", err)
	}

	// Filter to only custom patterns (exclude built-ins)
	customPatterns := make([]models.PromptPattern, 0)
	for _, p := range patterns {
		if !p.IsBuiltin {
			customPatterns = append(customPatterns, p)
		}
	}

	return json.MarshalIndent(customPatterns, "", "  ")
}

// ImportPatterns imports patterns from JSON
func (s *PatternService) ImportPatterns(ctx context.Context, projectID string, data []byte) (int, []string, error) {
	var patterns []models.PromptPattern
	if err := json.Unmarshal(data, &patterns); err != nil {
		return 0, nil, fmt.Errorf("invalid JSON: %w", err)
	}

	imported := 0
	var errors []string

	for i, p := range patterns {
		p.ID = ""        // Clear ID to generate new one
		p.ProjectID = projectID
		p.IsBuiltin = false // Never import as built-in
		p.UsageCount = 0    // Reset usage count
		p.LastUsedAt = nil  // Clear last used

		// Validate
		if p.Title == "" {
			errors = append(errors, fmt.Sprintf("pattern %d: missing title", i+1))
			continue
		}
		if p.TemplateText == "" {
			errors = append(errors, fmt.Sprintf("pattern %d (%s): missing template text", i+1, p.Title))
			continue
		}

		// Check for duplicate
		exists, err := s.patternRepo.ExistsByTitle(ctx, projectID, p.Title)
		if err != nil {
			errors = append(errors, fmt.Sprintf("pattern %d (%s): %v", i+1, p.Title, err))
			continue
		}
		if exists {
			errors = append(errors, fmt.Sprintf("pattern %d (%s): already exists", i+1, p.Title))
			continue
		}

		// Create
		if err := s.patternRepo.Create(ctx, &p); err != nil {
			errors = append(errors, fmt.Sprintf("pattern %d (%s): %v", i+1, p.Title, err))
			continue
		}

		imported++
	}

	return imported, errors, nil
}

// DuplicatePattern creates a copy of an existing pattern
func (s *PatternService) DuplicatePattern(ctx context.Context, id string) (*models.PromptPattern, error) {
	original, err := s.patternRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get pattern: %w", err)
	}
	if original == nil {
		return nil, fmt.Errorf("pattern not found")
	}

	// Create copy with new title
	copy := &models.PromptPattern{
		ProjectID:    original.ProjectID,
		Title:        original.Title + " (Copy)",
		Description:  original.Description,
		TemplateText: original.TemplateText,
		Variables:    original.Variables,
		Category:     original.Category,
		IsBuiltin:    false, // Copies are never built-in
		Tags:         original.Tags,
	}

	// Handle duplicate title
	baseTitle := copy.Title
	counter := 2
	for {
		exists, err := s.patternRepo.ExistsByTitle(ctx, copy.ProjectID, copy.Title)
		if err != nil {
			return nil, fmt.Errorf("check duplicate: %w", err)
		}
		if !exists {
			break
		}
		copy.Title = fmt.Sprintf("%s %d", strings.TrimSuffix(baseTitle, fmt.Sprintf(" %d", counter-1)), counter)
		counter++
	}

	if err := s.patternRepo.Create(ctx, copy); err != nil {
		return nil, fmt.Errorf("create copy: %w", err)
	}

	return copy, nil
}

// GetUsageHistory returns recent usage of a pattern
func (s *PatternService) GetUsageHistory(ctx context.Context, patternID string, limit int) ([]models.PatternUsageHistory, error) {
	return s.patternRepo.GetUsageHistory(ctx, patternID, limit)
}

// UpdateTaskResultStatus updates the result status for pattern usage tracking
// This should be called when a task created from a pattern completes
func (s *PatternService) UpdateTaskResultStatus(ctx context.Context, taskID string, status string) error {
	return s.patternRepo.UpdateUsageStatus(ctx, taskID, status)
}
