package service

import (
	"context"
	"fmt"
	"log"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type TemplateService struct {
	templateRepo *repository.TemplateRepo
	taskRepo     *repository.TaskRepo
	projectRepo  *repository.ProjectRepo
}

func NewTemplateService(
	templateRepo *repository.TemplateRepo,
	taskRepo *repository.TaskRepo,
	projectRepo *repository.ProjectRepo,
) *TemplateService {
	return &TemplateService{
		templateRepo: templateRepo,
		taskRepo:     taskRepo,
		projectRepo:  projectRepo,
	}
}

// GetDashboard returns aggregated template data
func (s *TemplateService) GetDashboard(ctx context.Context, projectID string) (*models.TemplateDashboardData, error) {
	data := &models.TemplateDashboardData{}

	// Get all templates
	allTemplates, err := s.templateRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}

	// Separate built-in vs custom
	var builtIn, custom []models.TaskTemplate
	for _, t := range allTemplates {
		if t.IsBuiltIn {
			builtIn = append(builtIn, t)
		} else {
			custom = append(custom, t)
		}
	}

	if builtIn == nil {
		builtIn = []models.TaskTemplate{}
	}
	if custom == nil {
		custom = []models.TaskTemplate{}
	}

	data.BuiltInTemplates = builtIn
	data.CustomTemplates = custom

	// Get favorites
	favorites, err := s.templateRepo.ListFavorites(ctx, projectID)
	if err != nil {
		log.Printf("[template-svc] error listing favorites: %v", err)
		favorites = []models.TaskTemplate{}
	}
	data.Favorites = favorites

	// Get recently used (last 5)
	recentlyUsed, err := s.templateRepo.ListRecentlyUsed(ctx, projectID, 5)
	if err != nil {
		log.Printf("[template-svc] error listing recently used: %v", err)
		recentlyUsed = []models.TaskTemplate{}
	}
	data.RecentlyUsed = recentlyUsed

	return data, nil
}

// CreateFromTemplate creates a new task from a template
func (s *TemplateService) CreateFromTemplate(ctx context.Context, templateID, projectID, userPrompt string) (*models.Task, error) {
	tmpl, err := s.templateRepo.GetByID(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("getting template: %w", err)
	}
	if tmpl == nil {
		return nil, fmt.Errorf("template not found")
	}

	// Determine the prompt: use user override if provided, otherwise use template default
	prompt := tmpl.DefaultPrompt
	if userPrompt != "" {
		prompt = userPrompt
	}

	// Create task from template
	task := &models.Task{
		ProjectID:    projectID,
		Title:        tmpl.Name,
		Category:     tmpl.Category,
		Priority:     tmpl.Priority,
		Status:       models.StatusPending,
		Prompt:       prompt,
		Tag:          tmpl.Tag,
		DisplayOrder: 0, // Will be set by repo
	}

	// Use suggested agent if specified
	if tmpl.SuggestedAgentID != nil {
		task.AgentID = tmpl.SuggestedAgentID
	}

	if err := s.taskRepo.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("creating task: %w", err)
	}

	// Increment template usage
	if err := s.templateRepo.IncrementUsage(ctx, templateID); err != nil {
		log.Printf("[template-svc] error incrementing usage: %v", err)
	}

	return task, nil
}

// SaveAsTemplate saves a task as a new template
func (s *TemplateService) SaveAsTemplate(ctx context.Context, taskID, projectID, name, description, categoryFilter, createdBy string) (*models.TaskTemplate, error) {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found")
	}

	tmpl := &models.TaskTemplate{
		ProjectID:      &projectID,
		Name:           name,
		Description:    description,
		DefaultPrompt:  task.Prompt,
		Category:       task.Category,
		Priority:       task.Priority,
		Tag:            task.Tag,
		TagsJSON:       "[]",
		CategoryFilter: categoryFilter,
		IsBuiltIn:      false,
		IsFavorite:     false,
		CreatedBy:      createdBy,
	}

	// Copy agent if task has one
	if task.AgentID != nil {
		tmpl.SuggestedAgentID = task.AgentID
	}

	if err := s.templateRepo.Create(ctx, tmpl); err != nil {
		return nil, fmt.Errorf("creating template: %w", err)
	}

	return tmpl, nil
}

// CreateCustomTemplate creates a new custom template
func (s *TemplateService) CreateCustomTemplate(ctx context.Context, tmpl *models.TaskTemplate) error {
	// Ensure it's not marked as built-in
	tmpl.IsBuiltIn = false

	if err := s.templateRepo.Create(ctx, tmpl); err != nil {
		return fmt.Errorf("creating template: %w", err)
	}

	return nil
}

// UpdateTemplate updates an existing template (custom only)
func (s *TemplateService) UpdateTemplate(ctx context.Context, tmpl *models.TaskTemplate) error {
	existing, err := s.templateRepo.GetByID(ctx, tmpl.ID)
	if err != nil {
		return fmt.Errorf("getting template: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("template not found")
	}
	if existing.IsBuiltIn {
		return fmt.Errorf("cannot modify built-in template")
	}

	if err := s.templateRepo.Update(ctx, tmpl); err != nil {
		return fmt.Errorf("updating template: %w", err)
	}

	return nil
}

// DeleteTemplate deletes a template (custom only)
func (s *TemplateService) DeleteTemplate(ctx context.Context, id string) error {
	existing, err := s.templateRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("getting template: %w", err)
	}
	if existing == nil {
		return fmt.Errorf("template not found")
	}
	if existing.IsBuiltIn {
		return fmt.Errorf("cannot delete built-in template")
	}

	return s.templateRepo.Delete(ctx, id)
}

// ToggleFavorite toggles favorite status
func (s *TemplateService) ToggleFavorite(ctx context.Context, id string, isFavorite bool) error {
	return s.templateRepo.ToggleFavorite(ctx, id, isFavorite)
}

// SearchTemplates searches templates
func (s *TemplateService) SearchTemplates(ctx context.Context, projectID, query string) ([]models.TaskTemplate, error) {
	return s.templateRepo.Search(ctx, projectID, query)
}

// FilterByCategory filters templates by category
func (s *TemplateService) FilterByCategory(ctx context.Context, projectID string, category models.TaskTemplateCategory) ([]models.TaskTemplate, error) {
	return s.templateRepo.ListByCategory(ctx, projectID, category)
}
