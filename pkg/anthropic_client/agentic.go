package anthropicclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
)

const (
	defaultAnthropicContextWindow = 200000
	compactionOutputReserveCap    = 20000
	compactionSummaryBuffer       = 13000
	compactionBlockingBuffer      = 3000
)

// DefaultCompactionThreshold mirrors Claude Code's trigger for a normal
// 200,000-token context window.
const DefaultCompactionThreshold = defaultAnthropicContextWindow - compactionOutputReserveCap - compactionSummaryBuffer

// MinCompactionThreshold is the minimum trigger accepted by Anthropic's
// server-side compaction API.
const MinCompactionThreshold = 50000

const (
	// Prefer the direct-call web tool versions for URL retrieval flows.
	// Newer web tool versions can route through provider code_execution,
	// which may hit per-turn tool budgets and cause fetch failures.
	anthropicWebSearchToolType    = "web_search_20250305"
	anthropicWebFetchToolType     = "web_fetch_20250910"
	anthropicToolOutputTokenLimit = 10000
)

// AgenticOptions configures an agentic send with tool use.
type AgenticOptions struct {
	Model         string
	MaxTokens     int
	ContextWindow int    // complete-request admission window; defaults conservatively when zero
	Effort        string // output_config.effort; empty preserves the provider default
	System        string
	WorkDir       string // working directory for tool execution
	MaxTurns      int    // max agentic loop iterations (default 25)
	DisableTools  bool   // when true, no tools are sent (chat orchestrator mode)
	// ToolOutputTokenLimit bounds each local tool result replayed to the model;
	// callbacks and the returned ToolCalls retain the complete output.
	ToolOutputTokenLimit int
	// SkipDefaultTools suppresses built-in local tools while still allowing
	// ExtraTools (for example runtime action tools) to be sent.
	SkipDefaultTools bool

	// EnableThinking enables extended thinking (reasoning before responding).
	// When true with BudgetTokens=0, uses adaptive thinking on supported models
	// (Opus 4.6+), falls back to fixed budget on others.
	// When true with BudgetTokens>0, uses that fixed budget except on models
	// that only support adaptive thinking.
	EnableThinking bool
	BudgetTokens   int

	// AutoCompaction enables server-side context compaction via the Anthropic beta API.
	// When input tokens exceed CompactionTokenThreshold, the API automatically
	// summarizes older messages into a compaction block. The compaction block is
	// round-tripped in subsequent requests to maintain compressed context.
	AutoCompaction bool
	// CompactionTokenThreshold is the input token count that triggers compaction.
	// Defaults to a Claude Code-compatible value derived from ContextWindow
	// (167,000 for the normal 200,000-token window) when zero.
	CompactionTokenThreshold int
	// CompactionInstructions provides additional instructions for the summarization.
	// For example: "Focus on code changes and decisions".
	CompactionInstructions string

	// NativeCompactionStateJSON replays a provider-native compaction block from
	// a compatible prior execution before post-checkpoint history.
	NativeCompactionStateJSON string

	// Attachments are files to include with the initial message (images, PDFs, code files).
	// They are sent as multimodal content blocks alongside the text prompt.
	Attachments []*FileAttachment

	// ExtraTools are appended to the default local tools (for example MCP tools).
	ExtraTools []ToolDefinition
	// ToolExecutor overrides tool execution. It should return (output, isError, err).
	// If nil, built-in local tool execution is used.
	ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, bool, error)
	// ToolFilter can deny tool execution by name at runtime.
	ToolFilter func(name string) bool

	// WebSearchEnabled adds Anthropic server-side web search and web fetch
	// tools to the request. These are executed server-side by Anthropic;
	// no local tool execution is needed.
	WebSearchEnabled bool

	// Callbacks for real-time output
	OnText       func(text string)                              // called for each text delta
	OnThinking   func(text string)                              // called for each thinking delta
	OnToolUse    func(name string, input json.RawMessage)       // called when a tool is invoked
	OnToolResult func(name string, output string, isError bool) // called when a tool completes
	// OnToolBoundarySteering is called after local tool results are appended and before the next model request.
	OnToolBoundarySteering func(ctx context.Context) (string, error)
	OnCompaction           func(summary string) // called when context is compacted
}

// NormalizeEffort returns an API-supported effort for the selected model.
// Unsupported models and model/effort combinations return an empty string so
// callers preserve the provider default instead of sending an invalid request.
func NormalizeEffort(model, value string) string {
	effort := strings.ToLower(strings.TrimSpace(value))
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return ""
	}

	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "claude-opus-5"),
		strings.Contains(m, "claude-sonnet-5"),
		strings.Contains(m, "claude-fable-5"),
		strings.Contains(m, "claude-mythos-5"),
		strings.Contains(m, "claude-opus-4-8"),
		strings.Contains(m, "claude-opus-4-7"):
		return effort
	case strings.Contains(m, "claude-mythos-preview"),
		strings.Contains(m, "claude-opus-4-6"),
		strings.Contains(m, "claude-sonnet-4-6"):
		if effort != "xhigh" {
			return effort
		}
	case strings.Contains(m, "claude-opus-4-5"):
		if effort == "low" || effort == "medium" || effort == "high" {
			return effort
		}
	}
	return ""
}

// AgenticResponse is the result of an agentic send.
type AgenticResponse struct {
	LastContextTokens         int    // Includes Anthropic's disjoint input/cache token buckets.
	Text                      string // final text output (all turns concatenated)
	Model                     string
	InputTokens               int
	OutputTokens              int
	CacheCreationInputTokens  int
	CacheReadInputTokens      int
	StopReason                string
	ToolCalls                 []ToolCall // log of all tool calls made
	Compacted                 bool       // true if context was compacted during this call
	NativeCompactionStateJSON string     // newly returned provider-native compaction block for durable replay
}

// agenticMessage is a message in the agentic conversation.
// Content can be a string (simple text) or []agenticBlock (tool use/results).
type agenticMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// agenticBlock is a content block that can represent text, thinking, tool_use, or tool_result.
type agenticBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`    // thinking
	Signature string          `json:"signature,omitempty"`   // thinking (required to echo back)
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   json.RawMessage `json:"content,omitempty"`     // tool_result/provider tool result
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

// MarshalJSON ensures tool-use blocks always include an object-valued input.
// Anthropic rejects echoed assistant tool_use content when the input field is
// omitted, even for empty-input tools.
func (b agenticBlock) MarshalJSON() ([]byte, error) {
	type Alias agenticBlock
	if b.Type == "tool_use" || b.Type == "server_tool_use" {
		b.Input = normalizeAnthropicToolInput(b.Input)
	}
	return json.Marshal(Alias(b))
}

// thinkingConfig configures extended thinking for the API request.
// Use BudgetTokens > 0 for a fixed budget, or 0 for adaptive (API decides).
type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// systemBlock is a system prompt content block with optional cache_control.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

// cacheControl enables prompt caching for a content block.
type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// contextManagementConfig configures server-side context management (beta).
type contextManagementConfig struct {
	Edits []contextManagementEdit `json:"edits"`
}

// contextManagementEdit is a single context management edit.
type contextManagementEdit struct {
	Type                 string              `json:"type"`
	Trigger              *inputTokensTrigger `json:"trigger,omitempty"`
	Instructions         string              `json:"instructions,omitempty"`
	PauseAfterCompaction bool                `json:"pause_after_compaction,omitempty"`
}

// inputTokensTrigger configures the token threshold for compaction.
type inputTokensTrigger struct {
	Type  string `json:"type"`
	Value int    `json:"value"`
}

// compactionBlockJSON is used for marshaling compaction blocks with proper null handling.
// When Content is nil, it marshals as "content": null (failed compaction).
type compactionBlockJSON struct {
	Type             string  `json:"type"`
	Content          *string `json:"content"`
	EncryptedContent *string `json:"encrypted_content,omitempty"`
}

// CompactionTriggerLimit returns the Claude Code-compatible automatic
// compaction trigger for an Anthropic context window.
func CompactionTriggerLimit(contextWindow int) int {
	if contextWindow <= 0 {
		contextWindow = defaultAnthropicContextWindow
	}
	return max(MinCompactionThreshold, contextWindow-compactionOutputReserveCap-compactionSummaryBuffer)
}

// CompactionOutputReserve returns the output headroom Claude Code uses when
// calculating compaction limits. Large model output limits are capped at 20k.
func CompactionOutputReserve(maxOutputTokens int) int {
	if maxOutputTokens <= 0 {
		return compactionOutputReserveCap
	}
	return min(maxOutputTokens, compactionOutputReserveCap)
}

// CompactionBlockingLimit returns the latest input size accepted while native
// compaction is still able to run.
func CompactionBlockingLimit(contextWindow int) int {
	if contextWindow <= 0 {
		contextWindow = defaultAnthropicContextWindow
	}
	return max(1, contextWindow-compactionOutputReserveCap-compactionBlockingBuffer)
}

const nativeCompactionStateVersion = 1

type nativeCompactionState struct {
	Version  int              `json:"version"`
	Messages []agenticMessage `json:"messages"`
}

func decodeNativeCompactionState(raw string) ([]agenticMessage, error) {
	var state nativeCompactionState
	if err := json.Unmarshal([]byte(raw), &state); err == nil && state.Version == nativeCompactionStateVersion {
		if hasLeadingCompactionMessage(state.Messages) {
			return state.Messages, nil
		}
		// Normalize checkpoints written before compaction blocks were retained
		// under their provider-returned assistant role.
		if hasLeadingCompactionMessageWithRole(state.Messages, "user") {
			state.Messages[0].Role = "assistant"
			if len(state.Messages) > 1 && state.Messages[1].Role == "assistant" {
				first, firstErr := contentBlocksJSON(state.Messages[0].Content)
				second, secondErr := contentBlocksJSON(state.Messages[1].Content)
				if firstErr != nil || secondErr != nil {
					return nil, fmt.Errorf("invalid legacy compaction message envelope")
				}
				state.Messages[0].Content = append(first, second...)
				state.Messages = append(state.Messages[:1], state.Messages[2:]...)
			}
			return state.Messages, nil
		}
		return nil, fmt.Errorf("invalid compaction message envelope")
	}

	// Accept checkpoints produced before the state envelope retained the
	// post-compaction assistant/tool continuation.
	var block compactionBlockJSON
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		return nil, err
	}
	if block.Type != "compaction" || block.Content == nil {
		return nil, fmt.Errorf("invalid compaction block")
	}
	return []agenticMessage{{Role: "assistant", Content: []compactionBlockJSON{block}}}, nil
}

func hasLeadingCompactionMessage(messages []agenticMessage) bool {
	return hasLeadingCompactionMessageWithRole(messages, "assistant")
}

func hasLeadingCompactionMessageWithRole(messages []agenticMessage, role string) bool {
	if len(messages) == 0 || messages[0].Role != role {
		return false
	}
	blocks, err := contentBlocksJSON(messages[0].Content)
	if err != nil || len(blocks) == 0 {
		return false
	}
	var block compactionBlockJSON
	if err := json.Unmarshal(blocks[0], &block); err != nil {
		return false
	}
	return block.Type == "compaction" && block.Content != nil
}

func contentBlocksJSON(content any) ([]json.RawMessage, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// agenticRequest is the API request body for agentic sends.
type agenticRequest struct {
	Model             string                   `json:"model"`
	MaxTokens         int                      `json:"max_tokens"`
	Messages          []agenticMessage         `json:"messages"`
	Tools             []ToolDefinition         `json:"tools,omitempty"`
	Stream            bool                     `json:"stream,omitempty"`
	System            []systemBlock            `json:"system,omitempty"`
	Thinking          *thinkingConfig          `json:"thinking,omitempty"`
	OutputConfig      *outputConfig            `json:"output_config,omitempty"`
	ContextManagement *contextManagementConfig `json:"context_management,omitempty"`
	// rawTools overrides Tools when set. Used when the tools array contains
	// mixed types (e.g. client tools + server tools for web search).
	rawTools json.RawMessage
}

// MarshalJSON implements custom JSON marshaling to support mixed tool types.
// When rawTools is set, it is used instead of the typed Tools slice.
func (r agenticRequest) MarshalJSON() ([]byte, error) {
	type Alias agenticRequest
	if r.rawTools != nil {
		// Build a map from the struct, then replace tools with raw JSON.
		a := Alias(r)
		a.Tools = nil
		base, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		// Insert rawTools into the JSON object.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(base, &m); err != nil {
			return nil, err
		}
		m["tools"] = r.rawTools
		return json.Marshal(m)
	}
	return json.Marshal(Alias(r))
}

// serverToolDef is a provider-executed server tool definition.
type serverToolDef struct {
	Type string `json:"type"` // e.g. "web_search_20260209"
	Name string `json:"name,omitempty"`
}

// agenticAPIResponse is the non-streaming API response with tool use support.
type agenticAPIResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []agenticBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// SendAgentic sends a message with tool use enabled, executing an agentic loop
// until the model finishes (stop_reason != "tool_use") or MaxTurns is reached.
// It uses streaming for each API call and reports progress via callbacks.
func (c *Client) SendAgentic(ctx context.Context, prompt string, opts *AgenticOptions) (*AgenticResponse, error) {
	if opts == nil {
		opts = &AgenticOptions{}
	}
	if opts.Model == "" {
		opts.Model = DefaultModel
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	// MaxTurns=0 means no limit (matches Claude Code CLI default).
	// The loop runs until the model finishes or context/budget is exhausted.
	if opts.MaxTurns == 0 {
		opts.MaxTurns = math.MaxInt32
	}
	if opts.WorkDir == "" {
		opts.WorkDir = "."
	}

	var tools []ToolDefinition
	if !opts.DisableTools {
		if !opts.SkipDefaultTools {
			tools = DefaultTools()
		}
		if len(opts.ExtraTools) > 0 {
			tools = append(tools, opts.ExtraTools...)
		}
		tools = filterToolDefinitions(tools, opts.ToolFilter)
	}

	// Build initial messages from a compatible native checkpoint, history, and new prompt.
	messages := make([]agenticMessage, 0, len(c.History)+2)
	if state := strings.TrimSpace(opts.NativeCompactionStateJSON); state != "" {
		checkpointMessages, err := decodeNativeCompactionState(state)
		if err != nil {
			return nil, fmt.Errorf("decode Anthropic native compaction state: %w", err)
		}
		messages = append(messages, checkpointMessages...)
	}
	for _, msg := range c.History {
		messages = append(messages, agenticMessage{Role: msg.Role, Content: msg.Content})
	}

	// If attachments are present, build multimodal content blocks for the prompt
	if len(opts.Attachments) > 0 {
		contentBlocks, blockErr := buildContentBlocks(prompt, opts.Attachments)
		if blockErr != nil {
			return nil, fmt.Errorf("build content blocks: %w", blockErr)
		}
		messages = append(messages, agenticMessage{Role: "user", Content: contentBlocks})
	} else {
		messages = append(messages, agenticMessage{Role: "user", Content: prompt})
	}

	result := &AgenticResponse{Model: opts.Model}
	var allText strings.Builder
	hasDurableCompactionState := false

	for turn := 0; turn < opts.MaxTurns; turn++ {
		// Send request (streaming)
		resp, err := httpretry.DoStreamTurn(ctx, httpretry.StreamTurnPolicy{
			RetryConnectionFailuresWithoutBudget: true,
			OnRetry: func(event httpretry.RetryEvent) {
				if httpretry.IsConnectionSetupFailure(event.Err) {
					applog.Infof("[anthropicclient] reconnecting agentic turn %d after connection error in %v: %v", turn+1, event.Delay, event.Err)
					return
				}
				applog.Infof("[anthropicclient] retrying agentic turn %d after stream/transport error, retry attempt %d/%d in %v: %v",
					turn+1, event.Attempt, event.MaxRetries, event.Delay, event.Err)
			},
		}, func(attemptCtx context.Context) (*turnResult, error) {
			return c.sendAgenticTurn(attemptCtx, messages, tools, opts)
		})
		if err != nil {
			return nil, categorizeAnthropicProviderError(fmt.Errorf("turn %d: %w", turn+1, err))
		}

		result.InputTokens += resp.billedInputTokens
		result.LastContextTokens = 0
		if input := resp.inputTokens + resp.cacheCreationInputTokens + resp.cacheReadInputTokens; input > 0 {
			result.LastContextTokens = input + resp.outputTokens
		}
		result.OutputTokens += resp.billedOutputTokens
		result.CacheCreationInputTokens += resp.billedCacheCreationInputTokens
		result.CacheReadInputTokens += resp.billedCacheReadInputTokens
		result.StopReason = resp.stopReason
		if resp.model != "" {
			result.Model = resp.model
		}

		// Handle compaction: if the API compacted context, replace old messages
		// with the assistant response beginning at the compaction block. Anthropic
		// ignores everything before that block on subsequent requests.
		if resp.compaction != nil {
			result.Compacted = true
			if resp.compaction.content != nil {
				hasDurableCompactionState = true
				applog.Infof("[anthropicclient] context compacted on turn %d, summary_len=%d", turn+1, len(*resp.compaction.content))
				if opts.OnCompaction != nil {
					opts.OnCompaction(*resp.compaction.content)
				}
			} else {
				hasDurableCompactionState = false
				applog.Infof("[anthropicclient] compaction failed on turn %d (null content), round-tripping as no-op", turn+1)
			}
		}

		// Ensure contentBlocks is never nil (nil slice marshals as JSON null,
		// but the API requires an array for assistant message content).
		if resp.contentBlocks == nil {
			resp.contentBlocks = []agenticBlock{}
		}

		// A compaction block is part of the assistant response and must remain
		// before any continuation blocks returned in that same response.
		if resp.compaction != nil {
			blocks := make([]any, 0, len(resp.contentBlocks)+1)
			blocks = append(blocks, compactionBlockJSON{Type: "compaction", Content: resp.compaction.content, EncryptedContent: resp.compaction.encryptedContent})
			for _, block := range resp.contentBlocks {
				blocks = append(blocks, block)
			}
			messages = []agenticMessage{{Role: "assistant", Content: blocks}}
		} else {
			messages = append(messages, agenticMessage{
				Role:    "assistant",
				Content: resp.contentBlocks,
			})
		}

		// Collect text blocks for this turn.
		turnText := ""
		for _, block := range resp.contentBlocks {
			if block.Type == "text" && block.Text != "" {
				turnText += block.Text
			}
		}

		// Provider tool callbacks are emitted in stream-order by the parser.
		hasServerToolResults := false
		for _, block := range resp.contentBlocks {
			if isAnthropicProviderToolResultBlockType(block.Type) {
				hasServerToolResults = true
				break
			}
		}

		// If this is not a tool-related continuation stop, we're done.
		// "pause_turn" is used by provider-side tool loops (for example web search)
		// and must continue with another request using the same conversation.
		if resp.stopReason != "tool_use" && resp.stopReason != "pause_turn" {
			allText.WriteString(turnText)
			// If max_tokens was hit, log a warning — the response may be truncated
			if resp.stopReason == "max_tokens" {
				applog.Infof("[anthropicclient] turn %d hit max_tokens limit, response may be truncated", turn+1)
			}
			break
		}

		// Execute local tools and collect results (initialize as empty slice, not nil,
		// since nil marshals as JSON null and the API requires an array).
		toolUseBlocks := make([]agenticBlock, 0, len(resp.contentBlocks))
		for _, block := range resp.contentBlocks {
			if block.Type != "tool_use" {
				continue
			}
			if isAnthropicProviderNativeToolName(block.Name) {
				continue
			}
			if opts.OnToolUse != nil {
				opts.OnToolUse(block.Name, block.Input)
			}
			toolUseBlocks = append(toolUseBlocks, block)
		}

		executed := executeAnthropicToolUses(ctx, opts, toolUseBlocks)
		toolResults := make([]agenticBlock, 0, len(executed))
		for _, exec := range executed {
			if opts.OnToolResult != nil {
				opts.OnToolResult(exec.block.Name, exec.output, exec.isError)
			}

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:   exec.block.Name,
				Output: exec.output,
				Error:  exec.isError,
			})

			toolResults = append(toolResults, agenticBlock{
				Type:      "tool_result",
				ToolUseID: exec.block.ID,
				Content:   anthropicStringContentRaw(truncateAnthropicToolOutputForModelInput(exec.output, opts.ToolOutputTokenLimit)),
				IsError:   exec.isError,
			})
		}

		// For server-side pause_turn, continue the loop by re-sending the same
		// conversation context (no extra user message required).
		if resp.stopReason == "pause_turn" {
			allText.WriteString(turnText)
			continue
		}

		// Safety: if stop_reason was tool_use but we found no tool_use blocks
		// AND no server tool results, break to avoid an infinite loop.
		if len(toolResults) == 0 && !hasServerToolResults {
			allText.WriteString(turnText)
			applog.Infof("[anthropicclient] stop_reason=tool_use but no tool_use blocks found, breaking")
			break
		}

		// When only server tools were used (no local tool results), the server
		// tool results are already in the assistant contentBlocks. Continue the
		// loop without adding a user tool_result message.
		if len(toolResults) == 0 && hasServerToolResults {
			allText.WriteString(turnText)
			continue
		}

		// Add tool results as user message.
		userBlocks := append([]agenticBlock(nil), toolResults...)
		if opts.OnToolBoundarySteering != nil {
			steering, err := opts.OnToolBoundarySteering(ctx)
			if err != nil {
				return nil, fmt.Errorf("tool-boundary steering: %w", err)
			}
			if steering = strings.TrimSpace(steering); steering != "" {
				userBlocks = append(userBlocks, agenticBlock{Type: "text", Text: steering})
			}
		}
		messages = append(messages, agenticMessage{
			Role:    "user",
			Content: userBlocks,
		})
		allText.WriteString(turnText)
	}

	if hasDurableCompactionState {
		encodedState, err := json.Marshal(nativeCompactionState{Version: nativeCompactionStateVersion, Messages: messages})
		if err != nil {
			return nil, fmt.Errorf("encode Anthropic native compaction state: %w", err)
		}
		result.NativeCompactionStateJSON = string(encodedState)
	}
	result.Text = allText.String()

	// Update client history with the final state
	c.History = append(c.History, Message{Role: "user", Content: prompt})
	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})

	return result, nil
}

func normalizeAnthropicToolInput(input json.RawMessage) json.RawMessage {
	raw := bytes.TrimSpace(input)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func isAnthropicProviderNativeToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "web_search",
		"web_search_20250305", "web_search_20260209",
		"web_fetch",
		"web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309":
		return true
	default:
		return false
	}
}

func isAnthropicProviderToolResultBlockType(blockType string) bool {
	t := strings.ToLower(strings.TrimSpace(blockType))
	if t == "" {
		return false
	}
	// Provider-managed tool results use "<tool_name>_tool_result" block types
	// (for example web_fetch_tool_result, code_execution_tool_result).
	return strings.HasSuffix(t, "_tool_result") && t != "tool_result"
}

func anthropicProviderToolResultName(block agenticBlock) string {
	name := strings.TrimSpace(block.Name)
	if name != "" {
		return name
	}
	t := strings.ToLower(strings.TrimSpace(block.Type))
	switch t {
	case "web_search_tool_result":
		return "web_search"
	case "web_fetch_tool_result":
		return "web_fetch"
	}
	if strings.HasSuffix(t, "_tool_result") {
		return strings.TrimSuffix(t, "_tool_result")
	}
	return ""
}

func truncateAnthropicToolOutputForModelInput(output string, tokenLimit int) string {
	if output == "" {
		return output
	}
	if tokenLimit <= 0 {
		tokenLimit = anthropicToolOutputTokenLimit
	}
	byteLimit := tokenestimate.ByteBudget(tokenLimit)
	if len(output) <= byteLimit {
		return output
	}
	const marker = "\n\n[Tool output truncated to fit model context; middle content omitted]\n\n"
	return tokenestimate.TruncateMiddle(output, byteLimit, marker)
}

func anthropicStringContentRaw(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		// json.Marshal on string should never fail; keep a quoted fallback.
		return json.RawMessage(`""`)
	}
	return json.RawMessage(b)
}

func anthropicBlockContentString(content json.RawMessage) string {
	raw := bytes.TrimSpace(content)
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	return string(raw)
}

func providerToolEventKey(toolUseID, name string) string {
	toolUseID = strings.TrimSpace(toolUseID)
	if toolUseID != "" {
		return "id:" + toolUseID
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	return "name:" + name
}

func inferProviderToolUseInput(name string, content json.RawMessage) json.RawMessage {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(bytes.TrimSpace(content)) == 0 {
		return json.RawMessage(`{}`)
	}

	var generic interface{}
	if err := json.Unmarshal(content, &generic); err != nil {
		return json.RawMessage(`{}`)
	}

	switch name {
	case "web_fetch":
		if url, ok := findNestedStringField(generic, "url"); ok {
			return rawJSONMapWithString("url", url)
		}
	case "web_search":
		if q, ok := findNestedStringField(generic, "query"); ok {
			return rawJSONMapWithString("query", q)
		}
	}
	return json.RawMessage(`{}`)
}

func findNestedStringField(value interface{}, key string) (string, bool) {
	switch v := value.(type) {
	case map[string]interface{}:
		if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
			return s, true
		}
		for _, child := range v {
			if s, ok := findNestedStringField(child, key); ok {
				return s, true
			}
		}
	case []interface{}:
		for _, child := range v {
			if s, ok := findNestedStringField(child, key); ok {
				return s, true
			}
		}
	}
	return "", false
}

func rawJSONMapWithString(key, value string) json.RawMessage {
	b, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(b)
}

func inferProviderResultToolName(content json.RawMessage) string {
	raw := bytes.TrimSpace(content)
	if len(raw) == 0 {
		return ""
	}

	var generic interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		return ""
	}

	typ, ok := findNestedStringField(generic, "type")
	if !ok {
		return ""
	}
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		return ""
	}

	switch {
	case strings.HasPrefix(typ, "web_fetch"):
		return "web_fetch"
	case strings.HasPrefix(typ, "web_search"):
		return "web_search"
	case strings.HasPrefix(typ, "code_execution"):
		return "code_execution"
	case strings.HasSuffix(typ, "_result"):
		return strings.TrimSuffix(typ, "_result")
	default:
		return ""
	}
}

func isAnthropicCodeExecutionToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "code_execution",
		"code_execution_20250522", "code_execution_20250825", "code_execution_20260120",
		"bash_code_execution":
		return true
	default:
		return false
	}
}

func anthropicTurnHasWebToolActivity(blocks []agenticBlock) bool {
	for _, block := range blocks {
		name := strings.TrimSpace(block.Name)
		switch block.Type {
		case "tool_use", "server_tool_use", "server_tool_result":
			if name == "" && block.Type == "server_tool_result" {
				name = inferProviderResultToolName(block.Content)
			}
			if isAnthropicProviderNativeToolName(name) {
				return true
			}
		default:
			if !isAnthropicProviderToolResultBlockType(block.Type) {
				continue
			}
			name = anthropicProviderToolResultName(block)
			if name == "" {
				name = inferProviderResultToolName(block.Content)
			}
			if isAnthropicProviderNativeToolName(name) {
				return true
			}
		}
	}
	return false
}

func anthropicTurnHasCodeExecutionTooManyRequests(blocks []agenticBlock) bool {
	for _, block := range blocks {
		if !(block.Type == "server_tool_result" || isAnthropicProviderToolResultBlockType(block.Type)) {
			continue
		}
		name := strings.TrimSpace(block.Name)
		if name == "" {
			name = anthropicProviderToolResultName(block)
		}
		if name == "" {
			name = inferProviderResultToolName(block.Content)
		}
		rawLower := bytes.ToLower(bytes.TrimSpace(block.Content))
		if len(rawLower) == 0 {
			continue
		}

		isCodeExec := isAnthropicCodeExecutionToolName(name) ||
			bytes.Contains(rawLower, []byte("code_execution")) ||
			bytes.Contains(rawLower, []byte("bash_code_execution"))
		if !isCodeExec {
			continue
		}
		if bytes.Contains(rawLower, []byte("too_many_requests")) {
			return true
		}
	}
	return false
}

type anthropicToolExecutionResult struct {
	block   agenticBlock
	output  string
	isError bool
}

func executeAnthropicToolUses(ctx context.Context, opts *AgenticOptions, blocks []agenticBlock) []anthropicToolExecutionResult {
	if len(blocks) == 0 {
		return nil
	}

	results := make([]anthropicToolExecutionResult, len(blocks))
	runOne := func(i int) {
		block := blocks[i]
		output, isError := runAnthropicToolUse(ctx, opts, block.Name, block.Input)
		results[i] = anthropicToolExecutionResult{
			block:   block,
			output:  output,
			isError: isError,
		}
	}
	if requestIndex := exclusiveAnthropicRequestUserInput(blocks); requestIndex >= 0 {
		for i, block := range blocks {
			if i == requestIndex {
				runOne(i)
				continue
			}
			results[i] = anthropicToolExecutionResult{
				block: block, output: "tool call rejected: request_user_input must complete in its own model turn", isError: true,
			}
		}
		return results
	}

	if len(blocks) > 1 && allAnthropicToolsReadOnly(blocks) {
		var wg sync.WaitGroup
		wg.Add(len(blocks))
		for i := range blocks {
			go func(idx int) {
				defer wg.Done()
				runOne(idx)
			}(i)
		}
		wg.Wait()
		return results
	}

	for i := range blocks {
		runOne(i)
	}
	return results
}

func exclusiveAnthropicRequestUserInput(blocks []agenticBlock) int {
	if len(blocks) < 2 {
		return -1
	}
	for i, block := range blocks {
		if block.Name == "request_user_input" {
			return i
		}
	}
	return -1
}

func runAnthropicToolUse(ctx context.Context, opts *AgenticOptions, name string, input json.RawMessage) (string, bool) {
	applog.Infof("[anthropicclient] executing tool %s", name)
	output := ""
	isError := false
	var err error
	if opts.ToolFilter != nil && !opts.ToolFilter(name) {
		isError = true
		output = fmt.Sprintf("tool %s is not allowed by this agent", name)
	} else if opts.ToolExecutor != nil {
		output, isError, err = opts.ToolExecutor(ctx, name, input)
		if err != nil {
			isError = true
			output = err.Error()
		}
	} else {
		output, err = ExecuteTool(ctx, opts.WorkDir, name, input)
		if err != nil {
			isError = true
			output = err.Error()
		}
	}
	return output, isError
}

func allAnthropicToolsReadOnly(blocks []agenticBlock) bool {
	for _, block := range blocks {
		switch block.Name {
		case "read_file", "list_files", "grep_search":
			continue
		default:
			return false
		}
	}
	return true
}

// turnResult holds the parsed result of a single API turn.
type turnResult struct {
	contentBlocks []agenticBlock
	stopReason    string
	model         string
	// Top-level usage describes the final message iteration and therefore the
	// live context after server-side compaction.
	inputTokens              int
	outputTokens             int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
	// Billed usage includes compaction sampling passes, which Anthropic excludes
	// from the top-level usage fields.
	billedInputTokens              int
	billedOutputTokens             int
	billedCacheCreationInputTokens int
	billedCacheReadInputTokens     int
	// compaction holds the compaction summary if the API compacted context.
	// nil = no compaction, non-nil with nil *string = failed compaction.
	compaction *compactionResult
}

// compactionResult holds the result of a server-side compaction.
type compactionResult struct {
	// content is the compaction summary. nil means compaction failed.
	content          *string
	encryptedContent *string
}

type streamUsage struct {
	InputTokens              *int                `json:"input_tokens"`
	OutputTokens             *int                `json:"output_tokens"`
	CacheCreationInputTokens *int                `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int                `json:"cache_read_input_tokens"`
	CacheCreation            *usageCacheCreation `json:"cache_creation,omitempty"`
	CacheRead                *usageCacheRead     `json:"cache_read,omitempty"`
	Iterations               []UsageIteration    `json:"iterations,omitempty"`
}

func applyStreamUsage(result *turnResult, raw json.RawMessage, iterationUsage *[]UsageIteration) {
	if len(raw) == 0 {
		return
	}
	var usage streamUsage
	if json.Unmarshal(raw, &usage) != nil {
		return
	}
	if usage.InputTokens != nil {
		result.inputTokens = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		result.outputTokens = *usage.OutputTokens
	}
	if usage.CacheCreationInputTokens != nil || usage.CacheCreation != nil {
		result.cacheCreationInputTokens = 0
		if usage.CacheCreationInputTokens != nil {
			result.cacheCreationInputTokens += *usage.CacheCreationInputTokens
		}
		if usage.CacheCreation != nil {
			result.cacheCreationInputTokens += usage.CacheCreation.InputTokens
		}
	}
	if usage.CacheReadInputTokens != nil || usage.CacheRead != nil {
		result.cacheReadInputTokens = 0
		if usage.CacheReadInputTokens != nil {
			result.cacheReadInputTokens += *usage.CacheReadInputTokens
		}
		if usage.CacheRead != nil {
			result.cacheReadInputTokens += usage.CacheRead.InputTokens
		}
	}
	if usage.Iterations != nil {
		*iterationUsage = usage.Iterations
	}
}

func finalizeTurnUsage(result *turnResult, iterations []UsageIteration) {
	if len(iterations) == 0 {
		result.billedInputTokens = result.inputTokens
		result.billedOutputTokens = result.outputTokens
		result.billedCacheCreationInputTokens = result.cacheCreationInputTokens
		result.billedCacheReadInputTokens = result.cacheReadInputTokens
		return
	}
	for _, usage := range iterations {
		result.billedInputTokens += usage.InputTokens
		result.billedOutputTokens += usage.OutputTokens
		result.billedCacheCreationInputTokens += usage.CacheCreationInputTokens + usage.CacheCreation.InputTokens
		result.billedCacheReadInputTokens += usage.CacheReadInputTokens + usage.CacheRead.InputTokens
	}
}

// CompactionBetaHeader is the beta feature flag for server-side compaction.
const CompactionBetaHeader = "compact-2026-01-12"

// ContextManagementBetaHeader is retained for source compatibility.
// Deprecated: use CompactionBetaHeader.
const ContextManagementBetaHeader = CompactionBetaHeader

func requiresAdaptiveThinking(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(m, "claude-opus-5") ||
		strings.Contains(m, "claude-sonnet-5") ||
		strings.Contains(m, "fable-5") ||
		strings.Contains(m, "mythos-5")
}

func usesAdaptiveThinking(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(m, "opus") || requiresAdaptiveThinking(m)
}

// sendAgenticTurn sends a single streaming request and returns parsed content blocks.
type anthropicProviderError struct {
	StatusCode int
	Type       string
	Message    string
}

func (e *anthropicProviderError) Error() string {
	return fmt.Sprintf("Anthropic API error %d (%s): %s", e.StatusCode, e.Type, e.Message)
}

func categorizeAnthropicAPIError(statusCode int, body []byte, nativeCompaction bool) error {
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	providerErr := &anthropicProviderError{StatusCode: statusCode, Type: strings.TrimSpace(envelope.Error.Type), Message: strings.TrimSpace(envelope.Error.Message)}
	if providerErr.Message == "" {
		providerErr.Message = strings.TrimSpace(string(body))
	}
	typ := strings.ToLower(providerErr.Type)
	msg := strings.ToLower(providerErr.Message)
	if typ == "request_too_large" || strings.Contains(msg, "context window") || strings.Contains(msg, "too many tokens") {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Anthropic Messages", providerErr)
	}
	if nativeCompaction && (typ == "unsupported_beta" || statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed || statusCode == http.StatusNotImplemented) {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionUnsupported, "Anthropic context management", providerErr)
	}
	if nativeCompaction && (strings.Contains(msg, "context management") || strings.Contains(msg, "compaction")) {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorNativeCompactionFailed, "Anthropic context management", providerErr)
	}
	return providerErr
}

func categorizeAnthropicProviderError(err error) error {
	if err == nil {
		return nil
	}
	var categorized *llmcontracts.CategorizedError
	if errors.As(err, &categorized) {
		return err
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorTransportFailure, "Anthropic transport", err)
	}
	return err
}

func ensureAnthropicAgenticRequestFits(messages []agenticMessage, tools []ToolDefinition, opts *AgenticOptions) error {
	if opts == nil {
		return nil
	}
	window := opts.ContextWindow
	if window <= 0 {
		window = defaultAnthropicContextWindow
	}
	reserved := opts.MaxTokens
	if reserved <= 0 {
		reserved = 8192
	}
	safe := window - reserved - max(1024, window/50)
	if opts.AutoCompaction {
		safe = CompactionBlockingLimit(window)
	}
	encoded, err := json.Marshal(struct {
		Messages []agenticMessage `json:"messages"`
		Tools    []ToolDefinition `json:"tools,omitempty"`
		System   string           `json:"system,omitempty"`
	}{messages, tools, opts.System})
	if err != nil {
		return err
	}
	tokens := tokenestimate.FromByteCount(len(encoded))
	if tokens <= safe {
		return nil
	}
	return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Anthropic Messages preflight", fmt.Errorf("complete request requires %d input tokens; safe limit is %d", tokens, safe))
}

func (c *Client) sendAgenticTurn(ctx context.Context, messages []agenticMessage, tools []ToolDefinition, opts *AgenticOptions) (*turnResult, error) {
	if err := ensureAnthropicAgenticRequestFits(messages, tools, opts); err != nil {
		return nil, err
	}
	policy := httpretry.DefaultPolicy()
	policy.MaxRetries = 0
	policy.AllowReplay = true
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[anthropicclient] stream error before output, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	result, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*turnResult, bool, error) {
		attemptOpts := *opts
		observed := false
		attemptOpts.OnText = func(text string) {
			observed = true
			if opts.OnText != nil {
				opts.OnText(text)
			}
		}
		attemptOpts.OnThinking = func(text string) {
			observed = true
			if opts.OnThinking != nil {
				opts.OnThinking(text)
			}
		}
		attemptOpts.OnToolUse = func(name string, input json.RawMessage) {
			observed = true
			if opts.OnToolUse != nil {
				opts.OnToolUse(name, input)
			}
		}
		attemptOpts.OnToolResult = func(name, output string, isError bool) {
			observed = true
			if opts.OnToolResult != nil {
				opts.OnToolResult(name, output, isError)
			}
		}
		result, err := c.sendAgenticTurnOnce(attemptCtx, messages, tools, &attemptOpts)
		return result, observed, err
	})
	return result, err
}

func (c *Client) sendAgenticTurnOnce(ctx context.Context, messages []agenticMessage, tools []ToolDefinition, opts *AgenticOptions) (*turnResult, error) {
	// Build system prompt as content blocks with cache_control for prompt caching.
	// OAuth tokens require a billing attribution block as the first system entry;
	// without it the API returns 400 "Error".
	var sysBlocks []systemBlock
	if c.auth.APIKey == "" && c.auth.Token != "" {
		sysBlocks = append(sysBlocks, systemBlock{
			Type: "text",
			Text: fmt.Sprintf("x-anthropic-billing-header: cc_version=%s; cc_entrypoint=cli; cch=00000;", ClaudeCodeVersion),
		})
	}
	if opts.System != "" {
		sysBlocks = append(sysBlocks, systemBlock{
			Type:         "text",
			Text:         opts.System,
			CacheControl: &cacheControl{Type: "ephemeral"},
		})
	}

	req := agenticRequest{
		Model:     opts.Model,
		MaxTokens: opts.MaxTokens,
		Messages:  messages,
		Tools:     tools,
		Stream:    true,
		System:    sysBlocks,
	}
	if effort := NormalizeEffort(opts.Model, opts.Effort); effort != "" {
		req.OutputConfig = &outputConfig{Effort: effort}
	}

	// Add provider-native web tools when web search is enabled.
	// Anthropic expects direct versioned tool types in the tools array, not a
	// "server_tool" wrapper. Because client tools and provider tools have
	// different shapes, build a mixed raw tools array.
	if opts.WebSearchEnabled {
		serverTools := []serverToolDef{
			{Type: anthropicWebSearchToolType, Name: "web_search"},
			{Type: anthropicWebFetchToolType, Name: "web_fetch"},
		}
		mixed := make([]any, 0, len(tools)+len(serverTools))
		for _, t := range tools {
			mixed = append(mixed, t)
		}
		for _, st := range serverTools {
			mixed = append(mixed, st)
		}
		raw, err := json.Marshal(mixed)
		if err == nil {
			req.rawTools = raw
			req.Tools = nil // rawTools takes precedence
		}
	}

	if opts.EnableThinking {
		if requiresAdaptiveThinking(opts.Model) {
			// Current Claude 5 models only support adaptive thinking;
			// fixed budget_tokens errors.
			req.Thinking = &thinkingConfig{Type: "adaptive"}
		} else if opts.BudgetTokens > 0 {
			// Fixed budget: use "enabled" with explicit budget_tokens
			req.Thinking = &thinkingConfig{
				Type:         "enabled",
				BudgetTokens: opts.BudgetTokens,
			}
		} else if usesAdaptiveThinking(opts.Model) {
			// Adaptive: API decides thinking budget dynamically.
			req.Thinking = &thinkingConfig{Type: "adaptive"}
		} else {
			// Other models (Sonnet etc): use "enabled" with a default budget
			budget := opts.MaxTokens * 8 / 10
			if budget < 10000 {
				budget = 10000
			}
			req.Thinking = &thinkingConfig{
				Type:         "enabled",
				BudgetTokens: budget,
			}
		}
	}

	// Ask Anthropic to summarize older context when the input reaches the trigger.
	if opts.AutoCompaction {
		threshold := opts.CompactionTokenThreshold
		if threshold == 0 {
			threshold = CompactionTriggerLimit(opts.ContextWindow)
		}
		if threshold < MinCompactionThreshold {
			threshold = MinCompactionThreshold
		}
		req.ContextManagement = &contextManagementConfig{
			Edits: []contextManagementEdit{{
				Type: "compact_20260112",
				Trigger: &inputTokensTrigger{
					Type:  "input_tokens",
					Value: threshold,
				},
				Instructions: opts.CompactionInstructions,
			}},
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Build beta headers matching what Claude Code sends.
	// OAuth requires: claude-code beta + oauth beta + thinking beta.
	// The claude-code beta is required for OAuth subscription access.
	betaHeaders := []string{
		"interleaved-thinking-2025-05-14",
	}
	isOAuth := c.auth.APIKey == "" && c.auth.Token != ""
	if isOAuth {
		betaHeaders = append(betaHeaders, "claude-code-20250219", OAuthBetaHeader, "prompt-caching-scope-2026-01-05")
	}
	if opts.AutoCompaction {
		betaHeaders = append(betaHeaders, CompactionBetaHeader)
	}

	// OAuth uses ?beta=true query parameter (required by the beta Messages endpoint).
	endpoint := AnthropicAPIHost + "/v1/messages"
	if isOAuth {
		endpoint += "?beta=true"
	}

	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-version", AnthropicAPIVersion)
		httpReq.Header.Set("anthropic-beta", strings.Join(betaHeaders, ","))
		if c.auth.APIKey != "" {
			httpReq.Header.Set("x-api-key", c.auth.APIKey)
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+c.auth.Token)
			httpReq.Header.Set("x-app", "cli")
		}
		return httpReq, nil
	}

	tokenUsed := c.auth.Token
	resp, err := c.doRequestWithRetry(ctx, buildReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && isOAuth && c.oauthUnauthorizedHandler != nil {
		resp.Body.Close()
		tokens, recovered, recoverErr := c.oauthUnauthorizedHandler(ctx, tokenUsed)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if !recovered {
			return nil, fmt.Errorf("API error 401: OAuth unauthorized recovery did not refresh token")
		}
		c.applyOAuthTokens(tokens)
		resp, err = c.doRequestWithRetry(ctx, buildReq)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		kl := strings.ToLower(k)
		if strings.Contains(kl, "ratelimit") || strings.Contains(kl, "rate-limit") || strings.Contains(kl, "retry") || strings.Contains(kl, "x-anthropic") {
			applog.Debugf("[anthropic-headers] %s: %v", k, v)
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, httpretry.NewResponseError(resp, categorizeAnthropicAPIError(resp.StatusCode, respBody, opts.AutoCompaction))
	}

	result, err := c.parseAgenticStreamWithCallbacks(resp.Body, opts.OnText, opts.OnThinking, opts.OnToolUse, opts.OnToolResult)
	if err != nil {
		return result, httpretry.NewStreamError(err)
	}
	return result, nil
}

// parseAgenticStream parses a streaming response, handling text, tool_use, and compaction blocks.
func (c *Client) parseAgenticStream(body io.Reader, onText func(string), onThinking func(string)) (*turnResult, error) {
	return c.parseAgenticStreamWithCallbacks(body, onText, onThinking, nil, nil)
}

// parseAgenticStreamWithCallbacks parses a streaming response, handling text,
// tool use, and compaction blocks while optionally emitting tool callbacks.
func (c *Client) parseAgenticStreamWithCallbacks(
	body io.Reader,
	onText func(string),
	onThinking func(string),
	onToolUse func(string, json.RawMessage),
	onToolResult func(string, string, bool),
) (*turnResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	result := &turnResult{
		contentBlocks: make([]agenticBlock, 0), // must be non-nil: nil marshals as JSON null
	}
	seenProviderToolUses := make(map[string]struct{})
	providerToolNamesByID := make(map[string]string)
	var iterationUsage []UsageIteration

	// Track content blocks being built
	type blockState struct {
		typ           string
		text          strings.Builder // for text blocks
		thinking      strings.Builder // for thinking blocks
		signature     string          // for thinking blocks (required to echo back)
		id            string          // for tool_use
		name          string          // for tool_use
		inputJSON     strings.Builder // for tool_use (accumulated partial JSON)
		startInput    json.RawMessage // for tool_use input present in content_block_start
		content       strings.Builder // for provider tool result blocks
		inToolCallTag bool            // true when inside <tool_call>...</tool_call> in text
		tagBuf        strings.Builder // buffer for detecting partial <tool_call> tags
		compaction    strings.Builder // for compaction blocks
		encrypted     *string         // opaque compaction state to round-trip
	}
	blocks := make(map[int]*blockState)
	seenMeaningfulEvent := false
	terminal := false

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			terminal = true
			break
		}

		var event struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block,omitempty"`
			Delta        json.RawMessage `json:"delta,omitempty"`
			Message      json.RawMessage `json:"message,omitempty"`
			Usage        json.RawMessage `json:"usage,omitempty"`
			Error        json.RawMessage `json:"error,omitempty"`
			RequestID    string          `json:"request_id,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_stop":
			terminal = true
		case "error":
			var streamErr struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if event.Error != nil {
				_ = json.Unmarshal(event.Error, &streamErr)
			}
			errType := strings.TrimSpace(streamErr.Type)
			if errType == "" {
				errType = "unknown_error"
			}
			errMsg := strings.TrimSpace(streamErr.Message)
			if errMsg == "" {
				errMsg = "stream error"
			}
			if event.RequestID != "" {
				return result, fmt.Errorf("anthropic stream error %s: %s request_id=%s", errType, errMsg, event.RequestID)
			}
			return result, fmt.Errorf("anthropic stream error %s: %s", errType, errMsg)

		case "message_start":
			seenMeaningfulEvent = true
			if event.Message != nil {
				var msg struct {
					Model string          `json:"model"`
					Usage json.RawMessage `json:"usage"`
				}
				if err := json.Unmarshal(event.Message, &msg); err == nil {
					result.model = msg.Model
					applyStreamUsage(result, msg.Usage, &iterationUsage)
				}
			}

		case "content_block_start":
			seenMeaningfulEvent = true
			if event.ContentBlock != nil {
				var cb struct {
					Type      string          `json:"type"`
					ID        string          `json:"id,omitempty"`
					ToolUseID string          `json:"tool_use_id,omitempty"`
					Name      string          `json:"name,omitempty"`
					Input     json.RawMessage `json:"input,omitempty"`
					Content   json.RawMessage `json:"content,omitempty"`
					Encrypted *string         `json:"encrypted_content,omitempty"`
				}
				if err := json.Unmarshal(event.ContentBlock, &cb); err == nil {
					blockID := cb.ID
					if blockID == "" {
						blockID = cb.ToolUseID
					}
					bs := &blockState{
						typ:        cb.Type,
						id:         blockID,
						name:       cb.Name,
						startInput: cb.Input,
						encrypted:  cb.Encrypted,
					}
					// Compaction and provider tool result blocks may include content
					// in the start event.
					if len(cb.Content) > 0 && string(cb.Content) != "null" {
						if cb.Type == "compaction" {
							var summary string
							if err := json.Unmarshal(cb.Content, &summary); err == nil {
								bs.compaction.WriteString(summary)
							}
						} else if cb.Type == "server_tool_result" || isAnthropicProviderToolResultBlockType(cb.Type) {
							bs.content.Write(cb.Content)
						}
					}
					blocks[event.Index] = bs
				}
			}

		case "content_block_delta":
			seenMeaningfulEvent = true
			if event.Delta == nil {
				continue
			}
			bs := blocks[event.Index]
			if bs == nil {
				continue
			}

			var delta struct {
				Type        string  `json:"type"`
				Text        string  `json:"text,omitempty"`
				Thinking    string  `json:"thinking,omitempty"`
				Signature   string  `json:"signature,omitempty"`
				PartialJSON string  `json:"partial_json,omitempty"`
				Content     string  `json:"content,omitempty"` // for compaction_delta
				Encrypted   *string `json:"encrypted_content,omitempty"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err != nil {
				continue
			}

			switch delta.Type {
			case "thinking_delta":
				bs.thinking.WriteString(delta.Thinking)
				if onThinking != nil {
					onThinking(delta.Thinking)
				}
			case "signature_delta":
				bs.signature = delta.Signature
			case "text_delta":
				bs.text.WriteString(delta.Text)
				if onText != nil {
					// Filter <tool_call>...</tool_call> from streaming output.
					// Buffer text to detect tag boundaries across deltas.
					bs.tagBuf.WriteString(delta.Text)
					buf := bs.tagBuf.String()

					if bs.inToolCallTag {
						// Inside a tool_call tag — check if closing tag arrived
						if idx := strings.Index(buf, "</tool_call>"); idx != -1 {
							bs.inToolCallTag = false
							// Emit text after the closing tag
							after := buf[idx+len("</tool_call>"):]
							bs.tagBuf.Reset()
							if trimmed := strings.TrimSpace(after); trimmed != "" {
								bs.tagBuf.WriteString(after)
							}
						}
						// Don't emit anything while inside tag
					} else if idx := strings.Index(buf, "<tool_call>"); idx != -1 {
						// Opening tag found — emit text before it, suppress the rest
						before := buf[:idx]
						if before != "" {
							onText(before)
						}
						bs.inToolCallTag = true
						bs.tagBuf.Reset()
						bs.tagBuf.WriteString(buf[idx:])
					} else if strings.Contains(buf, "<tool_") {
						// Might be a partial opening tag — hold the buffer
					} else {
						// No tag activity — emit buffered text
						onText(buf)
						bs.tagBuf.Reset()
					}
				}
			case "input_json_delta":
				bs.inputJSON.WriteString(delta.PartialJSON)
			case "compaction_delta":
				bs.compaction.WriteString(delta.Content)
				if delta.Encrypted != nil {
					bs.encrypted = delta.Encrypted
				}
			}

		case "content_block_stop":
			seenMeaningfulEvent = true
			bs := blocks[event.Index]
			if bs == nil {
				continue
			}

			switch bs.typ {
			case "thinking":
				// Thinking blocks are passed through so they can be echoed
				// back to the API in multi-turn conversations. Anthropic rejects
				// incomplete echoed thinking blocks, and omitempty would serialize
				// an empty thinking string as a block missing the required
				// `thinking` field. Drop incomplete blocks instead of poisoning the
				// next tool-continuation turn.
				thinkingText := bs.thinking.String()
				if strings.TrimSpace(thinkingText) != "" && strings.TrimSpace(bs.signature) != "" {
					result.contentBlocks = append(result.contentBlocks, agenticBlock{
						Type:      "thinking",
						Thinking:  thinkingText,
						Signature: bs.signature,
					})
				}
			case "text":
				// Flush any remaining buffered text that wasn't inside a tag
				if onText != nil && bs.tagBuf.Len() > 0 && !bs.inToolCallTag {
					onText(bs.tagBuf.String())
					bs.tagBuf.Reset()
				}
				// Strip <tool_call> tags from the final text block content
				cleanedText := stripToolCallTags(bs.text.String())
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type: "text",
					Text: cleanedText,
				})
			case "tool_use":
				inputRaw := completedAnthropicToolInput(bs.inputJSON.String(), bs.startInput)
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type:  "tool_use",
					ID:    bs.id,
					Name:  bs.name,
					Input: inputRaw,
				})
				// Some provider-native tools surface as regular tool_use blocks.
				if isAnthropicProviderNativeToolName(bs.name) {
					key := providerToolEventKey(bs.id, bs.name)
					if key != "" {
						seenProviderToolUses[key] = struct{}{}
					}
					if id := strings.TrimSpace(bs.id); id != "" {
						providerToolNamesByID[id] = strings.TrimSpace(bs.name)
					}
					if onToolUse != nil {
						onToolUse(bs.name, inputRaw)
					}
				}
			case "server_tool_use":
				// Provider-executed server tool (web search, web fetch).
				// Treated like tool_use for round-tripping and UI, but the
				// provider handles execution — no local ExecuteTool call.
				inputRaw := completedAnthropicToolInput(bs.inputJSON.String(), bs.startInput)
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type:  "server_tool_use",
					ID:    bs.id,
					Name:  bs.name,
					Input: inputRaw,
				})
				key := providerToolEventKey(bs.id, bs.name)
				if key != "" {
					seenProviderToolUses[key] = struct{}{}
				}
				if id := strings.TrimSpace(bs.id); id != "" {
					providerToolNamesByID[id] = strings.TrimSpace(bs.name)
				}
				if onToolUse != nil {
					onToolUse(bs.name, inputRaw)
				}
			case "server_tool_result":
				// Result from a provider-executed server tool. Round-tripped
				// in the conversation alongside server_tool_use blocks.
				content := anthropicProviderResultContentRaw(bs.text.String(), bs.content.String())
				blockName := strings.TrimSpace(bs.name) // preserve provider block shape
				effectiveName := blockName
				if effectiveName == "" {
					effectiveName = strings.TrimSpace(providerToolNamesByID[strings.TrimSpace(bs.id)])
				}
				if effectiveName == "" {
					effectiveName = inferProviderResultToolName(content)
				}
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type:      "server_tool_result",
					Name:      blockName,
					ToolUseID: bs.id,
					Content:   content,
				})
				key := providerToolEventKey(bs.id, effectiveName)
				if onToolUse != nil && key != "" {
					if _, ok := seenProviderToolUses[key]; !ok {
						onToolUse(effectiveName, inferProviderToolUseInput(effectiveName, content))
						seenProviderToolUses[key] = struct{}{}
					}
				}
				if onToolResult != nil {
					onToolResult(effectiveName, anthropicBlockContentString(content), false)
				}
			case "compaction":
				summary := bs.compaction.String()
				if summary != "" {
					result.compaction = &compactionResult{content: &summary, encryptedContent: bs.encrypted}
				} else {
					// Failed compaction — content is null
					result.compaction = &compactionResult{content: nil, encryptedContent: bs.encrypted}
				}
				// Compaction blocks are NOT added to contentBlocks — they are
				// tracked separately and round-tripped as user message content.
			default:
				// Newer provider tool result block types (for example
				// web_fetch_tool_result) should still be surfaced so agentic loop
				// flow can continue without treating them as missing results.
				if isAnthropicProviderToolResultBlockType(bs.typ) {
					content := anthropicProviderResultContentRaw(bs.text.String(), bs.content.String())
					blockName := strings.TrimSpace(bs.name) // preserve provider block shape
					effectiveName := anthropicProviderToolResultName(agenticBlock{Type: bs.typ, Name: blockName})
					if effectiveName == "" {
						effectiveName = strings.TrimSpace(providerToolNamesByID[strings.TrimSpace(bs.id)])
					}
					if effectiveName == "" {
						effectiveName = inferProviderResultToolName(content)
					}
					result.contentBlocks = append(result.contentBlocks, agenticBlock{
						Type:      bs.typ,
						Name:      blockName,
						ToolUseID: bs.id,
						Content:   content,
					})
					key := providerToolEventKey(bs.id, effectiveName)
					if onToolUse != nil && key != "" {
						if _, ok := seenProviderToolUses[key]; !ok {
							onToolUse(effectiveName, inferProviderToolUseInput(effectiveName, content))
							seenProviderToolUses[key] = struct{}{}
						}
					}
					if onToolResult != nil {
						onToolResult(effectiveName, anthropicBlockContentString(content), false)
					}
				}
			}

		case "message_delta":
			seenMeaningfulEvent = true
			if event.Delta != nil {
				var delta struct {
					StopReason string `json:"stop_reason"`
				}
				if err := json.Unmarshal(event.Delta, &delta); err == nil {
					result.stopReason = delta.StopReason
				}
			}
			if event.Usage != nil {
				applyStreamUsage(result, event.Usage, &iterationUsage)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !seenMeaningfulEvent {
		return result, fmt.Errorf("empty anthropic stream: no message events received")
	}
	if !terminal {
		return result, io.ErrUnexpectedEOF
	}
	finalizeTurnUsage(result, iterationUsage)
	return result, nil
}

func completedAnthropicToolInput(inputDeltas string, startInput json.RawMessage) json.RawMessage {
	if strings.TrimSpace(inputDeltas) != "" {
		return normalizeAnthropicToolInput(json.RawMessage(inputDeltas))
	}
	return normalizeAnthropicToolInput(startInput)
}

// reToolCallBlock matches <tool_call>...</tool_call> XML blocks that some models
// emit in text output alongside proper API tool use.
var reToolCallBlock = regexp.MustCompile(`(?s)<tool_call>.*?</tool_call>\s*`)

// stripToolCallTags removes <tool_call>...</tool_call> XML blocks from text.
func stripToolCallTags(s string) string {
	return strings.TrimSpace(reToolCallBlock.ReplaceAllString(s, ""))
}

func anthropicProviderResultContentRaw(textValue, rawValue string) json.RawMessage {
	text := strings.TrimSpace(textValue)
	if text != "" {
		return anthropicStringContentRaw(text)
	}
	raw := strings.TrimSpace(rawValue)
	if raw == "" {
		return nil
	}
	return json.RawMessage(raw)
}
