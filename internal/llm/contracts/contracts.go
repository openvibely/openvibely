package contracts

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

// Operation identifies the high-level call shape.
type Operation string

const (
	OperationDirect    Operation = "direct"
	OperationStreaming Operation = "streaming"
	OperationTask      Operation = "task"
)

// AgentRequest is the canonical provider-agnostic request contract passed to adapters.
type AgentRequest struct {
	Ctx                 context.Context
	Operation           Operation
	Message             string
	Attachments         []models.Attachment
	Agent               models.LLMConfig
	ExecID              string
	ChatHistory         []models.Execution
	ChatMode            models.ChatMode
	ChatSystemContext   string
	WorkDir             string
	Followup            bool
	ProjectInstructions string
	AgentDefinition     *models.Agent // Optional agent definition (system prompt, skills, MCP)
	PluginDirs          []string      // Optional plugin directories for CLI sessions (--plugin-dir)
	DisableTools        bool          // Optional: suppress tool/plugin execution for this request
}

// Usage tracks provider usage in a canonical shape.
// Only TotalTokens is guaranteed across all transports; the other fields are best-effort.
type Usage struct {
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
	CachedInputTokens int
	ReasoningTokens   int
	ProviderRaw       map[string]int
}

// AgentResult is the canonical provider-agnostic adapter response.
type AgentResult struct {
	Output         string
	TextOnlyOutput string
	Usage          Usage
	StopReason     string
	SessionID      string
}
