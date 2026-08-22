package models

import "time"

// AgentModelOption represents one selectable model override in the Agent modal.
type AgentModelOption struct {
	Value string
	Label string
}

// MCPServerConfig defines an MCP server connection for an agent.
type MCPServerConfig struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`    // stdio, http, sse, ws
	Command []string          `json:"command,omitempty"` // stdio server command + args
	URL     string            `json:"url,omitempty"`     // remote server URL
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// SkillConfig defines a skill (slash command) embedded in an agent.
type SkillConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tools       string `json:"tools,omitempty"` // comma-separated tool names
	Content     string `json:"content"`         // the skill instruction body
}

// ScopedFilesConfig grants project-relative filesystem access constrained to a
// directory and explicit permissions.
type ScopedFilesConfig struct {
	Directory   string   `json:"directory"`
	Permissions []string `json:"permissions"`
}

// AgentToolConfig stores structured configuration for parameterized tools.
type AgentToolConfig struct {
	ScopedFiles            []ScopedFilesConfig `json:"scoped_files,omitempty"`
	SkipDefaultTools       bool                `json:"skip_default_tools,omitempty"`
	DisableRuntimeWorktree bool                `json:"disable_runtime_worktree,omitempty"`
}

// AgentGeneratedStatus identifies how an agent record came to exist and whether
// it is editable through normal user/admin flows. Autonomous agent edits are no
// longer product behavior, but these values remain part of persisted state.
type AgentGeneratedStatus string

const (
	AgentStatusGenerated  AgentGeneratedStatus = "generated"   // produced by Skill Curator
	AgentStatusUserEdited AgentGeneratedStatus = "user_edited" // manually customized; preserve intent
	AgentStatusProtected  AgentGeneratedStatus = "protected"   // bundled/locked; no autonomous edits
	AgentStatusArchived   AgentGeneratedStatus = "archived"    // retained for history; not routed to
)

// AgentScope reports whether an agent is portable across projects or scoped
// to one repo.
type AgentScope string

const (
	AgentScopeGlobal  AgentScope = "global"
	AgentScopeProject AgentScope = "project"
)

// AgentCreatedBy reports who originally produced the agent record.
type AgentCreatedBy string

const (
	AgentCreatedByUser   AgentCreatedBy = "user"
	AgentCreatedBySystem AgentCreatedBy = "system"
	AgentCreatedByAgent  AgentCreatedBy = "agent"
)

// AgentPermissionDefaults captures the per-agent default permissions the
// runbook §Permissions Tab (lines 2253-2266) lists. Lifecycle hooks may
// override these per-hook through `permissions_json` on the hook row.
type AgentPermissionDefaults struct {
	ReadTaskPrompt       bool `json:"read_task_prompt,omitempty"`
	ReadTaskExecution    bool `json:"read_task_execution,omitempty"`
	ReadProjectMemory    bool `json:"read_project_memory,omitempty"`
	WriteProjectMemory   bool `json:"write_project_memory,omitempty"`
	ReadAgents           bool `json:"read_agents,omitempty"`
	WriteAgents          bool `json:"write_agents,omitempty"`
	ReadSkills           bool `json:"read_skills,omitempty"`
	WriteSkills          bool `json:"write_skills,omitempty"`
	ReadRepositoryFiles  bool `json:"read_repository_files,omitempty"`
	WriteRepositoryFiles bool `json:"write_repository_files,omitempty"`
	UseShellOrTools      bool `json:"use_shell_or_tools,omitempty"`
}

// AgentModelDefaults stores the optional model preferences and runtime
// execution defaults declared in agent frontmatter.
type AgentModelDefaults struct {
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
}

// ChatAssignableAgentDefinition is the compact Agent shape needed to advertise
// assignable Agent definitions in Chat prompt context without hydrating full
// prompts, tools, permissions, skills, MCP servers, plugins, or model defaults.
type ChatAssignableAgentDefinition struct {
	ID                  string
	Name                string
	Description         string
	Key                 string
	SystemKind          string
	SelectableAsPrimary bool
	Enabled             bool
	GeneratedStatus     AgentGeneratedStatus
	ArchivedAt          *time.Time
}

// Agent is a named configuration that wraps a system prompt, tool restrictions,
// skills, MCP servers, and parameterized tool config. Tasks can be assigned to an agent.
type Agent struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	SystemPrompt string            `json:"system_prompt"`
	Model        string            `json:"model"` // inherit, sonnet, haiku, opus
	Tools        []string          `json:"tools"`
	ToolConfig   AgentToolConfig   `json:"tool_config"`
	Plugins      []string          `json:"plugins"` // plugin IDs: "plugin@marketplace"
	MCPServers   []MCPServerConfig `json:"mcp_servers"`
	SystemKind   string            `json:"system_kind,omitempty"`
	Skills       []SkillConfig     `json:"skills"`
	// Lifecycle-era identity & policy fields (runbook §Data Model Additions
	// lines 2429-2450). All optional; defaults are populated by migration 077
	// for backwards compatibility with existing rows.
	Key                 string                  `json:"key,omitempty"`
	Scope               AgentScope              `json:"scope,omitempty"`
	ProjectID           string                  `json:"project_id,omitempty"`
	SelectableAsPrimary bool                    `json:"selectable_as_primary"`
	Enabled             bool                    `json:"enabled"`
	PermissionDefaults  AgentPermissionDefaults `json:"permission_defaults,omitempty"`
	ModelDefaults       AgentModelDefaults      `json:"model_defaults,omitempty"`
	CreatedBy           AgentCreatedBy          `json:"created_by,omitempty"`
	GeneratedStatus     AgentGeneratedStatus    `json:"generated_status,omitempty"`
	AbsorbedInto        string                  `json:"absorbed_into,omitempty"`
	SourceRefs          []string                `json:"source_refs,omitempty"`
	ArchivedAt          *time.Time              `json:"archived_at,omitempty"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
}

// PluginMarketplace mirrors Claude plugin marketplace list metadata.
type PluginMarketplace struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	URL             string `json:"url,omitempty"`
	Repo            string `json:"repo,omitempty"`
	InstallLocation string `json:"installLocation,omitempty"`
}

// InstalledPlugin mirrors `claude plugin list --json` entries.
type InstalledPlugin struct {
	ID          string   `json:"id"`
	Version     string   `json:"version,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Enabled     bool     `json:"enabled"`
	InstallPath string   `json:"installPath,omitempty"`
	InstalledAt string   `json:"installedAt,omitempty"`
	LastUpdated string   `json:"lastUpdated,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// AvailablePlugin mirrors `claude plugin list --json --available` entries.
type AvailablePlugin struct {
	PluginID        string `json:"pluginId"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	MarketplaceName string `json:"marketplaceName"`
	Source          string `json:"source,omitempty"`
}

// PluginState returns the current plugin marketplace/installation view.
type PluginState struct {
	Marketplaces []PluginMarketplace `json:"marketplaces"`
	Installed    []InstalledPlugin   `json:"installed"`
	Available    []AvailablePlugin   `json:"available"`
	Runtime      []PluginRuntimeMCP  `json:"runtime,omitempty"`
}

// PluginRuntimeMCP reports MCP server runtime health for plugin-backed tools.
type PluginRuntimeMCP struct {
	Name      string `json:"name"`
	PluginID  string `json:"plugin_id,omitempty"` // owning plugin ID (e.g. "github@marketplace")
	Status    string `json:"status"`              // running, failed, stopped
	Error     string `json:"error,omitempty"`
	ToolCount int    `json:"tool_count,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// AllAgentTools is the set of tool names an agent can allow.
const (
	// AgentSystemKindMemoryCurator is the built-in Memory Curator system agent.
	// It owns the project's managed memory through indexed skills:
	// recall_memory (route_task), update_memory (after_complete), and
	// consolidate_memory through a normal visible scheduled task.
	AgentSystemKindMemoryCurator = "memory_curator"

	// AgentSystemKindSkillCurator is the built-in Skill Curator system agent.
	// It owns route_task/observe_task_for_learning/maintain_skill_library
	// skills bound via route_task/after_complete lifecycle hooks plus a normal
	// visible scheduled task for skill library maintenance.
	AgentSystemKindSkillCurator = "skill_curator"

	// AgentSystemKindGoal is the built-in Goal Agent system agent.
	// It evaluates persisted task goals after task-thread turns and queues
	// continuation work only through send_to_task.
	AgentSystemKindGoal  = "goal"
	AgentToolScopedFiles = "ScopedFiles"
)

var AllAgentTools = []string{
	"Read", "Write", "Edit", "Bash", "Glob", "Grep",
	"WebFetch", "WebSearch", "NotebookEdit", AgentToolScopedFiles,
	// Lifecycle/skills runtime tools. These are attached via runtime context
	// per turn and surfaced in the agent dialog so users can choose which
	// agents may inspect or maintain standalone skills. After-complete learning
	// hooks may also improve skills owned by the task's assigned agent through
	// server-scoped agent_skill_manage.
	"skill_view", "skills_list", "agent_list", "agent_view", "skill_manage", "skill_import", "agent_skill_manage",
	"memory_view",
	"send_message",
	"create_alert", "create_notification", "list_alerts", "get_alert", "decide_alert", "claim_alert", "create_alert_implementation_task", "link_alert_implementation_task", "complete_alert_processing", "fail_alert_processing", "release_alert_claim",
	"github_create_issue", "github_get_issue", "github_get_project_inbox", "github_is_actor_authorized", "github_list_my_assigned_issues", "github_list_assigned_issues", "github_list_assigned_issues_with_prs", "github_comment_on_issue", "github_add_issue_labels", "github_close_issue", "github_open_pull_request", "github_replace_pull_request_branch", "github_forward_pr_feedback_to_tasks", "send_to_task", "set_task_goal", "clear_task_goal", "get_task_goal", "pause_task_goal", "resume_task_goal", "mark_task_goal_achieved", "report_task_goal_blocked"}
