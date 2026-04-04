package models

import "time"

// TaskTemplate represents a reusable task configuration
type TaskTemplate struct {
	ID               string       `json:"id"`
	ProjectID        *string      `json:"project_id,omitempty"` // NULL = global template
	Name             string       `json:"name"`
	Description      string       `json:"description"`
	DefaultPrompt    string       `json:"default_prompt"`
	SuggestedAgentID *string      `json:"suggested_agent_id,omitempty"`
	Category         TaskCategory `json:"category"`
	Priority         int          `json:"priority"`
	Tag              TaskTag      `json:"tag"`
	TagsJSON         string       `json:"tags_json"` // JSON array of strings
	CategoryFilter   string       `json:"category_filter"`
	IsBuiltIn        bool         `json:"is_built_in"`
	IsFavorite       bool         `json:"is_favorite"`
	UsageCount       int          `json:"usage_count"`
	CreatedBy        string       `json:"created_by"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}

// TaskTemplateCategory represents template categorization
type TaskTemplateCategory string

const (
	TaskTemplateCategoryAll           TaskTemplateCategory = "all"
	TaskTemplateCategoryCodeReview    TaskTemplateCategory = "code_review"
	TaskTemplateCategoryBugFix        TaskTemplateCategory = "bug_fix"
	TaskTemplateCategoryFeature       TaskTemplateCategory = "feature"
	TaskTemplateCategoryRefactor      TaskTemplateCategory = "refactor"
	TaskTemplateCategoryDocumentation TaskTemplateCategory = "documentation"
	TaskTemplateCategoryTesting       TaskTemplateCategory = "testing"
	TaskTemplateCategoryResearch      TaskTemplateCategory = "research"
	TaskTemplateCategoryDeployment    TaskTemplateCategory = "deployment"
	TaskTemplateCategoryMaintenance   TaskTemplateCategory = "maintenance"
	TaskTemplateCategoryPlanning      TaskTemplateCategory = "planning"
)

var AllTaskTemplateCategories = []TaskTemplateCategory{
	TaskTemplateCategoryAll,
	TaskTemplateCategoryCodeReview,
	TaskTemplateCategoryBugFix,
	TaskTemplateCategoryFeature,
	TaskTemplateCategoryRefactor,
	TaskTemplateCategoryDocumentation,
	TaskTemplateCategoryTesting,
	TaskTemplateCategoryResearch,
	TaskTemplateCategoryDeployment,
	TaskTemplateCategoryMaintenance,
	TaskTemplateCategoryPlanning,
}

// TemplateDashboardData aggregates templates for the library view
type TemplateDashboardData struct {
	BuiltInTemplates []TaskTemplate `json:"built_in_templates"`
	CustomTemplates  []TaskTemplate `json:"custom_templates"`
	Favorites        []TaskTemplate `json:"favorites"`
	RecentlyUsed     []TaskTemplate `json:"recently_used"`
}
