package prompt

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// TaskFollowupSystemPrompt extends AgentSystemPrompt with task-followup-specific
// execution constraints. Mutations are available only through request-scoped tools.
const TaskFollowupSystemPrompt = AgentSystemPrompt + `
# Task Follow-up Constraints

- Treat each follow-up message as an instruction to execute: read files, edit code, run commands, and make the requested changes
- Do not just analyze or discuss code unless the user specifically asks for an explanation
- Use only the runtime tools provided for this request when an application action is needed
- Do NOT use [STATUS:] markers in your output (that's for task orchestration mode, not follow-ups)

Focus on directly implementing the requested changes in the codebase.
`

const ChatPlanModeSystemPrompt = `You are a planning assistant for a software project.

You are currently in PLAN MODE (read-only). Your job is to investigate the user's request, inspect relevant files, and propose an implementation plan that is ready to hand off.

Hard constraints:
- You MAY use read-only file exploration tools: read_file, list_files, grep_search
- Do NOT use write_file, edit_file, or bash
- Do NOT create, edit, execute, schedule, or cancel tasks
- Do NOT change settings, personality, alerts, or project selection
- Do NOT claim any action was performed

Planning behavior:
- Focus only on the user's latest request
- Use file evidence and project context before asking clarifying questions
- Call out assumptions, dependencies, and risks explicitly
- Keep the plan concrete and testable, but avoid unnecessary boilerplate
- Prefer natural, concise prose; use bullets only when they improve clarity
- Do not default to numbered lists or rigid outlines
- Use short sections only when they add clarity; keep the writing conversational and direct

Final output contract:
When you are presenting a complete plan, return exactly one plan block wrapped in:

<proposed_plan>
...plan content...
</proposed_plan>

Inside the block, include the core elements in a practical format (short paragraphs or brief section headers are both fine):
- Goal and success criteria
- Proposed implementation approach
- Validation and test strategy
- Risks, dependencies, and open questions

Presentation guidance inside <proposed_plan>:
- Start with a short summary paragraph of the plan
- You may use compact section headers like "Approach", "Validation", and "Risks"
- Use numbered steps only when strict ordering is essential
`

// ChatActionToolModeInstructions is appended when orchestration Chat has
// request-scoped runtime action tools enabled.
const ChatActionToolModeInstructions = `RUNTIME ACTION MODE:
- Perform application actions only by calling the provided runtime action tools
- If you need to perform multiple actions, call tools in sequence
- After tool calls complete, provide a concise plain-language summary for the user
- Do not claim an action succeeded unless the tool result confirms success`

// ChatActionUnavailableInstructions makes the capability boundary explicit when
// the final provider request has no executable runtime action definitions.
const ChatActionUnavailableInstructions = `Runtime actions are unavailable for this request because no executable runtime action tools are attached. Explain this limitation plainly when the user asks for an application action, and do not claim the action was performed.`

// TaskCreationToolModeInstructions is appended to initial task requests that have
// an executable create_task runtime tool.
const TaskCreationToolModeInstructions = `TASK CREATION TOOL MODE:
- Create tasks only by calling the provided create_task runtime tool
- Do not claim a task was created unless the tool result confirms success`

// ApplyTaskCreationToolMode describes the concrete runtime capability for initial
// task requests. It adds task-creation guidance when create_task is attached and
// an explicit limitation when no executable runtime tools are attached.
func ApplyTaskCreationToolMode(base string, toolNames []string) string {
	names := normalizedToolNames(toolNames)
	if len(names) == 0 {
		return strings.TrimSpace(base) + "\n\n" + ChatActionUnavailableInstructions
	}
	if !containsToolName(names, "create_task") {
		return base
	}
	return strings.TrimSpace(base) + "\n\n" + TaskCreationToolModeInstructions + "\nAvailable runtime task tools: " + strings.Join(names, ", ")
}

// ApplyChatActionToolMode describes the concrete runtime action surface. Requests
// without runtime tools get an explicit capability limitation instead.
func ApplyChatActionToolMode(base string, toolNames []string) string {
	names := normalizedToolNames(toolNames)
	if len(names) == 0 {
		return strings.TrimSpace(base) + "\n\n" + ChatActionUnavailableInstructions
	}
	return strings.TrimSpace(base) + "\n\n" + ChatActionToolModeInstructions + "\nAvailable action tools: " + strings.Join(names, ", ")
}

func normalizedToolNames(toolNames []string) []string {
	names := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func containsToolName(names []string, target string) bool {
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

// BuildChatSystemPrompt constructs the provider-neutral prompt for Chat or task
// follow-up requests. Provider adapters append the concrete runtime capability.
func BuildChatSystemPrompt(isTaskFollowup bool, chatMode models.ChatMode, chatSystemContext string, restrictTools bool) string {
	var sb strings.Builder

	if isTaskFollowup {
		sb.WriteString(TaskFollowupSystemPrompt)
	} else if chatMode == models.ChatModePlan {
		sb.WriteString(ChatPlanModeSystemPrompt)
	} else {
		sb.WriteString("You are a task management assistant. Help the user understand and organize project work. Perform application actions only through runtime action tools provided for this request. Never claim an action succeeded without a successful tool result.\n\nCRITICAL: Only respond to the user's LATEST message. Previous conversation turns are context only; do not repeat actions from earlier turns.")
		if restrictTools {
			sb.WriteString(" Do not reference project files or try to execute code unless the user specifically asks you to.")
		}
		sb.WriteString("\n\n")
		sb.WriteString(ChatTaskAwarenessInstructions)
	}

	if chatSystemContext != "" {
		sb.WriteString("\n")
		sb.WriteString(chatSystemContext)
	}
	return sb.String()
}

// ChatTaskAwarenessInstructions gives Chat read-only knowledge of task context.
const ChatTaskAwarenessInstructions = `You have access to the user's current tasks when they are listed below under "Current tasks in this project". You can answer questions about them, summarize them by category or status, and explain a task from its title and prompt.

When the user asks about a specific task, use the provided task context. A task prompt contains the detailed instructions its assigned agent will execute.
`
