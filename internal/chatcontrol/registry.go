// Package chatcontrol provides the canonical chat capability registry.
//
// Every chat-controllable action is defined here exactly once. Tool definitions,
// mode gating, surface availability, and domain classification are all derived
// from this single source of truth so web/API/Telegram/Slack never drift.
//
// # API-domain mapping policy
//
// Chat RW (orchestrate mode):
//   - tasks: create_task, edit_task, execute_tasks, send_to_task
//   - schedules: schedule_task, delete_schedule, modify_schedule
//   - alerts: create_alert, delete_alert, toggle_alert
//   - personality: set_personality
//   - projects: switch_project
//   - chat: set_chat_mode
//
// Chat read-only (plan + orchestrate):
//   - tasks: view_task_thread
//   - projects: list_projects, project_info, get_current_project
//   - models: list_models, get_model
//   - agents: list_agents
//   - alerts: list_alerts, get_alert
//   - personality: list_personalities, get_personality
//   - settings: view_settings
//   - chat: get_chat_mode, list_capabilities
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
	"log"
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
)

// AllSurfaces is the full set of supported surfaces.
var AllSurfaces = []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack}

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

	// Parameters is the JSON Schema for the tool's input parameters.
	Parameters json.RawMessage
}

// chainSchemaProperties is the JSON Schema for the chain configuration object.
// Fully specifies all ChainConfiguration fields so the LLM can configure chaining
// in a single create_task call without needing a follow-up edit_task.
const chainSchemaProperties = `{"type":"object","properties":{"enabled":{"type":"boolean","description":"true to enable chaining, false to disable"},"trigger":{"type":"string","enum":["on_completion","on_planning_complete"],"description":"When to trigger the child task"},"child_title":{"type":"string","description":"Title for the child task (defaults to '{parent title} (Implementation)')"},"child_prompt_prefix":{"type":"string","description":"Text prepended to parent output to form the child prompt"},"child_category":{"type":"string","enum":["active","backlog"],"description":"Category for child task (defaults to parent category)"},"child_agent_id":{"type":"string","description":"Agent/model config ID for the child task"},"child_chain_config":{"type":"object","description":"Nested chain config for multi-step sequences"}},"required":["enabled"]}`

// createTaskParams is the full JSON Schema for the create_task tool.
const createTaskParams = `{"type":"object","properties":{"title":{"type":"string"},"prompt":{"type":"string"},"category":{"type":"string","enum":["active","backlog"]},"priority":{"type":"integer","minimum":1,"maximum":4},"agent_id":{"type":"string"},"agent_definition_id":{"type":"string"},"agent":{"type":"string"},"chain":` + chainSchemaProperties + `},"required":["title","prompt"],"additionalProperties":false}`

// editTaskParams is the full JSON Schema for the edit_task tool.
const editTaskParams = `{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"prompt":{"type":"string"},"category":{"type":"string","enum":["active","backlog","scheduled"]},"priority":{"type":"integer","minimum":1,"maximum":4},"tag":{"type":"string"},"agent_id":{"type":"string"},"agent_config_id":{"type":"string"},"chain":` + chainSchemaProperties + `,"attachments":{"type":"array","items":{"type":"string"}}},"required":["id"],"additionalProperties":false}`

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
		Parameters:         json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"message":{"type":"string"}},"required":["message"],"additionalProperties":false}`),
	},

	// --- Schedules domain (RW in orchestrate) ---
	{
		Name:         "schedule_task",
		Description:  "Create a schedule for a task.",
		Domain:       DomainSchedules,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string"},"title":{"type":"string"},"time":{"type":"string"},"repeat":{"type":"string"},"interval":{"type":"integer","minimum":1},"days":{"type":"array","items":{"type":"string"}}},"required":["time"],"additionalProperties":false}`),
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
		Parameters:   json.RawMessage(`{"type":"object","properties":{"schedule_id":{"type":"string"},"task_id":{"type":"string"},"title":{"type":"string"},"time":{"type":"string"},"repeat":{"type":"string"},"interval":{"type":"integer","minimum":1},"days":{"type":"array","items":{"type":"string"}},"enabled":{"type":"boolean"}},"additionalProperties":false}`),
	},

	// --- Alerts domain ---
	{
		Name:         "list_alerts",
		Description:  "List alerts for the current project.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	},
	{
		Name:         "get_alert",
		Description:  "Get a specific alert by id.",
		Domain:       DomainAlerts,
		Access:       AccessRead,
		Sensitivity:  SensitivityNormal,
		AllowedModes: bothModes(),
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"alert_id":{"type":"string"}},"required":["alert_id"],"additionalProperties":false}`),
	},
	{
		Name:         "create_alert",
		Description:  "Create a new alert.",
		Domain:       DomainAlerts,
		Access:       AccessWrite,
		Sensitivity:  SensitivityNormal,
		AllowedModes: []models.ChatMode{models.ChatModeOrchestrate},
		Surfaces:     allSurfaces(),
		Parameters:   json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"message":{"type":"string"},"severity":{"type":"string","enum":["info","warning","error"]},"type":{"type":"string","enum":["custom","task_failed","task_needs_followup"]},"task_id":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
	},
	{
		Name:              "delete_alert",
		Description:       "Delete an alert by id.",
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
		Description:  "Mark an alert as read.",
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
	return []Surface{SurfaceWeb, SurfaceAPI, SurfaceTelegram, SurfaceSlack}
}

func bothModes() []models.ChatMode {
	return []models.ChatMode{models.ChatModeOrchestrate, models.ChatModePlan}
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
	var defs []llmcontracts.RuntimeToolDefinition
	for _, a := range registry {
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
			})
		}
	}
	return defs
}

// IsAllowed checks whether an action is allowed for the given mode and surface.
// Returns an ActionError if blocked, nil if allowed.
func IsAllowed(name string, mode models.ChatMode, surface Surface) *ActionError {
	def := Get(name)
	if def == nil {
		return &ActionError{
			Action:  name,
			Code:    "unknown_action",
			Message: fmt.Sprintf("action %q is not a recognized chat capability", name),
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
		if !modeAllowed(a, mode) || !surfaceAllowed(a, surface) {
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
func LogGating(action string, mode models.ChatMode, surface Surface, allowed bool) {
	if allowed {
		log.Printf("[chatcontrol] action=%s mode=%s surface=%s allowed=true", action, mode, surface)
	} else {
		log.Printf("[chatcontrol] action=%s mode=%s surface=%s allowed=false", action, mode, surface)
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
