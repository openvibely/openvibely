package contracts

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// RuntimeToolPolicyOptions describes provider-specific edges around the shared
// runtime-tool allow/deny policy.
type RuntimeToolPolicyOptions struct {
	// IsTaskFollowup keeps task-thread follow-ups on the provider's base tool policy.
	IsTaskFollowup bool
	ChatMode       models.ChatMode

	// CanonicalName maps provider wire names back to OpenVibely runtime tool names.
	// Adapters that do not rename runtime tools can leave this nil.
	CanonicalName func(string) string

	// AllowsReadOnlyTool reports provider-local read-only tools that remain usable
	// in Plan mode, such as file reads and provider-native web tools.
	AllowsReadOnlyTool func(string) bool
}

// RuntimeSkipDefaultTools reports whether a request-scoped runtime tool set asks
// adapters to suppress provider-local/default tools.
func RuntimeSkipDefaultTools(rt *RuntimeTools) bool {
	return rt != nil && rt.SkipDefaultTools
}

// DefaultPlanModeAllowsReadOnlyTool classifies provider-neutral read-only tools
// shared by OpenAI-style adapters.
func DefaultPlanModeAllowsReadOnlyTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "list_files", "grep_search",
		"web_search", "web_search_preview":
		return true
	default:
		return false
	}
}

// ComposeRuntimeToolFilter owns the shared mode/runtime/default-tool filtering
// policy used by runtime-tool-capable provider adapters.
func ComposeRuntimeToolFilter(base func(string) bool, rt *RuntimeTools, opts RuntimeToolPolicyOptions) func(string) bool {
	runtimeTools := runtimeToolAccessMap(rt, opts.CanonicalName)
	allowsReadOnly := opts.AllowsReadOnlyTool
	if allowsReadOnly == nil {
		allowsReadOnly = DefaultPlanModeAllowsReadOnlyTool
	}
	canonicalName := opts.CanonicalName
	if canonicalName == nil {
		canonicalName = func(name string) string { return name }
	}

	return func(name string) bool {
		canonical := canonicalName(name)
		access, isRuntimeTool := runtimeToolAccess(runtimeTools, canonical)
		if !opts.IsTaskFollowup {
			switch opts.ChatMode {
			case models.ChatModePlan:
				// Plan mode: read-only exploration tools only; no write/action tools.
				if isRuntimeTool && access != RuntimeToolAccessRead {
					return false
				}
				if !isRuntimeTool && !allowsReadOnly(name) {
					return false
				}
			default:
				// Orchestrate mode: action/runtime tools only (no filesystem/MCP tools).
				if !isRuntimeTool {
					return false
				}
			}
		}

		if isRuntimeTool {
			if rt != nil && rt.Filter != nil {
				allow, handled := rt.Filter(canonical)
				if handled {
					return allow
				}
			}
			return true
		}

		if RuntimeSkipDefaultTools(rt) {
			return false
		}
		if base != nil {
			return base(name)
		}
		return true
	}
}

// BuiltInToolPolicyOptions describes provider-local built-in tool grants for an
// assigned agent.
type BuiltInToolPolicyOptions struct {
	SkipDefaultTools bool
	ConfiguredTools  []string
	MapToolName      func(string) string
}

// AllowsBuiltInTool preserves the shared agent/default-tool grant semantics:
// SkipDefaultTools denies defaults, an empty configured list allows defaults,
// mapped built-ins require an explicit configured grant, and unmapped tools pass
// through to provider/runtime handling.
func AllowsBuiltInTool(name string, opts BuiltInToolPolicyOptions) bool {
	if opts.SkipDefaultTools {
		return false
	}
	if len(opts.ConfiguredTools) == 0 {
		return true
	}
	mapped := ""
	if opts.MapToolName != nil {
		mapped = opts.MapToolName(name)
	}
	if mapped == "" {
		return true
	}
	for _, tool := range opts.ConfiguredTools {
		if strings.EqualFold(strings.TrimSpace(tool), mapped) {
			return true
		}
	}
	return false
}

func runtimeToolAccessMap(rt *RuntimeTools, canonicalName func(string) string) map[string]RuntimeToolAccess {
	if rt == nil || len(rt.Definitions) == 0 {
		return nil
	}
	if canonicalName == nil {
		canonicalName = func(name string) string { return name }
	}
	out := make(map[string]RuntimeToolAccess, len(rt.Definitions))
	for _, def := range rt.Definitions {
		name := strings.ToLower(strings.TrimSpace(canonicalName(def.Name)))
		if name == "" {
			continue
		}
		access := def.Access
		if access == "" {
			access = RuntimeToolAccessWrite
		}
		out[name] = access
	}
	return out
}

func runtimeToolAccess(runtimeTools map[string]RuntimeToolAccess, name string) (RuntimeToolAccess, bool) {
	if len(runtimeTools) == 0 {
		return "", false
	}
	access, ok := runtimeTools[strings.ToLower(strings.TrimSpace(name))]
	return access, ok
}
