package handler

import (
	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// These adapters keep test setup concise; production uses the full runtime entry points.

// buildChatActionToolRuntime creates request-scoped runtime tools using the
// canonical registry and direct typed action handlers.
func (h *Handler) buildChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceWeb
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	// Web handler includes thread tools for orchestrate mode
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatToolDefsForProject(params, mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// buildAPIChatActionToolRuntime creates a RuntimeTools for the API surface.
func (h *Handler) buildAPIChatActionToolRuntime(params streamingResponseParams, collector *chatActionSummaryCollector) *llmcontracts.RuntimeTools {
	surface := chatcontrol.SurfaceAPI
	mode := params.ChatMode
	if mode == "" {
		mode = models.ChatModeOrchestrate
	}
	includeThread := mode == models.ChatModeOrchestrate
	defs := chatToolDefsForProject(params, mode, surface, includeThread)

	return &llmcontracts.RuntimeTools{
		Definitions: defs,
		Executor:    h.chatActionExecutor(params, collector, mode, surface),
	}
}

// chatActionToolDefinitions returns tool definitions from the canonical registry.
// Kept for backward compatibility with existing tests.
func chatActionToolDefinitions() []llmcontracts.RuntimeToolDefinition {
	return chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
}
