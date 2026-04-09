package models

import (
	"encoding/json"
	"time"
)

type TaskCategory string

const (
	CategoryActive    TaskCategory = "active"
	CategoryCompleted TaskCategory = "completed"
	CategoryBacklog   TaskCategory = "backlog"
	CategoryScheduled TaskCategory = "scheduled"
	CategoryChat      TaskCategory = "chat" // Internal category for chat messages (not displayed in UI)
)

var AllCategories = []TaskCategory{
	CategoryBacklog,
	CategoryActive,
	CategoryCompleted,
	// CategoryScheduled intentionally excluded - scheduled tasks are managed via the Schedule page
	// CategoryChat intentionally excluded - chat messages should not appear in kanban board
}

// AllCategoriesIncludingScheduled includes scheduled for task detail dialogs
// where a task may already be in the scheduled category.
var AllCategoriesIncludingScheduled = []TaskCategory{
	CategoryActive,
	CategoryBacklog,
	CategoryCompleted,
	CategoryScheduled,
}

// SelectableCategories are the categories that can be selected when creating/editing a task
// (excludes "completed" and "scheduled" which are managed via Schedule page)
var SelectableCategories = []TaskCategory{
	CategoryActive,
	CategoryBacklog,
}

type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusQueued    TaskStatus = "queued"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
	StatusBlocked   TaskStatus = "blocked" // Waiting for parent task to complete (chained child pre-created for visibility)
)

type TaskTag string

const (
	TagNone    TaskTag = ""
	TagFeature TaskTag = "feature"
	TagBug     TaskTag = "bug"
)

var AllTags = []TaskTag{
	TagNone,
	TagFeature,
	TagBug,
}

type MergeStatus string

const (
	MergeStatusNone     MergeStatus = ""
	MergeStatusPending  MergeStatus = "pending"
	MergeStatusMerged   MergeStatus = "merged"
	MergeStatusFailed   MergeStatus = "failed"
	MergeStatusConflict MergeStatus = "conflict"
)

// TaskOrigin identifies which interface created a task.
const (
	TaskOriginWeb      = "web"
	TaskOriginTelegram = "telegram"
	TaskOriginSlack    = "slack"
)

type Task struct {
	ID                string       `json:"id"`
	ProjectID         string       `json:"project_id"`
	Title             string       `json:"title"`
	Category          TaskCategory `json:"category"`
	Priority          int          `json:"priority"`
	Status            TaskStatus   `json:"status"`
	Prompt            string       `json:"prompt"`
	AgentID           *string      `json:"agent_id,omitempty"`            // Optional: LLM config (model) to use; uses default if nil
	AgentDefinitionID *string      `json:"agent_definition_id,omitempty"` // Optional: agent definition (system prompt, skills, MCP)
	Tag               TaskTag      `json:"tag"`
	DisplayOrder      int          `json:"display_order"`
	ParentTaskID      *string      `json:"parent_task_id,omitempty"` // Optional: references parent task for chaining
	ChainConfig       string       `json:"chain_config"`             // JSON configuration for task chaining
	WorktreePath      string       `json:"worktree_path"`
	WorktreeBranch    string       `json:"worktree_branch"`
	AutoMerge         bool         `json:"auto_merge"`
	MergeTargetBranch string       `json:"merge_target_branch"`
	MergeStatus       MergeStatus  `json:"merge_status"`
	BaseBranch        string       `json:"base_branch"`      // Git branch this task should base its worktree on (from parent lineage)
	BaseCommitSHA     string       `json:"base_commit_sha"`  // Git commit SHA to base worktree on (from parent lineage)
	LineageDepth      int          `json:"lineage_depth"`     // Depth in chain: 0 = root, 1 = child, 2 = grandchild, etc.
	CreatedVia        string       `json:"created_via"`       // Origin: "web", "telegram", etc.
	TelegramChatID    int64        `json:"telegram_chat_id"`  // Telegram chat ID for sending completion notifications
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

// IsTerminalStatus returns true if the task status is terminal (completed/failed/cancelled).
func IsTerminalStatus(s TaskStatus) bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// ChainConfiguration defines the chaining behavior for tasks
type ChainConfiguration struct {
	Enabled           bool                `json:"enabled"`                       // Whether chaining is enabled
	Trigger           string              `json:"trigger"`                       // Trigger condition: "on_completion", "on_planning_complete"
	ChildAgentID      string              `json:"child_agent_id"`                // Agent ID to use for child task (empty = use default)
	ChildModel        string              `json:"child_model"`                   // Model to use for child task (e.g., "sonnet", "opus")
	ChildCategory     string              `json:"child_category"`                // Category for child task (empty = use same as parent)
	ChildTitle        string              `json:"child_title,omitempty"`         // Explicit title for child task (empty = "{parent.Title} (Implementation)")
	ChildPromptPrefix string              `json:"child_prompt_prefix,omitempty"` // Prefix prepended to parent output for child prompt
	ChildChainConfig  *ChainConfiguration `json:"child_chain_config,omitempty"`  // Chain config for the child task (enables multi-level chaining)
}

// ParseChainConfig parses the ChainConfig JSON string from a Task
func (t *Task) ParseChainConfig() (*ChainConfiguration, error) {
	if t.ChainConfig == "" || t.ChainConfig == "{}" {
		return &ChainConfiguration{Enabled: false}, nil
	}
	var config ChainConfiguration
	if err := json.Unmarshal([]byte(t.ChainConfig), &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// SetChainConfig serializes a ChainConfiguration to the ChainConfig JSON string
func (t *Task) SetChainConfig(config *ChainConfiguration) error {
	if config == nil || !config.Enabled {
		t.ChainConfig = "{}"
		return nil
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	t.ChainConfig = string(data)
	return nil
}
