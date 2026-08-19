package chatcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// RuntimeActionHandler executes a chat action for a given input payload.
type RuntimeActionHandler func(ctx context.Context, input json.RawMessage) (string, error)

// BuildRuntimeToolExecutor creates a runtime-tools executor with centralized
// policy gating and handler dispatch.
func BuildRuntimeToolExecutor(mode models.ChatMode, surface Surface, handlers map[string]RuntimeActionHandler) llmcontracts.RuntimeToolExecutor {
	return buildRuntimeToolExecutor(mode, surface, handlers, false, nil)
}

// BuildRuntimeToolExecutorForActions creates an executor for a partial runtime
// bundle. Calls for actions outside allowedActions fall through so later
// composite executors can handle them.
func BuildRuntimeToolExecutorForActions(mode models.ChatMode, surface Surface, handlers map[string]RuntimeActionHandler, allowedActions map[string]bool) llmcontracts.RuntimeToolExecutor {
	return buildRuntimeToolExecutor(mode, surface, handlers, false, allowedActions)
}

// BuildLifecycleRuntimeToolExecutor creates an executor for protected lifecycle agents.
// It allows lifecycle-only actions that are intentionally hidden from ordinary chat turns.
func BuildLifecycleRuntimeToolExecutor(mode models.ChatMode, surface Surface, handlers map[string]RuntimeActionHandler) llmcontracts.RuntimeToolExecutor {
	return buildRuntimeToolExecutor(mode, surface, handlers, true, nil)
}

func isExternalRuntimeTool(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "memory_view")
}

func normalizedActionSet(actions map[string]bool) map[string]bool {
	if len(actions) == 0 {
		return nil
	}
	out := make(map[string]bool, len(actions))
	for action, allowed := range actions {
		name := strings.ToLower(strings.TrimSpace(action))
		if name == "" || !allowed {
			continue
		}
		out[name] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func buildRuntimeToolExecutor(mode models.ChatMode, surface Surface, handlers map[string]RuntimeActionHandler, includeLifecycleOnly bool, allowedActions map[string]bool) llmcontracts.RuntimeToolExecutor {
	allowed := normalizedActionSet(allowedActions)
	var lastEmptyListTasks *runtimeNoopDiscoveryCall
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		toolName := strings.ToLower(strings.TrimSpace(name))
		if toolName == "" {
			return "", false, false, nil
		}
		if allowed != nil && !allowed[toolName] {
			return "", false, false, nil
		}

		if actionErr := isAllowed(toolName, mode, surface, includeLifecycleOnly); actionErr != nil {
			// If the tool is not in the chatcontrol registry at all, return
			// handled=false so the provider's base executor can handle it
			// (e.g. grep_search, read_file, list_files are provider-native
			// file tools, not chatcontrol actions).
			if actionErr.Code == "unknown_action" {
				return "", false, false, nil
			}
			LogGating(toolName, mode, surface, false)
			return actionErr.Message, true, true, nil
		}
		LogGating(toolName, mode, surface, true)

		handler, ok := handlers[toolName]
		if !ok {
			msg := fmt.Sprintf("{\"code\":\"handler_missing\",\"action\":%q,\"surface\":%q,\"message\":\"Action is registered but no runtime handler is wired for this surface.\"}", toolName, surface)
			return msg, true, true, nil
		}

		listTasksInputKey := ""
		if toolName == "list_tasks" {
			listTasksInputKey = canonicalListTasksRuntimeInput(input)
			if lastEmptyListTasks != nil && lastEmptyListTasks.InputKey == listTasksInputKey {
				return markDuplicateNoopDiscovery(lastEmptyListTasks.Output), true, false, nil
			}
		} else {
			lastEmptyListTasks = nil
		}

		output, err := handler(ctx, input)
		if err != nil {
			if toolName == "list_tasks" {
				lastEmptyListTasks = nil
			}
			return "", true, true, err
		}
		if toolName == "list_tasks" && isEmptyExhaustedDiscoveryOutput(output) {
			lastEmptyListTasks = &runtimeNoopDiscoveryCall{InputKey: listTasksInputKey, Output: output}
		} else if toolName == "list_tasks" {
			lastEmptyListTasks = nil
		}
		return output, true, false, nil
	}
}

type runtimeNoopDiscoveryCall struct {
	InputKey string
	Output   string
}

func canonicalListTasksRuntimeInput(input json.RawMessage) string {
	payload := strings.TrimSpace(string(input))
	if payload == "" {
		payload = "{}"
	}
	var request struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Status   string `json:"status"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return payload
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	encoded, err := json.Marshal(struct {
		Query    string `json:"query"`
		Category string `json:"category"`
		Status   string `json:"status"`
		Limit    int    `json:"limit"`
		Offset   int    `json:"offset"`
	}{
		Query:    strings.TrimSpace(request.Query),
		Category: strings.ToLower(strings.TrimSpace(request.Category)),
		Status:   strings.ToLower(strings.TrimSpace(request.Status)),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return payload
	}
	return string(encoded)
}

func isEmptyExhaustedDiscoveryOutput(output string) bool {
	var result struct {
		OK      bool            `json:"ok"`
		Tasks   json.RawMessage `json:"tasks"`
		Total   int             `json:"total"`
		HasMore bool            `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.OK || result.Total != 0 || result.HasMore {
		return false
	}
	var tasks []json.RawMessage
	return json.Unmarshal(result.Tasks, &tasks) == nil && len(tasks) == 0
}

func markDuplicateNoopDiscovery(output string) string {
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return output
	}
	result["duplicate_noop"] = true
	result["note"] = "Identical list_tasks parameters already returned no tasks with total=0 and has_more=false in this run; use a different query/filter/offset or proceed without repeating this no-op discovery."
	encoded, err := json.Marshal(result)
	if err != nil {
		return output
	}
	return string(encoded)
}

// ValidateHandlerCoverage verifies that all runtime tool definitions for the
// context have registered handlers. Used by tests to prevent drift.
func ValidateHandlerCoverage(mode models.ChatMode, surface Surface, includeThreadTools bool, handlers map[string]RuntimeActionHandler) error {
	defs := ToolDefsForContext(mode, surface, includeThreadTools)
	missing := make([]string, 0)
	for _, d := range defs {
		if isExternalRuntimeTool(d.Name) {
			continue
		}
		if _, ok := handlers[d.Name]; !ok {
			missing = append(missing, d.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("missing runtime handlers for %s/%s: %s", surface, mode, strings.Join(missing, ", "))
}
