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
	return func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		toolName := strings.ToLower(strings.TrimSpace(name))
		if toolName == "" {
			return "", false, false, nil
		}

		if actionErr := IsAllowed(toolName, mode, surface); actionErr != nil {
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

		output, err := handler(ctx, input)
		if err != nil {
			return "", true, true, err
		}
		return output, true, false, nil
	}
}

// ValidateHandlerCoverage verifies that all runtime tool definitions for the
// context have registered handlers. Used by tests to prevent drift.
func ValidateHandlerCoverage(mode models.ChatMode, surface Surface, includeThreadTools bool, handlers map[string]RuntimeActionHandler) error {
	defs := ToolDefsForContext(mode, surface, includeThreadTools)
	missing := make([]string, 0)
	for _, d := range defs {
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
