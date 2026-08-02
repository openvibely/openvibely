// Package chatcontrol provides the canonical chat capability registry.
//
// Every chat-controllable action is defined here exactly once. Tool definitions,
// mode gating, surface availability, and domain classification are all derived
// from this single source of truth so web/API/Telegram/Slack/Discord never drift.
//
// # API-domain mapping policy
//
// Chat RW (orchestrate mode):
//   - tasks: create_task, create_swarm_task, edit_task, execute_tasks, send_to_task
//   - schedules: schedule_task, delete_schedule, modify_schedule
//   - alerts: create_alert, delete_alert, toggle_alert
//   - personality: set_personality
//   - projects: switch_project
//   - chat: set_chat_mode
//
// Chat read-only (plan + orchestrate):
//   - tasks: list_tasks, view_task_thread
//   - schedules: list_schedules
//   - projects: list_projects, project_info, get_current_project
//   - models: list_models, get_model
//   - agents: list_agents
//   - alerts: list_alerts, get_alert
//   - personality: list_personalities, get_personality
//   - settings: view_settings
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
)

// AllSurfaces is the full set of supported surfaces.
var AllSurfaces = []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack, SurfaceEmail, SurfaceDiscord}

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

	// Parameters is the JSON Schema for the tool's input parameters.
	Parameters json.RawMessage
}

// chainSchemaProperties is the JSON Schema for the chain configuration object.
// Fully specifies all ChainConfiguration fields so the LLM can configure chaining
// in a single create_task call without needing a follow-up edit_task.
const chainSchemaProperties = `{"type":"object","properties":{"enabled":{"type":"boolean","description":"true to enable chaining, false to disable"},"trigger":{"type":"string","enum":["on_completion","on_planning_complete"],"description":"When to trigger the child task"},"child_title":{"type":"string","description":"Title for the child task (defaults to '{parent title} (Implementation)')"},"child_prompt_prefix":{"type":"string","description":"Text prepended to parent output to form the child prompt"},"child_category":{"type":"string","enum":["active","backlog"],"description":"Category for child task (defaults to parent category)"},"child_agent_id":{"type":"string","description":"Agent/model config ID for the child task"},"child_chain_config":{"type":"object","description":"Nested chain config for multi-step sequences"}},"required":["enabled"]}`

// createTaskParams is the full JSON Schema for the create_task tool.
const createTaskParams = `{"type":"object","properties":{"title":{"type":"string"},"prompt":{"type":"string"},"goal":{"type":"string","description":"Optional completion condition for the task. If set, the Goal Agent may continue the task across turns until this condition is satisfied."},"category":{"type":"string","enum":["active","backlog"]},"priority":{"type":"integer","minimum":1,"maximum":4},"agent_id":{"type":"string","description":"Internal model config ID. Do not use for Agent definitions from the Agents page."},"agent_definition_id":{"type":"string","description":"Agent definition ID when already known."},"agent":{"type":"string","description":"Exact name of an enabled selectable Agent definition from the Agents page, e.g. natural requests like 'Have <agent name>...' use agent: '<agent name>'."},"chain":` + chainSchemaProperties + `,"source_github_issue_number":{"type":"integer","minimum":1,"description":"For a GitHub Dev Inbox implementation task, the exact assigned issue number returned by this execution."},"source_github_repo_url":{"type":"string","description":"Optional repository URL for source_github_issue_number. Defaults to the current project repository."}},"required":["title","prompt"],"additionalProperties":false}`

const createSwarmTaskParams = `{"type":"object","properties":{"title":{"type":"string"},"prompt":{"type":"string"},"project_id":{"type":"string","description":"Optional project id; defaults to current project."},"category":{"type":"string","enum":["active","backlog"],"description":"Active starts the planner now; backlog defers planning until the swarm parent is run or moved to Active."},"max_workers":{"type":"integer","minimum":1,"maximum":8},"worker_isolation":{"type":"string","enum":["worktree","read_only","shared"]}},"required":["title","prompt"],"additionalProperties":false}`

// editTaskParams is the full JSON Schema for the edit_task tool.
const editTaskParams = `{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"prompt":{"type":"string"},"category":{"type":"string","enum":["active","backlog","scheduled"]},"priority":{"type":"integer","minimum":1,"maximum":4},"tag":{"type":"string"},"agent_id":{"type":"string"},"agent_config_id":{"type":"string"},"chain":` + chainSchemaProperties + `,"attachments":{"type":"array","items":{"type":"string"}}},"required":["id"],"additionalProperties":false}`

const sendMessageParams = `{"type":"object","properties":{"action":{"type":"string","enum":["send","list"],"description":"send delivers a message. list returns configured outbound targets including their target_kind."},"target":{"type":"string","description":"Delivery target. Format: platform, platform:#target-name, platform:target_id, or platform:target_id:thread_id. Saved outbound targets and home targets are preferred first. Authorized channel users/senders can be used as direct recipients, including email:person@example.com for an authorized Email sender, telegram:123456789 for an authorized Telegram numeric user ID, and slack:user:U123... or discord:user:1518288288572641398 for direct messages. Arbitrary unsaved explicit targets require the project policy. For Discord channel sends use discord:channel:<channel_id> or discord:channel:<channel_id>:<thread_id>. Prefer saved/named targets; call action=list to see configured targets."},"message":{"type":"string","description":"Text to send."},"subject":{"type":"string","description":"Optional subject for email targets. Ignored by chat platforms."}},"additionalProperties":false}`

const githubRepoURLProperty = `"repo_url":{"type":"string","description":"Optional GitHub repository URL. Defaults to the current project repository."}`
const githubCreateIssueParams = `{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"},"labels":{"type":"array","items":{"type":"string"},"description":"Plain GitHub labels such as suggestion, bug, approved, in-progress. Do not use an openvibely: prefix."},"assignees":{"type":"array","items":{"type":"string"}},` + githubRepoURLProperty + `},"required":["title"],"additionalProperties":false}`
const githubIssueNumberParams = `{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},` + githubRepoURLProperty + `},"required":["issue_number"],"additionalProperties":false}`
const githubListAssignedIssuesParams = `{"type":"object","properties":{"assignee":{"type":"string","description":"GitHub login whose assigned open issues should be listed."},` + githubRepoURLProperty + `},"required":["assignee"],"additionalProperties":false}`
const githubListMyAssignedIssuesParams = `{"type":"object","properties":{` + githubRepoURLProperty + `},"additionalProperties":false}`
const githubCommentIssueParams = `{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"body":{"type":"string"},` + githubRepoURLProperty + `},"required":["issue_number","body"],"additionalProperties":false}`
const githubAddLabelsParams = `{"type":"object","properties":{"issue_number":{"type":"integer","minimum":1},"labels":{"type":"array","items":{"type":"string"},"description":"Plain GitHub labels such as approved, in-progress, pr-opened. Do not use an openvibely: prefix."},` + githubRepoURLProperty + `},"required":["issue_number","labels"],"additionalProperties":false}`
const githubOpenPullRequestParams = `{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string","description":"Task title to resolve when task_id is omitted."},"pr_title":{"type":"string","description":"Optional pull request title. Defaults to the task title."},"pr_body":{"type":"string","description":"Optional pull request body. Defaults to an OpenVibely task summary."},"base":{"type":"string","description":"Optional target branch. Defaults to the task merge target or repository default branch."},"draft":{"type":"boolean"},"issue_number":{"type":"integer","minimum":1,"description":"Optional GitHub issue number to persist on the task PR record."},"issue_url":{"type":"string","description":"Optional GitHub issue URL to persist on the task PR record."}},"additionalProperties":false}`
const githubReplacePullRequestBranchParams = `{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string","description":"Task title to resolve when task_id is omitted."},"expected_head_sha":{"type":"string","pattern":"^[0-9a-fA-F]{40}$","description":"Exact current remote PR branch commit SHA used as the atomic lease guard."},"confirm_history_rewrite":{"type":"boolean","const":true,"description":"Must be true to explicitly confirm replacing shared pull request branch history."}},"required":["expected_head_sha","confirm_history_rewrite"],"additionalProperties":false}`
const githubForwardPRFeedbackParams = `{"type":"object","properties":{"repo_url":{"type":"string","description":"Optional GitHub repository URL. Defaults to the current project repository."}},"additionalProperties":false}`
const githubActorAuthorizedParams = `{"type":"object","properties":{"github_login":{"type":"string","description":"GitHub login to check against the configured authorized actor list."}},"required":["github_login"],"additionalProperties":false}`

// registry is the canonical list of all chat-controllable actions.
// Order matters for prompt/documentation consistency.
var registry = []ActionDef{
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
		Parameters:         json.RawMessage(createTaskParams),
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
		Parameters:         json.RawMessage(createSwarmTaskParams),
	},
	{
		Name:               "edit_task",
		Description:        "Edit an existing task by id. Can add or modify chain configuration for sequential execution.",
		Domain:             DomainTasks,
		Access:             AccessWrite,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Parameters:         json.RawMessage(editTaskParams),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}},"min_priority":{"type":"integer","minimum":1,"maximum":4},"include_completed":{"type":"boolean"}},"additionalProperties":false}`),
	},
	{
		Name:               "list_tasks",
		Description:        "Discover tasks in the current project by partial title and/or optional category/status filters. Returns compact summaries (task ID, title, category, status, priority, updated time, parent/swarm role) with deterministic ordering and explicit limit/offset pagination. Read-only; excludes internal chat rows and never crosses projects. Use it to find an existing task's ID before create_task/edit_task/execute_tasks or to reconcile a GitHub issue by number/URL.",
		Domain:             DomainTasks,
		Access:             AccessRead,
		Sensitivity:        SensitivityNormal,
		AllowedModes:       bothModes(),
		Surfaces:           allSurfaces(),
		IncludeThreadTools: false,
		Parameters:         json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Optional partial (case-insensitive substring) title match."},"category":{"type":"string","enum":["active","backlog","scheduled","completed"],"description":"Optional category filter. Internal chat rows are always excluded."},"status":{"type":"string","enum":["pending","queued","running","completed","failed","cancelled","blocked"],"description":"Optional task status filter."},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Max results to return (default 20, capped at 50)."},"offset":{"type":"integer","minimum":0,"description":"Number of results to skip for pagination."}},"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"offset":{"type":"integer","description":"Execution index to start from (0-based). Use the offset value from the pagination note to fetch the next page."},"limit":{"type":"integer","description":"Max number of executions to return. 0 or omitted returns all that fit within the size budget."}},"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"message":{"type":"string"},"origin":{"type":"string"},"origin_agent":{"type":"string"}},"required":["message"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","description":"Task id, or 'current' in a task thread."},"goal":{"type":"string"}},"required":["task_id","goal"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"goal_id":{"type":"string"},"reason":{"type":"string"}},"required":["task_id","goal_id","reason"],"additionalProperties":false}`),
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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"goal_id":{"type":"string"},"blocker_key":{"type":"string"},"reason":{"type":"string"}},"required":["task_id","goal_id","blocker_key","reason"],"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(sendMessageParams),
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
		Parameters:   json.RawMessage(githubCreateIssueParams),
	},
	{
		Name:         "github_get_issue",
		Description:  "Read a GitHub issue by number, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubIssueNumberParams),
	},
	{
		Name:         "github_get_project_inbox",
		Description:  "List GitHub Authorized Users as issue assignee candidates for the current project. Use these logins with github_list_assigned_issues for GitHub App or custom mailbox setups.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "github_is_actor_authorized",
		Description:  "Check whether a GitHub login is in the GitHub Authorized Users list. Missing or empty authorization lists deny by default.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubActorAuthorizedParams),
	},
	{
		Name:         "github_list_my_assigned_issues",
		Description:  "List open GitHub issues assigned to the authenticated PAT user configured for the GitHub channel, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL. For GitHub App installations or custom inboxes, use github_list_assigned_issues with an explicit assignee.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubListMyAssignedIssuesParams),
	},
	{
		Name:         "github_list_assigned_issues",
		Description:  "List open GitHub issues assigned to the provided GitHub login, defaulting to the current project repository. Pull request objects are omitted. Pass repo_url for a specific GitHub repository URL. For GitHub App/custom setups, pass a login from github_get_project_inbox.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubListAssignedIssuesParams),
	},
	{Name: "github_list_assigned_issues_with_prs",
		Description:  "List open GitHub issues assigned to a login only when each issue already has an associated pull request, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL. Assigned issues without an associated PR are skipped by automation.",
		Domain:       DomainGitHub,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubListAssignedIssuesParams),
	},
	{
		Name:         "github_comment_on_issue",
		Description:  "Post a comment to a GitHub issue, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubCommentIssueParams),
	},
	{
		Name:         "github_add_issue_labels",
		Description:  "Add plain labels to a GitHub issue, defaulting to the current project repository. Pass repo_url for a specific GitHub repository URL. Do not use an openvibely: prefix.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubAddLabelsParams),
	},
	{
		Name:         "github_open_pull_request",
		Description:  "Open or reuse a GitHub pull request for an existing OpenVibely task worktree branch by publishing the branch through the configured GitHub channel token/API, then persist the task PR record.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubOpenPullRequestParams),
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
		Parameters:        json.RawMessage(githubReplacePullRequestBranchParams),
	},
	{
		Name:         "github_forward_pr_feedback_to_tasks",
		Description:  "Fetch new pull request comments/reviews from GitHub Authorized Users for OpenVibely-created task PRs and queue each new feedback item into the linked task thread, with durable deduplication.",
		Domain:       DomainGitHub,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(githubForwardPRFeedbackParams),
	}, // --- Schedules domain (RW in orchestrate) ---
	{
		Name:         "list_schedules",
		Description:  "Discover schedules in the current project. Returns compact summaries (schedule ID, bound task ID/title, enabled state, recurrence type/interval/days, next run, clear-context-on-start) with deterministic ordering and explicit limit/offset pagination. Read-only; never crosses projects. Optional filters: task_id, title (partial task title), enabled. Use the returned schedule IDs with modify_schedule or delete_schedule.",
		Domain:       DomainSchedules,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","description":"Optional: restrict to schedules bound to this task ID."},"title":{"type":"string","description":"Optional partial (case-insensitive substring) task title match."},"enabled":{"type":"boolean","description":"Optional: filter by enabled (true) or disabled (false) schedules."},"limit":{"type":"integer","minimum":1,"maximum":50,"description":"Max results to return (default 20, capped at 50)."},"offset":{"type":"integer","minimum":0,"description":"Number of results to skip for pagination."}},"additionalProperties":false}`),
	},
	{
		Name:         "schedule_task",
		Description:  "Create a schedule for a task.",
		Domain:       DomainSchedules,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"time":{"type":"string","pattern":"^(?:[01][0-9]|2[0-3]):[0-5][0-9]$","description":"Time in exact 24-hour HH:MM format (00:00-23:59)."},"repeat":{"type":"string","enum":["once","daily","weekly","monthly","hours","hourly","minutes","seconds"]},"interval":{"type":"integer","minimum":1},"days":{"type":"array","description":"For weekly schedules, accepted weekday spellings are sun, mon, tue, wed, thu, fri, and sat.","items":{"type":"string","enum":["sun","mon","tue","wed","thu","fri","sat"]}},"clear_context_on_start":{"type":"boolean","description":"Clear prior model conversation context when each scheduled run starts; defaults to true."}},"required":["time"],"additionalProperties":false}`),
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
		Parameters:        json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"string"},"task_id":{"type":"string"},"title":{"type":"string"}},"additionalProperties":false}`),
	},
	{
		Name:         "modify_schedule",
		Description:  "Modify an existing schedule.",
		Domain:       DomainSchedules,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"string"},"task_id":{"type":"string"},"title":{"type":"string"},"time":{"type":"string","pattern":"^(?:[01][0-9]|2[0-3]):[0-5][0-9]$","description":"Time in exact 24-hour HH:MM format (00:00-23:59)."},"repeat":{"type":"string","enum":["once","daily","weekly","monthly","hours","hourly","minutes","seconds"]},"interval":{"type":"integer","minimum":1},"days":{"type":"array","description":"For weekly schedules, accepted weekday spellings are sun, mon, tue, wed, thu, fri, and sat.","items":{"type":"string","enum":["sun","mon","tue","wed","thu","fri","sat"]}},"enabled":{"type":"boolean"},"clear_context_on_start":{"type":"boolean","description":"Whether each scheduled start clears prior model conversation context."}},"additionalProperties":false}`),
	},

	// --- Alerts and actionable notifications domain ---
	{
		Name:         "list_alerts",
		Description:  "List project-scoped alerts and actionable notifications with stable pagination and lifecycle filters.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string","description":"Optional same-project assertion. Omit it to use the persisted caller task's project context; never discover or reuse a project ID."},"decision_state":{"type":"string","enum":["not_required","pending","approved","rejected","dismissed"]},"processing_state":{"type":"string","enum":["not_applicable","unclaimed","claimed","implementation_task_linked","completed","failed"]},"type":{"type":"string"},"source":{"type":"string"},"read":{"type":"boolean","description":"Optional read-state filter. Omit it to include both read and unread alerts."},"implementation_task_linked":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0}},"additionalProperties":false}`),
	},
	{
		Name:         "get_alert",
		Description:  "Get one notification in the caller's project before claiming or processing it.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "create_alert",
		Description:  "Create a project-scoped operational alert.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"message":{"type":"string"},"severity":{"type":"string","enum":["info","warning","error"]},"type":{"type":"string","enum":["custom","task_failed","task_needs_followup"]},"task_id":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
	},
	{
		Name:         "create_notification",
		Description:  "Create a project-scoped actionable notification for human review. Approval authorizes task creation only, not merge, release, or deployment.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"type":{"type":"string","maxLength":100},"title":{"type":"string","maxLength":200},"message":{"type":"string","maxLength":2000},"body":{"type":"string","maxLength":20000},"severity":{"type":"string","enum":["info","warning","error"]},"source":{"type":"string","maxLength":100},"metadata":{"type":"object"},"idempotency_key":{"type":"string","maxLength":200}},"required":["type","title"],"additionalProperties":false}`),
	},
	{
		Name:         "claim_alert",
		Description:  "Atomically claim an approved notification for the current persisted task using a bounded lease.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"},"lease_seconds":{"type":"integer","minimum":1,"maximum":86400}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "create_alert_implementation_task",
		Description:  "Idempotently create and link one backlog implementation task for a notification claimed by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"},"title":{"type":"string"},"prompt":{"type":"string"},"priority":{"type":"integer","minimum":1,"maximum":4},"tag":{"type":"string","enum":["","feature","bug"]}},"required":["alert_id","title","prompt"],"additionalProperties":false}`),
	},
	{
		Name:         "link_alert_implementation_task",
		Description:  "Link an existing same-project implementation task to a notification claimed by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"},"task_id":{"type":"string"}},"required":["alert_id","task_id"],"additionalProperties":false}`),
	},
	{
		Name:         "complete_alert_processing",
		Description:  "Mark processing completed for a notification owned by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"},"message":{"type":"string","maxLength":2000}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "fail_alert_processing",
		Description:  "Mark processing failed with retryable diagnostic context for a notification owned by the current task.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"},"message":{"type":"string","maxLength":2000}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "release_alert_claim",
		Description:  "Release the current task's unlinked claim so another scheduled scan can retry it.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project_id":{"type":"string"},"alert_id":{"type":"string"}},"required":["alert_id"],"additionalProperties":false}`),
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
		Parameters:        json.RawMessage(`{"type":"object","properties":{"alert_id":{"type":"string"}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "toggle_alert",
		Description:  "Mark an alert as read in the caller's project.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"alert_id":{"type":"string"}},"required":["alert_id"],"additionalProperties":false}`),
	},

	// --- Personality domain ---
	{
		Name:         "list_personalities",
		Description:  "List available personality presets.",
		Domain:       DomainPersonality,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "get_personality",
		Description:  "Get the current active personality preset.",
		Domain:       DomainPersonality,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "set_personality",
		Description:  "Set the global personality preset.",
		Domain:       DomainPersonality,
		Access:       AccessWrite,
		Sensitivity:  SensitivitySystemWide,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"personality":{"type":"string"}},"required":["personality"],"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "get_model",
		Description:  "Get details for a specific LLM model by name or id.",
		Domain:       DomainModels,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"model_id":{"type":"string"},"name":{"type":"string"}},"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "project_info",
		Description:  "View details for the current project.",
		Domain:       DomainProjects,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "get_current_project",
		Description:  "Get the name and id of the currently active project.",
		Domain:       DomainProjects,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "switch_project",
		Description:  "Switch active project by id or name.",
		Domain:       DomainProjects,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"project":{"type":"string"}},"required":["project"],"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},

	// --- Memory domain (read-only, scoped by selected-memory runtime tools) ---
	{
		Name:         "memory_view",
		Description:  "Load an authorized managed memory selected for this turn. Use only handles listed in the selected memory index.",
		Domain:       DomainMemory,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string","description":"Selected memory handle/file, e.g. provider_architecture.md"}},"required":["handle"],"additionalProperties":false}`),
	},

	// --- Automations domain (project-scoped definition control) ---
	{
		Name:         "preview_automation_description",
		Description:  "Generate and validate an ephemeral custom or maintained-template Automation graph from a description using the same surfaced capabilities as the visual builder. This does not persist a draft or create runtime resources.",
		Domain:       DomainAutomations,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"description":{"type":"string","minLength":1,"maxLength":4000}},"required":["description"],"additionalProperties":false}`),
	},
	{
		Name:         "save_automation",
		Description:  "Generate, validate, and atomically save a custom or maintained-template Automation from the user's request using the same capabilities and Save pipeline as the visual builder. Use this when the user asks to create or save an Automation; the successful tool result includes its Live URL. Do not ask for a separate save confirmation.",
		Domain:       DomainAutomations,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     webAPISurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","enum":["template","describe","blank"]},"template_key":{"type":"string","enum":["native_sdlc","github_sdlc"]},"description":{"type":"string","maxLength":4000}},"required":["source"],"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "set_chat_mode",
		Description:  "Set the chat mode. 'orchestrate' allows full task management; 'plan' is read-only exploration.",
		Domain:       DomainChat,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"mode":{"type":"string","enum":["orchestrate","plan"]}},"required":["mode"],"additionalProperties":false}`),
	},
	{
		Name:         "list_capabilities",
		Description:  "List available chat actions for the current mode and surface.",
		Domain:       DomainChat,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
}

// helpers

func allSurfaces() []Surface {
	return []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack, SurfaceEmail, SurfaceDiscord}
}

func bothModes() []models.ChatMode {
	return []models.ChatMode{models.ChatModeOrchestrate, models.ChatModePlan}
}

func webAPISurfaces() []Surface {
	return []Surface{SurfaceWeb, SurfaceAPI}
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
				Parameters:  a.Parameters,
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
