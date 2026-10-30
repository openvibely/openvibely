// Package chatcontrol provides the canonical chat capability registry.
//
// Every chat-controllable action is defined here exactly once. Tool definitions,
// mode gating, surface availability, and domain classification are all derived
// from this single source of truth so web/API/Telegram/Slack/Discord never drift.
//
// # API-domain mapping policy
//
// Chat RW (orchestrate mode):
//   - tasks: create_task, create_swarm_task, edit_task, execute_tasks, cancel_task, send_to_task
//   - schedules: schedule_task, delete_schedule, modify_schedule
//   - alerts: create_alert, create_notification, decide_alert, delete_alert, toggle_alert
//   - personality: set_personality, save_custom_personality
//   - projects: create_project, update_project_settings, switch_project
//   - agents: create_agent, update_agent
//   - automations: save_automation, update_automation_template, run_automation_now, pause_automation, resume_automation, delete_automation
//   - chat: set_chat_mode
//
// Chat read-only (plan + orchestrate):
//   - tasks: list_tasks, view_swarm, view_task_thread
//   - schedules: list_schedules
//   - pulse: view_pulse
//   - projects: list_projects, project_info, get_current_project
//   - models: list_models, get_model
//   - analytics: view_usage_analytics
//   - agents: list_agents
//   - alerts: list_alerts, get_alert
//   - personality: list_personalities, get_personality
//   - settings: view_settings, list_channels, view_system_update
//   - memory: memory_view (only when selected-memory runtime tools authorize a handle)
//   - chat: get_chat_mode, list_capabilities
//   - messaging: send_message
//
// NOT chat-controllable (excluded by design):
//   - OAuth callbacks, credential/token entry endpoints (security boundary)
//   - SSE/plumbing/internal callback routes (infrastructure, not user actions)
//   - GitHub PR/merge operations (complex, multi-step, needs UI context)
//   - Worker pool resize (system-wide, needs explicit admin intent)
//   - Database migrations, config file I/O (infrastructure)
package chatcontrol

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// Surface identifies a chat entry point.
type Surface string

const (
	SurfaceWeb      Surface = "web"
	SurfaceAPI      Surface = "api"
	SurfaceTelegram Surface = "telegram"
	SurfaceSlack    Surface = "slack"
	SurfaceEmail    Surface = "email"
	SurfaceDiscord  Surface = "discord"
	SurfaceX        Surface = "x"
)

// AllSurfaces is the full set of supported surfaces.
var AllSurfaces = []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack, SurfaceEmail, SurfaceDiscord, SurfaceX, SurfaceX}

// AccessLevel classifies read vs write.
type AccessLevel string

const (
	AccessRead  AccessLevel = "read"
	AccessWrite AccessLevel = "write"
)

// Sensitivity classifies the risk level of an action.
type Sensitivity string

const (
	SensitivityNormal      Sensitivity = "normal"
	SensitivityDestructive Sensitivity = "destructive"
	SensitivitySystemWide  Sensitivity = "system_wide"
)

// Domain groups related actions.
type Domain string

const (
	DomainTasks       Domain = "tasks"
	DomainSchedules   Domain = "schedules"
	DomainAlerts      Domain = "alerts"
	DomainPersonality Domain = "personality"
	DomainModels      Domain = "models"
	DomainAgents      Domain = "agents"
	DomainProjects    Domain = "projects"
	DomainSettings    Domain = "settings"
	DomainMessaging   Domain = "messaging"
	DomainGitHub      Domain = "github"
	DomainAnalytics   Domain = "analytics"
	DomainPulse       Domain = "pulse"
	DomainAutomations Domain = "automations"
	DomainMemory      Domain = "memory"
	DomainChat        Domain = "chat"
)

// ActionDef is the canonical definition of a single chat-controllable action.
type ActionDef struct {
	Name        string
	Description string
	Domain      Domain
	Access      AccessLevel
	Sensitivity Sensitivity

	// AllowedModes lists which chat modes allow this action.
	// Plan mode allows only read actions; orchestrate allows all.
	AllowedModes []models.ChatMode

	// Surfaces lists which entry points support this action.
	Surfaces []Surface

	// NeedsConfirmation is true for destructive/system-wide operations.
	NeedsConfirmation bool

	// IncludeThreadTools controls whether this action is included when
	// thread-scoped tools are requested (view_task_thread, send_to_task).
	IncludeThreadTools bool

	// LifecycleOnly keeps a tool out of normal chat runtimes while allowing
	// protected lifecycle agents to request it explicitly.
	LifecycleOnly bool

	// Schema is the provider-independent definition of the tool input.
	Schema *toolschema.Schema
}

// chainSchemaProperties is the JSON Schema for the chain configuration object.
// Fully specifies all ChainConfiguration fields so the LLM can configure chaining
// in a single create_task call without needing a follow-up edit_task.
var chainSchemaProperties = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"enabled": {
			Type:        "boolean",
			Description: "true to enable chaining, false to disable",
		},
		"trigger": {
			Type:        "string",
			Enum:        []any{"on_completion", "on_planning_complete"},
			Description: "When to trigger the child task",
		},
		"child_title": {
			Type:        "string",
			Description: "Title for the child task (defaults to '{parent title} (Implementation)')",
		},
		"child_prompt_prefix": {
			Type:        "string",
			Description: "Text prepended to parent output to form the child prompt",
		},
		"child_category": {
			Type:        "string",
			Enum:        []any{"active", "backlog"},
			Description: "Category for child task (defaults to parent category)",
		},
		"child_agent_id": {
			Type:        "string",
			Description: "Agent/model config ID for the child task",
		},
		"child_chain_config": {
			Type:        "object",
			Description: "Nested chain config for multi-step sequences",
		},
	},
	Required: []string{"enabled"},
}

// createTaskParams is the full JSON Schema for the create_task tool.
var createTaskParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"title": {
			Type: "string"},
		"prompt": {
			Type: "string"},
		"goal": {
			Type:        "string",
			Description: "Optional completion condition for the task. If set, the Goal Agent may continue the task across turns until this condition is satisfied.",
		},
		"category": {
			Type: "string",
			Enum: []any{"active", "backlog"},
		},
		"priority": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
			Maximum: toolschema.Number("4"),
		},
		"agent_id": {
			Type:        "string",
			Description: "Internal model config ID. Do not use for Agent definitions from the Agents page.",
		},
		"agent_definition_id": {
			Type:        "string",
			Description: "Agent definition ID when already known.",
		},
		"agent": {
			Type:        "string",
			Description: "Exact name of an enabled selectable Agent definition from the Agents page, e.g. natural requests like 'Have <agent name>...' use agent: '<agent name>'.",
		},
		"chain": chainSchemaProperties,

		"auto_merge": {
			Type:        "boolean",
			Description: "Auto-merge to the target branch on successful execution completion.",
		},
		"auto_merge_on_goal_achieved": {
			Type:        "boolean",
			Description: "Auto-merge to the target branch when the stored task goal becomes achieved.",
		},
		"merge_target_branch": {
			Type:        "string",
			Description: "Optional local target branch for automatic merge.",
		},
		"source_github_issue_number": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Description: "For a GitHub Dev Inbox implementation task, the exact assigned issue number returned by this execution.",
		},
		"source_github_repo_url": {
			Type:        "string",
			Description: "Optional repository URL for source_github_issue_number. Defaults to the current project repository.",
		},
	},
	Required:             []string{"title", "prompt"},
	AdditionalProperties: toolschema.Pointer(false),
}

var createAgentParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"name": {
			Type:        "string",
			Description: "Display name for the reusable Agent profile.",
		},
		"description": {
			Type:        "string",
			Description: "Short summary shown on the Agents page and selection surfaces.",
		},
		"system_prompt": {
			Type:        "string",
			Description: "System prompt/persona for this user-managed Agent.",
		},
		"model": {
			Type:        "string",
			Description: "Optional model override by configured model id/name, or inherit. Defaults to inherit.",
		},
		"tools": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
			Description: "Optional allowed tool names from the safe Agent tool catalog. Unknown tools are rejected.",
		},
		"scoped_files": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"directory": {
						Type:        "string",
						Description: "Project-relative directory.",
					},
					"permissions": {
						Type: "array",
						Items: &toolschema.Schema{
							Type: "string",
							Enum: []any{"read", "write", "delete"},
						},
					},
				},
				Required:             []string{"directory"},
				AdditionalProperties: toolschema.Pointer(false),
			},
			Description: "Optional safe scoped-file grants. Secret-bearing MCP env/header values are not supported.",
		},
		"scope": {
			Type:        "string",
			Enum:        []any{"project", "global"},
			Description: "Agent scope. Defaults to project.",
		},
		"project_id": {
			Type:        "string",
			Description: "Optional assertion matching the current project for project-scoped Agents.",
		},
		"enabled": {
			Type:        "boolean",
			Description: "Whether the Agent is enabled. Defaults to true.",
		},
		"selectable_as_primary": {
			Type:        "boolean",
			Description: "Whether the Agent can be assigned as a primary Agent for tasks. Defaults to true.",
		},
	},
	Required:             []string{"name", "system_prompt"},
	AdditionalProperties: toolschema.Pointer(false),
}
var updateAgentParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"agent_id": {
			Type:        "string",
			Description: "Existing Agent ID to update.",
		},
		"agent_name": {
			Type:        "string",
			Description: "Exact existing Agent name to update when agent_id/key are not known.",
		},
		"key": {
			Type:        "string",
			Description: "Existing Agent key to update.",
		},
		"project_id": {
			Type:        "string",
			Description: "Optional assertion matching the current project for project-scoped Agents.",
		},
		"name": {
			Type:        "string",
			Description: "Replacement display name. Duplicate enabled selectable names are rejected.",
		},
		"description": {
			Type:        "string",
			Description: "Replacement description. Use an empty string to clear it.",
		},
		"system_prompt": {
			Type:        "string",
			Description: "Replacement system prompt/persona for this user-managed Agent.",
		},
		"model": {
			Type:        "string",
			Description: "Replacement model override by configured model id/name, or inherit to clear the override.",
		},
		"tools": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
			Description: "Replacement allowed tool names from the safe Agent tool catalog. Unknown tools are rejected.",
		},
		"scoped_files": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"directory": {
						Type:        "string",
						Description: "Project-relative directory.",
					},
					"permissions": {
						Type: "array",
						Items: &toolschema.Schema{
							Type: "string",
							Enum: []any{"read", "write", "delete"},
						},
					},
				},
				Required:             []string{"directory"},
				AdditionalProperties: toolschema.Pointer(false),
			},
			Description: "Replacement safe scoped-file grants. Secret-bearing MCP env/header values are not supported.",
		},
		"enabled": {
			Type:        "boolean",
			Description: "Whether the Agent is enabled.",
		},
		"selectable_as_primary": {
			Type:        "boolean",
			Description: "Whether the Agent can be assigned as a primary Agent for tasks.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var saveCustomPersonalityParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"mode": {
			Type:        "string",
			Enum:        []any{"create", "update"},
			Description: "Use create for a new custom personality and update for an existing custom personality.",
		},
		"name": {
			Type:        "string",
			Description: "Display name for the custom personality.",
		},
		"key": {
			Type:        "string",
			Description: "Optional key for create; required exact existing custom key for update.",
		},
		"description": {
			Type:        "string",
			Description: "Short description shown in Personality settings and list_personalities.",
		},
		"system_prompt": {
			Type:        "string",
			Description: "Custom personality system prompt. Must satisfy the same length validation as Settings.",
		},
		"activate": {
			Type:        "boolean",
			Description: "Set this custom personality as active after the save succeeds.",
		},
	},
	Required:             []string{"mode", "name", "system_prompt"},
	AdditionalProperties: toolschema.Pointer(false),
}
var createProjectParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"name": {
			Type:        "string",
			Description: "Project name.",
		},
		"repo_url": {
			Type:        "string",
			Description: "GitHub repository URL to clone for the new project. Local filesystem paths and create-directory behavior are not supported.",
		},
		"description": {
			Type:        "string",
			Description: "Optional project description.",
		},
		"default_agent_config_id": {
			Type:        "string",
			Description: "Optional default model config ID for the project.",
		},
		"max_workers": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Description: "Optional positive per-project worker limit. It must not exceed the configured finite global worker limit.",
		},
		"switch_after_create": {
			Type:        "boolean",
			Description: "Request switching the current Chat/channel context to the new project when the surface supports it.",
		},
	},
	Required:             []string{"name", "repo_url"},
	AdditionalProperties: toolschema.Pointer(false),
}
var updateProjectSettingsParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"project_id": {
			Type:        "string",
			Description: "Optional assertion matching the current project ID. The action updates only the current project.",
		},
		"project_name": {
			Type:        "string",
			Description: "Optional assertion matching the current project name exactly, case-insensitive. The action updates only the current project.",
		},
		"new_name": {
			Type:        "string",
			Description: "Optional new project display name. Must be nonblank after trimming.",
		},
		"description": {
			Type:        "string",
			Description: "Optional replacement project description. Use an empty string to clear it.",
		},
		"default_model": {
			Type:        "string",
			Description: "Optional project default model to set, by exact model config ID first or unambiguous exact model name.",
		},
		"default_model_id": {
			Type:        "string",
			Description: "Optional project default model config ID to set. Use either default_model or default_model_id, not both.",
		},
		"clear_default_model": {
			Type:        "boolean",
			Description: "Clear the project-specific default model so future tasks inherit the global default.",
		},
		"max_workers": {
			Type:        "integer",
			Minimum:     toolschema.Number("0"),
			Description: "Optional project worker limit. Use 0 to clear the project-specific limit; a finite value must not exceed the configured finite global worker limit.",
		},
		"clear_max_workers": {
			Type:        "boolean",
			Description: "Clear the project-specific worker limit, equivalent to max_workers=0.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var createSwarmTaskParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"title": {
			Type: "string"},
		"prompt": {
			Type: "string"},
		"goal": {
			Type:        "string",
			Description: "Optional completion condition for the swarm parent. If set, the Goal Agent may continue the parent task until this condition is satisfied.",
		},
		"project_id": {
			Type:        "string",
			Description: "Optional project id; defaults to current project.",
		},
		"category": {
			Type:        "string",
			Enum:        []any{"active", "backlog"},
			Description: "Active starts the planner now; backlog defers planning until the swarm parent is run or moved to Active.",
		},
		"priority": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
			Maximum: toolschema.Number("4"),
		},
		"agent_id": {
			Type:        "string",
			Description: "Internal model config ID for the swarm parent/children. Do not use for Agent definitions from the Agents page.",
		},
		"agent_definition_id": {
			Type:        "string",
			Description: "Agent definition ID when already known.",
		},
		"agent": {
			Type:        "string",
			Description: "Exact name of an enabled selectable Agent definition from the Agents page, e.g. natural requests like 'Have <agent name>...' use agent: '<agent name>'.",
		},
		"tag": {
			Type: "string",
			Enum: []any{"bug", "feature"},
		},
		"max_workers": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
			Maximum: toolschema.Number("8"),
		},
		"worker_isolation": {
			Type: "string",
			Enum: []any{"worktree", "read_only", "shared"},
		},
		"reviewer_enabled": {
			Type:        "boolean",
			Description: "Whether the swarm should create and run a reviewer stage. Defaults to true.",
		},
		"merger_enabled": {
			Type:        "boolean",
			Description: "Whether the swarm should create and run a merger stage. Defaults to true.",
		},
		"auto_merge": {
			Type:        "boolean",
			Description: "Auto-merge the swarm task branch on successful completion.",
		},
		"auto_merge_on_goal_achieved": {
			Type:        "boolean",
			Description: "Auto-merge the swarm task branch when its stored goal is achieved.",
		},
		"merge_target_branch": {
			Type:        "string",
			Description: "Optional merge target branch for swarm output.",
		},
	},
	Required:             []string{"title", "prompt"},
	AdditionalProperties: toolschema.Pointer(false),
}
var viewSwarmParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"task_id": {
			Type:        "string",
			Description: "Swarm parent or child task ID, or 'current' in a persisted task-thread follow-up.",
		},
		"title": {
			Type:        "string",
			Description: "Exact swarm parent or child task title in the current project when task_id is not known.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var cancelTaskParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"task_id": {
			Type:        "string",
			Description: "Task ID to cancel, or 'current' in a persisted task-thread follow-up.",
		},
		"title": {
			Type:        "string",
			Description: "Exact current-project task title when task_id is not known.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}

// editTaskParams is the full JSON Schema for the edit_task tool.
var editTaskParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"id": {
			Type: "string"},
		"title": {
			Type: "string"},
		"prompt": {
			Type: "string"},
		"category": {
			Type: "string",
			Enum: []any{"active", "backlog", "scheduled"},
		},
		"priority": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
			Maximum: toolschema.Number("4"),
		},
		"tag": {
			Type: "string"},
		"agent_id": {
			Type:        "string",
			Description: "Internal model config ID. Do not use for Agent definitions from the Agents page.",
		},
		"agent_config_id": {
			Type:        "string",
			Description: "Alias for agent_id; selects the LLM model config only.",
		},
		"agent_definition_id": {
			Type:        "string",
			Description: "Primary Agent definition ID when already known.",
		},
		"agent": {
			Type:        "string",
			Description: "Exact name of an enabled selectable Agent definition from the Agents page. Use this when only the Agent name is known.",
		},
		"clear_agent_definition": {
			Type:        "boolean",
			Description: "Clear the primary Agent assignment without clearing or changing the model config.",
		},
		"chain": chainSchemaProperties,

		"auto_merge": {
			Type:        "boolean",
			Description: "Set the successful-completion auto-merge option.",
		},
		"auto_merge_on_goal_achieved": {
			Type:        "boolean",
			Description: "Set the goal-achievement auto-merge option.",
		},
		"merge_target_branch": {
			Type:        "string",
			Description: "Set or clear the local automatic-merge target branch.",
		},
		"attachments": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
		},
	},
	Required:             []string{"id"},
	AdditionalProperties: toolschema.Pointer(false),
}

var sendMessageParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"action": {
			Type:        "string",
			Enum:        []any{"send", "list"},
			Description: "send delivers a message. list returns a bounded page of configured outbound targets including their target_kind.",
		},
		"target": {
			Type:        "string",
			Description: "Delivery target. Format: platform, platform:#target-name, platform:target_id, or platform:target_id:thread_id. Saved outbound targets and home targets are preferred first. Authorized channel users/senders can be used as direct recipients, including email:person@example.com for an authorized Email sender, telegram:123456789 for an authorized Telegram numeric user ID, and slack:user:U123... or discord:user:1518288288572641398 for direct messages. Arbitrary unsaved explicit targets require the project policy. For Discord channel sends use discord:channel:<channel_id> or discord:channel:<channel_id>:<thread_id>. Prefer saved/named targets; call action=list to see configured targets.",
		},
		"message": {
			Type:        "string",
			Description: "Text to send.",
		},
		"subject": {
			Type:        "string",
			Description: "Optional subject for email targets. Ignored by chat platforms.",
		},
		"limit": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Maximum:     toolschema.Number("100"),
			Description: "Maximum targets to return for action=list; defaults to 20 and is capped at 100.",
		},
		"offset": {
			Type:        "integer",
			Minimum:     toolschema.Number("0"),
			Description: "Number of targets to skip for action=list; use next_offset from the previous response.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var requestUserInputParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"questions": {
			Type:        "array",
			Description: "Ask 1 to 3 questions. Put the recommended/default answer first for each question. Do not add a generic per-question move-forward option unless it is a real answer to that specific question; the Chat UI provides a separate Recommended and move forward shortcut for submitting all first-option recommendations. These choices are not answers until the user explicitly selects them.",
			MinItems:    toolschema.Pointer(1),
			MaxItems:    toolschema.Pointer(3),
			Items: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"id": {
						Type:        "string",
						Description: "Stable question id used in the answer result.",
					},
					"question": {
						Type:        "string",
						Description: "Plain-text question to show to the user.",
					},
					"options": {
						Type:        "array",
						Description: "Clickable choices. The first option must be the recommended/default answer. Do not treat any option as selected until the user clicks it or explicitly chooses the global Recommended and move forward shortcut.",
						MinItems:    toolschema.Pointer(2),
						MaxItems:    toolschema.Pointer(3),
						Items: &toolschema.Schema{
							Type: "object",
							Properties: map[string]*toolschema.Schema{
								"label": {
									Type:        "string",
									Description: "Clickable option label returned exactly when selected.",
								},
								"description": {
									Type:        "string",
									Description: "Short plain-text explanation shown under the label.",
								},
							},
							Required:             []string{"label", "description"},
							AdditionalProperties: toolschema.Pointer(false),
						},
					},
				},
				Required:             []string{"id", "question", "options"},
				AdditionalProperties: toolschema.Pointer(false),
			},
		},
	},
	Required:             []string{"questions"},
	AdditionalProperties: toolschema.Pointer(false),
}

var githubCreateIssueParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"title": {
			Type: "string"},
		"body": {
			Type: "string"},
		"labels": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
			Description: "Plain GitHub labels such as suggestion, bug, approved, in-progress. Do not use an openvibely: prefix.",
		},
		"assignees": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
		},
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
	},
	Required:             []string{"title"},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubIssueNumberParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"issue_number": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
		},
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
	},
	Required:             []string{"issue_number"},
	AdditionalProperties: toolschema.Pointer(false),
}

var githubListAssignedIssuesParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"assignee": {
			Type:        "string",
			Description: "GitHub login whose assigned open issues should be listed.",
		},
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
		"limit": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Maximum:     toolschema.Number("100"),
			Description: "Maximum number of assigned issues to return for this page; defaults to 100.",
		},
		"offset": {
			Type:        "integer",
			Minimum:     toolschema.Number("0"),
			Description: "Number of assigned issues to skip for pagination; use next_offset from the previous response.",
		},
	},
	Required:             []string{"assignee"},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubListMyAssignedIssuesParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
		"limit": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Maximum:     toolschema.Number("100"),
			Description: "Maximum number of assigned issues to return for this page; defaults to 100.",
		},
		"offset": {
			Type:        "integer",
			Minimum:     toolschema.Number("0"),
			Description: "Number of assigned issues to skip for pagination; use next_offset from the previous response.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubListExistingAutomationIssuesParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
		"limit": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Maximum:     toolschema.Number("100"),
			Description: "Maximum number of newest issues to return for this page; defaults to 50.",
		},
		"offset": {
			Type:        "integer",
			Minimum:     toolschema.Number("0"),
			Description: "Number of newest matching issues to skip for pagination; use next_offset from the previous response.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubCommentIssueParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"issue_number": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
		},
		"body": {
			Type: "string"},
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
	},
	Required:             []string{"issue_number", "body"},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubAddLabelsParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"issue_number": {
			Type:    "integer",
			Minimum: toolschema.Number("1"),
		},
		"labels": {
			Type: "array",
			Items: &toolschema.Schema{
				Type: "string"},
			Description: "Plain GitHub labels such as approved, in-progress, pr-opened. Do not use an openvibely: prefix.",
		},
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
	},
	Required:             []string{"issue_number", "labels"},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubOpenPullRequestParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"task_id": {
			Type: "string"},
		"title": {
			Type:        "string",
			Description: "Task title to resolve when task_id is omitted.",
		},
		"pr_title": {
			Type:        "string",
			Description: "Optional pull request title. Defaults to the task title.",
		},
		"pr_body": {
			Type:        "string",
			Description: "Optional pull request body. For GitHub SDLC work, provide a concise factual Markdown summary of the changes and validation plus a closing reference to the source issue. Defaults to a concise summary and closes the supplied issue.",
		},
		"base": {
			Type:        "string",
			Description: "Optional target branch. Defaults to the task merge target or repository default branch.",
		},
		"draft": {
			Type: "boolean"},
		"issue_number": {
			Type:        "integer",
			Minimum:     toolschema.Number("1"),
			Description: "Optional GitHub issue number to persist on the task PR record.",
		},
		"issue_url": {
			Type:        "string",
			Description: "Optional GitHub issue URL to persist on the task PR record.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubReplacePullRequestBranchParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"task_id": {
			Type: "string"},
		"title": {
			Type:        "string",
			Description: "Task title to resolve when task_id is omitted.",
		},
		"expected_head_sha": {
			Type:        "string",
			Pattern:     "^[0-9a-fA-F]{40}$",
			Description: "Exact current remote PR branch commit SHA used as the atomic lease guard.",
		},
		"confirm_history_rewrite": {
			Type:        "boolean",
			Const:       toolschema.Literal(true),
			Description: "Must be true to explicitly confirm replacing shared pull request branch history.",
		},
	},
	Required:             []string{"expected_head_sha", "confirm_history_rewrite"},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubForwardPRFeedbackParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"repo_url": {
			Type:        "string",
			Description: "Optional GitHub repository URL. Defaults to the current project repository.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}
var githubActorAuthorizedParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"github_login": {
			Type:        "string",
			Description: "GitHub login to check against the configured authorized actor list.",
		},
	},
	Required:             []string{"github_login"},
	AdditionalProperties: toolschema.Pointer(false),
}
var automationLifecycleParams = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"automation_id": {
			Type:        "string",
			Description: "Saved Automation ID. Use this when known.",
		},
		"name": {
			Type:        "string",
			Description: "Exact saved Automation name to resolve within the current project when automation_id is not known.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}

// registry is the canonical list of all chat-controllable actions.
// Order matters for prompt/documentation consistency.
var registry = []ActionDef{
	// --- Chat domain (RW in orchestrate) ---
	{
		Name:               "request_user_input",
		Description:        "Ask the web Chat user 1 to 3 plain-text questions with 2 to 3 clickable options each, then wait for their selected answers before continuing this same model run. This must be the only tool call in the current model turn. For each question, put the recommended/default answer first; do not add a generic per-question move-forward option unless it is a real answer to that specific question. The Chat UI provides a separate Recommended and move forward shortcut that lets the user explicitly submit all first-option recommendations at once. These are user-visible choices only, so do not assume, preselect, or answer them on the user's behalf unless the user chooses that shortcut. After answers return, inspect every answer before responding. If an answer asks to make another choice, show options, discuss a requirement, or otherwise leaves a requested decision unresolved, call request_user_input again with the necessary follow-up questions; do not give a final summary or tell the user to ask again while that follow-up is unresolved. When clarifying task creation, unless the user already specified the answer, include questions for model/agent choice, whether to assign a persistent goal, and whether/how to create the task with sensible defaults for category/priority/scheduling/chaining/auto-merge where relevant. Each task-creation question must put the recommended/default option first, but the user must explicitly select each answer or choose the global recommended shortcut. When the user asks to be asked questions, use this tool instead of ordinary assistant prose. When generic wording such as need, want, should support, or fix describes work that would benefit from a task but does not explicitly request task creation, use this tool to ask material requirement questions and whether to create the task. This remains required after memory, file, search, or other read-only calls: never end with a prose question such as 'Should I create a task?' when this tool is available. Do not call create_task until the answer is affirmative.",
		Domain:             DomainChat,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           []Surface{SurfaceWeb},
		IncludeThreadTools: false,
		Schema:             requestUserInputParams,
	},
	// --- Tasks domain (RW in orchestrate) ---
	{
		Name:               "create_task",
		Description:        "Create one task in the current project. For sequential workflows ('do X then Y'), create the first task with chain config to automatically trigger the follow-up on completion.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema:             createTaskParams,
	},
	{
		Name:               "create_swarm_task",
		Description:        "Create an autonomous swarm parent task when the user explicitly asks for swarm, subagents, parallel workers, multiple agents, or splitting work across workers. The planner child creates worker/reviewer/merger tasks.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema:             createSwarmTaskParams,
	},
	{
		Name:               "edit_task",
		Description:        "Edit an existing task by id within the current project. Supports title, prompt, priority, category, tag, Agent/model assignment, chain configuration, auto-merge settings, merge target branch, and attachment file paths.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema:             editTaskParams,
	},
	{
		Name:               "execute_tasks",
		Description:        "Execute tasks by exact task_id/title or by optional bulk tag/priority filters. Completed tasks are excluded by default in bulk mode unless include_completed=true.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"tags": {
					Type: "array",
					Items: &toolschema.Schema{
						Type: "string"},
				},
				"min_priority": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
					Maximum: toolschema.Number("4"),
				},
				"include_completed": {
					Type: "boolean"},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "cancel_task",
		Description:        "Cancel one running, queued, or active-pending current-project task. Accepts a task ID, exact title, or task_id='current' in a persisted task-thread follow-up. This stops active work, cancels pending follow-ups, pauses an active goal as stopped by the user, and delegates swarm-parent cancellation to the swarm cascade. It does not support bulk cancellation.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityDestructive,
		NeedsConfirmation:  true,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema:             cancelTaskParams,
	},
	{
		Name:               "list_tasks",
		Description:        "Discover tasks in the current project by partial title and/or optional category/status filters. Omit category/status for a broad search across all visible task categories and statuses. Supplying category or status restricts results to only that lifecycle state and can exclude otherwise matching tasks; do not add them when checking whether any matching task exists. Returned filter values echo the parameters you sent; they are not runtime defaults and are not a reason to retry the same query. If you accidentally supplied category/status, do not immediately repeat the same query just to get an unfiltered echo. Do not enumerate lifecycle filters after an empty total=0, has_more=false result for the same query. Use merge_status='unmerged' to find tasks whose branch has not been merged yet (pending, conflict, or failed), e.g. to ask each one to rebase. Returns compact summaries (task ID, title, category, status, priority, updated time, parent/swarm role, merge_status when the task has a branch: pending, merged, conflict, or failed) with deterministic ordering and explicit limit/offset pagination. Read-only; excludes internal chat rows and never crosses projects. Use it to find an existing task's ID before create_task/edit_task/execute_tasks or to reconcile a GitHub issue by number/URL.",
		Domain:             DomainTasks,
		Access:             AccessRead,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       bothModes(),
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"query": {
					Type:        "string",
					Description: "Optional partial (case-insensitive substring) title match.",
				},
				"category": {
					Type:        "string",
					Enum:        []any{"active", "backlog", "scheduled", "completed"},
					Description: "Optional category filter. Internal chat rows are always excluded.",
				},
				"status": {
					Type:        "string",
					Enum:        []any{"pending", "queued", "running", "completed", "failed", "cancelled", "blocked"},
					Description: "Optional task status filter.",
				},
				"merge_status": {
					Type:        "string",
					Enum:        []any{"unmerged", "pending", "merged", "conflict", "failed"},
					Description: "Optional filter on the task branch's recorded merge status. 'unmerged' matches every task with a branch that is not merged.",
				},
				"limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("50"),
					Description: "Max results to return (default 20, capped at 50).",
				},
				"offset": {
					Type:        "integer",
					Minimum:     toolschema.Number("0"),
					Description: "Number of results to skip for pagination.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "view_swarm",
		Description:        "Inspect one current-project swarm as a compact parent/child hierarchy. Accepts a swarm parent ID, exact parent title, child ID/title (resolved to its parent), or task_id='current' in a persisted task-thread follow-up. Returns ordered planner, worker, reviewer, and merger/integrator child summaries without prompts, execution outputs, diff hunks, or config blobs. Non-swarm tasks return a controlled not-a-swarm response.",
		Domain:             DomainTasks,
		Access:             AccessRead,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       bothModes(),
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Schema:             viewSwarmParams,
	},
	{
		Name:               "view_task_thread",
		Description:        "Fetch execution thread history for a task. Returns paginated results if the thread is large. Use offset to fetch subsequent pages when the response indicates more executions are available.",
		Domain:             DomainTasks,
		Access:             AccessRead,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       bothModes(),
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"offset": {
					Type:        "integer",
					Description: "Execution index to start from (0-based). Use the offset value from the pagination note to fetch the next page.",
				},
				"limit": {
					Type:        "integer",
					Description: "Max number of executions to return. 0 or omitted returns all that fit within the size budget.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "send_to_task",
		Description:        "Send a follow-up message to a task thread.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"message": {
					Type: "string"},
				"origin": {
					Type: "string"},
				"origin_agent": {
					Type: "string"},
			},
			Required:             []string{"message"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "merge_task",
		Description:        "Merge or rebase one current-project task's worktree branch into/onto its target branch, using the same eligibility checks as the task board. action: merge (default, merge commit), squash, fast_forward, or rebase (updates the task branch onto the target without merging). A merge that hits conflicts is aborted automatically, leaving the target branch unchanged, and the task is marked conflict; the result lists conflict_files so you can ask the task via send_to_task to rebase and resolve them. Returns result: merged, rebased, up_to_date, already_merged, conflict, not_eligible, busy, or failed. Handles one task per call; use list_tasks with merge_status='unmerged' to find candidates and call this once per task, sequentially.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityDestructive,
		NeedsConfirmation:  true,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           webAPISurfaces(),
		IncludeThreadTools: false,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID to merge, or 'current' in a persisted task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Exact current-project task title when task_id is not known.",
				},
				"action": {
					Type:        "string",
					Enum:        []any{"merge", "squash", "fast_forward", "rebase"},
					Description: "Merge mode, or rebase to update the task branch onto its target without merging. Defaults to merge.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "set_task_goal",
		Description:        "Set or replace the stored goal for a task.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				},
				"goal": {
					Type: "string"},
			},
			Required:             []string{"goal"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "clear_task_goal",
		Description:        "Clear the stored goal for a task.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				}},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "get_task_goal",
		Description:        "Read the current task goal and status.",
		Domain:             DomainTasks,
		Access:             AccessRead,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       bothModes(),
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				}},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "pause_task_goal",
		Description:        "Pause automatic continuation for a task goal.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				}},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "resume_task_goal",
		Description:        "Resume automatic continuation for a paused task goal.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				}},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "mark_task_goal_achieved",
		Description:        "Mark the current task goal achieved. Requires an explicit agent tool grant and matching goal_id stale-write guard.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				},
				"goal_id": {
					Type: "string"},
				"reason": {
					Type: "string"},
			},
			Required:             []string{"goal_id", "reason"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:               "report_task_goal_blocked",
		Description:        "Report a repeatable blocker for the task goal. Requires an explicit agent tool grant; the service decides when it becomes blocked.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: true,
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Task ID or 'current'. Omit with title, or to use the current task in a task-thread follow-up.",
				},
				"title": {
					Type:        "string",
					Description: "Task title when task_id is not known.",
				},
				"goal_id": {
					Type: "string"},
				"blocker_key": {
					Type: "string"},
				"reason": {
					Type: "string"},
			},
			Required:             []string{"goal_id", "blocker_key", "reason"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Messaging domain (RW in orchestrate) ---
	{
		Name:         "send_message",
		Description:  "Send a message to a channel target, authorized direct recipient, or saved outbound target, or list available outbound targets. Saved outbound targets/home targets are preferred first; authorized channel users/senders can be used as direct recipients; arbitrary unsaved explicit targets are controlled by project policy. If the user names a destination and the exact target is unclear, call send_message with action=list before sending.",
		Domain:       DomainMessaging,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       sendMessageParams,
	},
	// --- GitHub domain ---
	{
		Name:         "github_create_issue",
		Description:  "Create a GitHub issue, defaulting to the current project repository. Pass repo_url to create it in a specific GitHub repository URL. Labels must not use an openvibely: prefix.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubCreateIssueParams,
	},
	{
		Name:         "github_get_issue",
		Description:  "Read a GitHub issue by number, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubIssueNumberParams,
	},
	{
		Name:         "github_get_project_inbox",
		Description:  "List GitHub Authorized Users as issue assignee candidates for the current project. Use these logins with github_list_assigned_issues for GitHub App or custom mailbox setups.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "github_is_actor_authorized",
		Description:  "Check whether a GitHub login is in the GitHub Authorized Users list. Missing or empty authorization lists deny by default.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubActorAuthorizedParams,
	},
	{
		Name:         "github_list_my_assigned_issues",
		Description:  "List open GitHub issues assigned to the authenticated PAT user configured for the GitHub channel, defaulting to the current project repository. Returns compact body-free issue summaries with pagination metadata; use github_get_issue for a specific issue when body/details are needed for task creation. Pass repo_url for a specific GitHub repository URL. For GitHub App installations or custom inboxes, use github_list_assigned_issues with an explicit assignee.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubListMyAssignedIssuesParams,
	},
	{
		Name:         "github_list_existing_automation_issues",
		Description:  "List recent GitHub issues in the current project repository created by the authenticated PAT account so GitHub SDLC finders can avoid duplicate reports. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubListExistingAutomationIssuesParams,
	},
	{
		Name:         "github_list_assigned_issues",
		Description:  "List open GitHub issues assigned to a configured GitHub Authorized User, defaulting to the current project repository. Pull request objects are omitted. Returns compact body-free issue summaries with pagination metadata; use github_get_issue for a specific issue when body/details are needed for task creation. Pass repo_url for a specific GitHub repository URL. For GitHub App/custom setups, pass a login from github_get_project_inbox.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubListAssignedIssuesParams,
	},
	{Name: "github_list_assigned_issues_with_prs",
		Description:  "List open GitHub issues assigned to a login only when each issue already has an associated pull request, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL. Assigned issues without an associated PR are skipped by automation.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema:       githubListAssignedIssuesParams,
	},
	{
		Name:         "github_comment_on_issue",
		Description:  "Post a comment to a GitHub issue, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubCommentIssueParams,
	},
	{
		Name:         "github_add_issue_labels",
		Description:  "Add plain labels to a GitHub issue, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL. Do not use an openvibely: prefix.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubAddLabelsParams,
	},
	{
		Name:         "github_close_issue",
		Description:  "Close a GitHub issue by number, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubIssueNumberParams,
	},
	{
		Name: "github_open_pull_request", Description: "Open or reuse a GitHub pull request for an existing OpenVibely task worktree branch by publishing the branch through the configured GitHub channel token/API, then persist the task PR record.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubOpenPullRequestParams,
	},
	{
		Name:              "github_replace_pull_request_branch",
		Description:       "Destructively replace a linked task pull request branch with the task worktree's clean local HEAD using an atomic force-with-lease guard. Use only for explicitly approved history cleanup, and pass the exact current remote branch SHA.",
		Domain:            DomainGitHub,
		Access:            AccessWrite,
		Sensitivity:       SensitivityDestructive,
		NeedsConfirmation: true,
		AllowedModes:      []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:          webAPISurfaces(),
		Schema:            githubReplacePullRequestBranchParams,
	},
	{
		Name:         "github_forward_pr_feedback_to_tasks",
		Description:  "Fetch new pull request comments/reviews from GitHub Authorized Users for OpenVibely-created task PRs and queue each new feedback item into the linked task thread, with durable deduplication.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Schema:       githubForwardPRFeedbackParams,
	}, // --- Schedules domain (RW in orchestrate) ---
	{
		Name:         "list_schedules",
		Description:  "Discover schedules in the current project. Returns compact summaries (schedule ID, bound task ID/title, enabled state, recurrence type/interval, next run, clear-context-on-start) with deterministic ordering and explicit limit/offset pagination. Read-only; never crosses projects. Optional filters: task_id, title (partial task title), enabled. Use the returned schedule IDs with modify_schedule or delete_schedule.",
		Domain:       DomainSchedules,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type:        "string",
					Description: "Optional: restrict to schedules bound to this task ID.",
				},
				"title": {
					Type:        "string",
					Description: "Optional partial (case-insensitive substring) task title match.",
				},
				"enabled": {
					Type:        "boolean",
					Description: "Optional: filter by enabled (true) or disabled (false) schedules.",
				},
				"limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("50"),
					Description: "Max results to return (default 20, capped at 50).",
				},
				"offset": {
					Type:        "integer",
					Minimum:     toolschema.Number("0"),
					Description: "Number of results to skip for pagination.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "view_pulse",
		Description:  "Return the current project's read-only Pulse upcoming-work agenda: running tasks, pending active tasks, blocked dependency tasks waiting for a parent or orchestration gate (up to 200 bounded entries in blocked_tasks), scheduled tasks due within seven days, and compact priority/status/category/schedule counts including the aggregate task_summary.status.blocked count. Uses project-scoped non-Chat projections and bounded task previews; it does not expose full prompts, execution outputs, or schedule history, and does not mutate work.",
		Domain:       DomainPulse,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     chatSurfacesExceptEmail(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "schedule_task",
		Description:  "Create a schedule for a task.",
		Domain:       DomainSchedules,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"time": {
					Type:        "string",
					Pattern:     "^(?:[01][0-9]|2[0-3]):[0-5][0-9]$",
					Description: "Time in exact 24-hour HH:MM format (00:00-23:59).",
				},
				"repeat": {
					Type: "string",
					Enum: []any{"once", "daily", "weekly", "monthly", "hours", "hourly", "minutes", "seconds"},
				},
				"interval": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
				},
				"days": {
					Type:        "array",
					MaxItems:    toolschema.Pointer(1),
					Description: "For weekly schedules, provide at most one weekday. Accepted spellings are sun, mon, tue, wed, thu, fri, and sat.",
					Items: &toolschema.Schema{
						Type: "string",
						Enum: []any{"sun", "mon", "tue", "wed", "thu", "fri", "sat"},
					},
				},
				"clear_context_on_start": {
					Type:        "boolean",
					Description: "Clear prior model conversation context when each scheduled run starts; defaults to true.",
				},
			},
			Required:             []string{"time"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:              "delete_schedule",
		Description:       "Delete a schedule by schedule_id, task_id, or title.",
		Domain:            DomainSchedules,
		Access:            AccessWrite,
		Sensitivity:       SensitivityDestructive,
		NeedsConfirmation: true,
		AllowedModes:      []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:          allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"schedule_id": {
					Type: "string"},
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "modify_schedule",
		Description:  "Modify an existing schedule.",
		Domain:       DomainSchedules,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"schedule_id": {
					Type: "string"},
				"task_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"time": {
					Type:        "string",
					Pattern:     "^(?:[01][0-9]|2[0-3]):[0-5][0-9]$",
					Description: "Time in exact 24-hour HH:MM format (00:00-23:59).",
				},
				"repeat": {
					Type: "string",
					Enum: []any{"once", "daily", "weekly", "monthly", "hours", "hourly", "minutes", "seconds"},
				},
				"interval": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
				},
				"days": {
					Type:        "array",
					MaxItems:    toolschema.Pointer(1),
					Description: "For weekly schedules, provide at most one weekday. Accepted spellings are sun, mon, tue, wed, thu, fri, and sat.",
					Items: &toolschema.Schema{
						Type: "string",
						Enum: []any{"sun", "mon", "tue", "wed", "thu", "fri", "sat"},
					},
				},
				"enabled": {
					Type: "boolean"},
				"clear_context_on_start": {
					Type:        "boolean",
					Description: "Whether each scheduled start clears prior model conversation context.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Alerts and actionable notifications domain ---
	{
		Name:         "list_alerts",
		Description:  "List project-scoped alerts and actionable notifications with stable pagination and conjunctive lifecycle filters. Native Approved Inbox callers use decision_state=approved, processing_state=unclaimed, and implementation_task_linked=false and omit read, type, and source so eligible waiting work is not excluded.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type:        "string",
					Description: "Optional same-project assertion. Omit it to use the persisted caller task's project context; never discover or reuse a project ID.",
				},
				"decision_state": {
					Type: "string",
					Enum: []any{"not_required", "pending", "approved", "rejected", "dismissed"},
				},
				"processing_state": {
					Type:        "string",
					Enum:        []any{"not_applicable", "unclaimed", "claimed", "implementation_task_linked", "completed", "failed"},
					Description: "Optional processing-state filter. Omit it to include all processing states.",
				},
				"type": {
					Type:        "string",
					Description: "Optional notification-type filter. Omit it to include all types.",
				},
				"source": {
					Type:        "string",
					Description: "Optional notification-source filter. Omit it to include all sources.",
				},
				"read": {
					Type:        "boolean",
					Description: "Optional read-state filter. Omit it to include both read and unread alerts.",
				},
				"implementation_task_linked": {
					Type: "boolean"},
				"limit": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
					Maximum: toolschema.Number("100"),
				},
				"offset": {
					Type:    "integer",
					Minimum: toolschema.Number("0"),
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "get_alert",
		Description:  "Get one notification in the caller's project before claiming or processing it.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
			},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "list_existing_automation_notifications",
		Description:  "List existing actionable notifications owned by the current Native SDLC Automation so finders can avoid duplicate reports before creating a new notification.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("100"),
					Description: "Max notifications to return; defaults to 50.",
				},
				"offset": {
					Type:    "integer",
					Minimum: toolschema.Number("0"),
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "create_alert",
		Description:  "Create a project-scoped operational alert.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"title": {
					Type: "string"},
				"message": {
					Type: "string"},
				"severity": {
					Type: "string",
					Enum: []any{"info", "warning", "error"},
				},
				"type": {
					Type: "string",
					Enum: []any{"custom", "task_failed", "task_needs_followup"},
				},
				"task_id": {
					Type: "string"},
			},
			Required:             []string{"title"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "create_notification",
		Description:  "Create a project-scoped actionable notification for human review. Approval authorizes task creation only, not merge, release, or deployment.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"type": {
					Type:      "string",
					MaxLength: toolschema.Pointer(100),
				},
				"title": {
					Type:      "string",
					MaxLength: toolschema.Pointer(200),
				},
				"message": {
					Type:      "string",
					MaxLength: toolschema.Pointer(2000),
				},
				"body": {
					Type:      "string",
					MaxLength: toolschema.Pointer(20000),
				},
				"severity": {
					Type: "string",
					Enum: []any{"info", "warning", "error"},
				},
				"source": {
					Type:      "string",
					MaxLength: toolschema.Pointer(100),
				},
				"metadata": {
					Type: "object"},
			},
			Required:             []string{"type", "title"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "decide_alert",
		Description:  "Approve, reject, or dismiss a pending actionable notification in the current project after explicit user review.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type:        "string",
					Description: "Optional same-project assertion. The action always uses the caller's current project.",
				},
				"alert_id": {
					Type: "string"},
				"decision": {
					Type: "string",
					Enum: []any{"approved", "rejected", "dismissed"},
				},
			},
			Required:             []string{"alert_id", "decision"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "claim_alert",
		Description:  "Atomically claim an approved notification for the current persisted task using a bounded lease.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
				"lease_seconds": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
					Maximum: toolschema.Number("86400"),
				},
			},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "create_alert_implementation_task",
		Description:  "Idempotently create and link one backlog implementation task for a notification claimed by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
				"title": {
					Type: "string"},
				"prompt": {
					Type: "string"},
				"goal": {
					Type:      "string",
					MaxLength: toolschema.Pointer(2000),
				},
				"priority": {
					Type:    "integer",
					Minimum: toolschema.Number("1"),
					Maximum: toolschema.Number("4"),
				},
				"tag": {
					Type: "string",
					Enum: []any{"", "feature", "bug"},
				},
			},
			Required:             []string{"alert_id", "title", "prompt"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "link_alert_implementation_task",
		Description:  "Link an existing same-project implementation task to a notification claimed by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
				"task_id": {
					Type: "string"},
			},
			Required:             []string{"alert_id", "task_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "complete_alert_processing",
		Description:  "Mark processing completed for a notification owned by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
				"message": {
					Type:      "string",
					MaxLength: toolschema.Pointer(2000),
				},
			},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "fail_alert_processing",
		Description:  "Mark processing failed with retryable diagnostic context for a notification owned by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
				"message": {
					Type:      "string",
					MaxLength: toolschema.Pointer(2000),
				},
			},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "release_alert_claim",
		Description:  "Release the current task's unlinked claim so another scheduled scan can retry it.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type: "string"},
				"alert_id": {
					Type: "string"},
			},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:              "delete_alert",
		Description:       "Delete an alert by id from the caller's project.",
		Domain:            DomainAlerts,
		Access:            AccessWrite,
		Sensitivity:       SensitivityDestructive,
		NeedsConfirmation: true,
		AllowedModes:      []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:          allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"alert_id": {
					Type: "string"}},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "toggle_alert",
		Description:  "Mark an alert as read in the caller's project.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"alert_id": {
					Type: "string"}},
			Required:             []string{"alert_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Personality domain ---
	{
		Name:         "list_personalities",
		Description:  "List one bounded page of personality presets and custom personalities. Built-in presets sort first, then custom personalities by name. Defaults to 20 items and caps at 50. Returns pagination metadata (total, has_more, next_offset). Use set_personality with an exact key even when that key is not on the current page.",
		Domain:       DomainPersonality,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("50"),
					Description: "Maximum personalities to return. Defaults to 20 and caps at 50.",
				},
				"offset": {
					Type:        "integer",
					Minimum:     toolschema.Number("0"),
					Description: "Number of personalities to skip; use next_offset from the previous response.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "get_personality",
		Description:  "Get the current active personality preset.",
		Domain:       DomainPersonality,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "set_personality",
		Description:  "Set the global personality preset.",
		Domain:       DomainPersonality,
		Access:       AccessWrite,
		Sensitivity:  SensitivitySystemWide,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"personality": {
					Type: "string"}},
			Required:             []string{"personality"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "save_custom_personality",
		Description:  "Create or update a custom personality using name, optional key for create, description, and system_prompt. Deletion is not supported. Use activate=true only when the user asks to select it after saving.",
		Domain:       DomainPersonality,
		Access:       AccessWrite,
		Sensitivity:  SensitivitySystemWide,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       saveCustomPersonalityParams,
	},

	// --- Models domain (read-only from chat) ---
	{
		Name:         "list_models",
		Description:  "List configured LLM models.",
		Domain:       DomainModels,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "get_model",
		Description:  "Get details for a specific LLM model by name or id.",
		Domain:       DomainModels,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"model_id": {
					Type: "string"},
				"name": {
					Type: "string"},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Analytics domain (read-only from chat) ---
	{
		Name:         "view_usage_analytics",
		Description:  "Return compact prompt-safe current-project model usage, token totals, cost availability, top model/provider breakdowns, recent buckets, and current provider account limits. Uses local records for model usage and fetches account limits live; reports unavailable limits on provider failure.",
		Domain:       DomainAnalytics,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"range": {
					Type:        "string",
					Enum:        []any{"7d", "30d", "90d", "365d", "month", "all"},
					Description: "Convenience range matching Analytics API; defaults to 30d.",
				},
				"provider": {
					Type:        "string",
					Description: "Optional provider filter, e.g. openai, anthropic, openai_compatible.",
				},
				"group_by": {
					Type:        "string",
					Enum:        []any{"hour", "day", "week", "month"},
					Description: "Recent usage bucket grouping; defaults to day.",
				},
				"date_from": {
					Type:        "string",
					Description: "Optional RFC3339 or YYYY-MM-DD start time.",
				},
				"date_to": {
					Type:        "string",
					Description: "Optional RFC3339 or YYYY-MM-DD end time.",
				},
				"top_limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("10"),
					Description: "Maximum top model/provider rows to return; defaults to 5.",
				},
				"recent_bucket_limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("0"),
					Maximum:     toolschema.Number("24"),
					Description: "Maximum recent usage buckets to return; defaults to 8. Use 0 to omit buckets.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Agents domain (read-only from chat) ---
	{
		Name:         "list_agents",
		Description:  "List configured agent definitions.",
		Domain:       DomainAgents,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "create_agent",
		Description:  "Save a bounded user-managed reusable Agent profile from Chat. Orchestrate-only; does not edit protected Agents, delete Agents, mutate plugins/lifecycle hooks, or accept MCP credentials, OAuth tokens, or API keys.",
		Domain:       DomainAgents,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       createAgentParams,
	},
	{
		Name:         "update_agent",
		Description:  "Update a bounded safe subset of an existing user-managed Agent by ID, exact name, or key. Orchestrate-only; rejects protected or archived Agents, cross-project project-scoped Agents, deletion, plugin/skill/lifecycle mutation, and MCP/OAuth/API-key secrets.",
		Domain:       DomainAgents,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       updateAgentParams,
	},
	// --- Projects domain ---
	{
		Name:         "list_projects",
		Description:  "List all projects.",
		Domain:       DomainProjects,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "project_info",
		Description:  "View details for the current project.",
		Domain:       DomainProjects,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "get_current_project",
		Description:  "Get the name and id of the currently active project.",
		Domain:       DomainProjects,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "create_project",
		Description:  "Create a GitHub-backed project from a project name and GitHub repository URL. Orchestrate-only; does not support local repository paths, create-directory behavior, deletion, or credential details.",
		Domain:       DomainProjects,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       createProjectParams,
	},
	{
		Name:         "update_project_settings",
		Description:  "Update safe settings for the current project: name, description, project default model, and per-project worker limit. Orchestrate-only; optional project_id/project_name are current-project assertions. Does not expose repository path changes, GitHub URL recloning, project deletion, credential edits, or repository rebinding.",
		Domain:       DomainProjects,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       updateProjectSettingsParams,
	},
	{
		Name: "switch_project", Description: "Switch active project by id or name.",
		Domain:       DomainProjects,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project": {
					Type: "string"}},
			Required:             []string{"project"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Settings domain (read-only from chat) ---
	{
		Name:         "view_settings",
		Description:  "View app-level settings.",
		Domain:       DomainSettings,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "list_channels",
		Description:  "Summarize prompt-safe channel and integration readiness for the current project, including GitHub, Slack, Telegram, Discord, Email, inbound webhooks, and outbound message target counts. Does not expose tokens, passwords, webhook secrets, private keys, or raw target credentials.",
		Domain:       DomainSettings,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "view_system_update",
		Description:  "Return a compact prompt-safe summary of OpenVibely system update status from the update coordinator. Read-only; available on web/API Chat and does not accept, cancel, apply, stage, restart, or roll back updates.",
		Domain:       DomainSettings,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Memory domain (read-only, scoped by selected-memory runtime tools) ---
	{
		Name:         "memory_view",
		Description:  "Read a permitted managed memory from the current project memory index.",
		Domain:       DomainMemory,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"handle": {
					Type:        "string",
					Description: "Memory handle/file, e.g. provider_architecture.md",
				},
			},
			Required:             []string{"handle"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},

	// --- Automations domain (project-scoped definition control) ---
	{
		Name:         "list_automations",
		Description:  "List one bounded page of compact project Automation summaries (ID, name, status, paused, adapter key, saved graph node count, operational state counts, next run, last run) plus pagination metadata. Defaults to 20 items and caps large limits. Does not expose YAML graph definitions.",
		Domain:       DomainAutomations,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"project_id": {
					Type:        "string",
					Description: "Optional project ID equality assertion.",
				},
				"limit": {
					Type:        "integer",
					Minimum:     toolschema.Number("1"),
					Maximum:     toolschema.Number("50"),
					Description: "Maximum summaries to return. Defaults to 20 and caps at 50.",
				},
				"offset": {
					Type:        "integer",
					Minimum:     toolschema.Number("0"),
					Description: "Number of summaries to skip before returning the page.",
				},
			},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "get_automation",
		Description:  "Return a compact summary for a single Automation by ID (same shape as list_automations). Returns a clear not-found response for unknown IDs. Does not expose YAML graph definitions.",
		Domain:       DomainAutomations,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"automation_id": {
					Type:        "string",
					Description: "ID of the Automation to retrieve.",
				},
				"project_id": {
					Type:        "string",
					Description: "Optional project ID equality assertion.",
				},
			},
			Required:             []string{"automation_id"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "preview_automation_description",
		Description:  "Generate and validate an ephemeral custom or maintained-template Automation graph from a description using the same surfaced capabilities as the visual builder. This does not persist a draft or create runtime resources.",
		Domain:       DomainAutomations,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"description": {
					Type:      "string",
					MinLength: toolschema.Pointer(1),
					MaxLength: toolschema.Pointer(4000),
				},
			},
			Required:             []string{"description"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "save_automation",
		Description:  "Generate, validate, and atomically save a custom or maintained-template Automation from the user's request or an exact canonical Automation YAML document using the same capabilities and Save pipeline as the visual builder. Use source=\"yaml\" with automation_yaml when the user provides reviewed Automation YAML. Use this when the user asks to create or save an Automation; the successful tool result includes its Live URL. Do not ask for a separate save confirmation.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"source": {
					Type: "string",
					Enum: []any{"template", "describe", "blank", "yaml"},
				},
				"template_key": {
					Type: "string",
					Enum: []any{"native_sdlc", "github_sdlc"},
				},
				"description": {
					Type:      "string",
					MaxLength: toolschema.Pointer(4000),
				},
				"automation_yaml": {
					Type:        "string",
					Description: "Canonical Automation YAML document to save when source is yaml.",
					MinLength:   toolschema.Pointer(1),
					MaxLength:   toolschema.Pointer(65536),
				},
			},
			Required:             []string{"source"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "update_automation_template",
		Description:  "Update an outdated maintained Native SDLC or GitHub SDLC Automation to the latest shipped template by ID or exact unambiguous name in the current project. Replaces the graph using the same explicit maintained-template update path as the browser and preserves lifecycle state. Returns a clear no-op response when already current or unsupported.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       automationLifecycleParams,
	},
	{
		Name:         "run_automation_now",
		Description:  "Run a saved Automation immediately by ID or exact unambiguous name in the current project using the same lifecycle path as the browser Run now action. Returns Automation identity, lifecycle state, started invocation IDs, and Live URL.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       automationLifecycleParams,
	},
	{
		Name:         "pause_automation",
		Description:  "Pause a saved Automation by ID or exact unambiguous name in the current project using the same lifecycle path as the browser Disable action. Returns Automation identity, lifecycle state, and Live URL.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       automationLifecycleParams,
	},
	{
		Name:         "resume_automation",
		Description:  "Resume a saved Automation by ID or exact unambiguous name in the current project using the same lifecycle path as the browser Enable action. Returns Automation identity, lifecycle state, and Live URL.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema:       automationLifecycleParams,
	},
	{
		Name:              "delete_automation",
		Description:       "Permanently delete a saved Automation by ID or exact unambiguous name in the current project. This destructive action requires explicit confirmation and uses the same guarded lifecycle path as browser deletion; it preserves existing domain tasks and removes only Automation-owned trigger schedules. Do not infer deletion from a read or cleanup request.",
		Domain:            DomainAutomations,
		Access:            AccessWrite,
		Sensitivity:       SensitivityDestructive,
		NeedsConfirmation: true,
		AllowedModes:      []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:          allSurfaces(),
		Schema:            automationLifecycleParams,
	},
	// --- Chat domain ---
	{
		Name:         "get_chat_mode",
		Description:  "Get the current chat mode (orchestrate or plan).",
		Domain:       DomainChat,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "set_chat_mode",
		Description:  "Set the chat mode. 'orchestrate' allows full task management; 'plan' is read-only exploration.",
		Domain:       DomainChat,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type: "object",
			Properties: map[string]*toolschema.Schema{
				"mode": {
					Type: "string",
					Enum: []any{"orchestrate", "plan"},
				},
			},
			Required:             []string{"mode"},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
	{
		Name:         "list_capabilities",
		Description:  "List available chat actions for the current mode and surface.",
		Domain:       DomainChat,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Schema: &toolschema.Schema{
			Type:                 "object",
			Properties:           map[string]*toolschema.Schema{},
			AdditionalProperties: toolschema.Pointer(false),
		},
	},
}

// helpers

func allSurfaces() []Surface {
	return []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack, SurfaceEmail, SurfaceDiscord, SurfaceX}
}

func bothModes() []models.ChatMode {
	return []models.ChatMode{models.ChatModeOrchestrate, models.ChatModePlan}
}

func webAPISurfaces() []Surface {
	return []Surface{SurfaceWeb, SurfaceAPI}
}

func chatSurfacesExceptEmail() []Surface {
	return []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack, SurfaceDiscord, SurfaceX}
}

// ---- Public query API ----

// Registry returns a copy of the full action registry.
func Registry() []ActionDef {
	out := make([]ActionDef, len(registry))
	copy(out, registry)
	return out
}

// AllActionNames returns the names of all registered actions.
func AllActionNames() []string {
	names := make([]string, len(registry))
	for i, a := range registry {
		names[i] = a.Name
	}
	return names
}

// Get returns the action definition for the given name, or nil if not found.
func Get(name string) *ActionDef {
	needle := strings.ToLower(strings.TrimSpace(name))
	for i := range registry {
		if registry[i].Name == needle {
			def := registry[i]
			return &def
		}
	}
	return nil
}

// ToolDefsForContext returns RuntimeToolDefinitions filtered by mode, surface,
// and whether thread-scoped tools should be included.
// This is the ONLY function that should be used to generate tool definitions
// for LLM requests — never hand-craft tool definition lists.
func ToolDefsForContext(mode models.ChatMode, surface Surface, includeThreadTools bool) []llmcontracts.RuntimeToolDefinition {
	return toolDefsForContext(mode, surface, includeThreadTools, false)
}

// LifecycleToolDefsForContext returns tool definitions that include lifecycle-only
// actions. It is intended for protected lifecycle agents, not ordinary chat turns.
func LifecycleToolDefsForContext(mode models.ChatMode, surface Surface, includeThreadTools bool) []llmcontracts.RuntimeToolDefinition {
	return toolDefsForContext(mode, surface, includeThreadTools, true)
}

func toolDefsForContext(mode models.ChatMode, surface Surface, includeThreadTools bool, includeLifecycleOnly bool) []llmcontracts.RuntimeToolDefinition {
	var defs []llmcontracts.RuntimeToolDefinition
	for _, a := range registry {
		if a.LifecycleOnly && !includeLifecycleOnly {
			continue
		}
		if !modeAllowed(a, mode) {
			continue
		}
		if !surfaceAllowed(a, surface) {
			continue
		}
		if a.IncludeThreadTools && !includeThreadTools {
			// thread-only tools (view_task_thread, send_to_task) are only included
			// when explicitly requested
		}
		// Include thread-only tools when includeThreadTools is true;
		// always include non-thread tools.
		if !a.IncludeThreadTools || includeThreadTools {
			defs = append(defs, llmcontracts.RuntimeToolDefinition{
				Name:        a.Name,
				Description: a.Description,
				Schema:      a.Schema,
				Access:      runtimeToolAccessForAction(a.Access),
			})
		}
	}
	return defs
}

// IsAllowed checks whether an action is allowed for the given mode and surface.
// Returns an ActionError if blocked, nil if allowed.
func IsAllowed(name string, mode models.ChatMode, surface Surface) *ActionError {
	return isAllowed(name, mode, surface, false)
}

func isAllowed(name string, mode models.ChatMode, surface Surface, includeLifecycleOnly bool) *ActionError {
	def := Get(name)
	if def == nil {
		return &ActionError{
			Action:  name,
			Code:    "unknown_action",
			Message: fmt.Sprintf("action %q is not a recognized chat capability", name),
		}
	}
	if def.LifecycleOnly && !includeLifecycleOnly {
		return &ActionError{
			Action:  name,
			Code:    "lifecycle_only",
			Message: fmt.Sprintf("action %q is only available to protected lifecycle agents", name),
		}
	}
	if !modeAllowed(*def, mode) {
		return &ActionError{
			Action:  name,
			Code:    "mode_blocked",
			Message: fmt.Sprintf("action %q is not available in %s mode (requires orchestrate mode)", name, mode),
		}
	}
	if !surfaceAllowed(*def, surface) {
		return &ActionError{
			Action:  name,
			Code:    "surface_blocked",
			Message: fmt.Sprintf("action %q is not available on %s surface", name, surface),
		}
	}
	return nil
}

// ListForContext returns action metadata for all actions available in the given
// mode and surface. Used by the list_capabilities action.
func ListForContext(mode models.ChatMode, surface Surface) []ActionSummary {
	var out []ActionSummary
	for _, a := range registry {
		if a.LifecycleOnly || !modeAllowed(a, mode) || !surfaceAllowed(a, surface) {
			continue
		}
		out = append(out, ActionSummary{
			Name:        a.Name,
			Description: a.Description,
			Domain:      string(a.Domain),
			Access:      string(a.Access),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ActionSummary is a user-facing summary of an available action.
type ActionSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Domain      string `json:"domain"`
	Access      string `json:"access"`
}

// ActionError is a structured error returned when an action is blocked.
type ActionError struct {
	Action  string `json:"action"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ActionError) Error() string {
	return e.Message
}

// LogGating logs the decision when an action is gated or allowed.
// Commented out: fires on every action routing check (very high frequency).
// Uncomment and switch to applog.Debugf for chatcontrol routing traces.
func LogGating(action string, mode models.ChatMode, surface Surface, allowed bool) {
	if allowed {
		// applog.Debugf("[chatcontrol] action=%s mode=%s surface=%s allowed=true", action, mode, surface)
	} else {
		// applog.Debugf("[chatcontrol] action=%s mode=%s surface=%s allowed=false", action, mode, surface)
	}
}

// internal helpers

func modeAllowed(a ActionDef, mode models.ChatMode) bool {
	for _, m := range a.AllowedModes {
		if m == mode {
			return true
		}
	}
	return false
}

func surfaceAllowed(a ActionDef, surface Surface) bool {
	for _, s := range a.Surfaces {
		if s == surface {
			return true
		}
	}
	return false
}

func runtimeToolAccessForAction(access AccessLevel) llmcontracts.RuntimeToolAccess {
	if access == AccessRead {
		return llmcontracts.RuntimeToolAccessRead
	}
	return llmcontracts.RuntimeToolAccessWrite
}

// ParameterSchema returns the handler contract as ordinary JSON Schema.
func (a ActionDef) ParameterSchema() json.RawMessage { return toolschema.JSON(a.Schema, nil) }
