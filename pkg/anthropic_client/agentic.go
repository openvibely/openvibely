package anthropicclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// DefaultCompactionThreshold is the default input token count that triggers compaction.
const DefaultCompactionThreshold = 150000

// AgenticOptions configures an agentic send with tool use.
type AgenticOptions struct {
	Model        string
	MaxTokens    int
	System       string
	WorkDir      string // working directory for tool execution
	MaxTurns     int    // max agentic loop iterations (default 25)
	DisableTools bool   // when true, no tools are sent (chat orchestrator mode)

	// EnableThinking enables extended thinking (reasoning before responding).
	// When true with BudgetTokens=0, uses adaptive thinking on supported models
	// (Opus 4.6+), falls back to fixed budget on others.
	// When true with BudgetTokens>0, uses that fixed budget.
	EnableThinking bool
	BudgetTokens   int

	// AutoCompaction enables server-side context compaction via the Anthropic beta API.
	// When input tokens exceed CompactionTokenThreshold, the API automatically
	// summarizes older messages into a compaction block. The compaction block is
	// round-tripped in subsequent requests to maintain compressed context.
	AutoCompaction bool
	// CompactionTokenThreshold is the input token count that triggers compaction.
	// Defaults to DefaultCompactionThreshold (150,000) if zero.
	CompactionTokenThreshold int
	// CompactionInstructions provides additional instructions for the summarization.
	// For example: "Focus on code changes and decisions".
	CompactionInstructions string

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

	// Callbacks for real-time output
	OnText       func(text string)                              // called for each text delta
	OnThinking   func(text string)                              // called for each thinking delta
	OnToolUse    func(name string, input json.RawMessage)       // called when a tool is invoked
	OnToolResult func(name string, output string, isError bool) // called when a tool completes
	OnCompaction func(summary string)                           // called when context is compacted
}

// AgenticResponse is the result of an agentic send.
type AgenticResponse struct {
	Text                     string // final text output (all turns concatenated)
	Model                    string
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	StopReason               string
	ToolCalls                []ToolCall // log of all tool calls made
	Compacted                bool       // true if context was compacted during this call
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
	Content   string          `json:"content,omitempty"`     // tool_result
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

// thinkingConfig configures extended thinking for the API request.
// Use BudgetTokens > 0 for a fixed budget, or 0 for adaptive (API decides).
type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
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
	Type    string  `json:"type"`
	Content *string `json:"content"`
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
	ContextManagement *contextManagementConfig `json:"context_management,omitempty"`
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
		tools = DefaultTools()
		if len(opts.ExtraTools) > 0 {
			tools = append(tools, opts.ExtraTools...)
		}
	}

	// Build initial messages from history + new prompt
	messages := make([]agenticMessage, 0, len(c.History)+1)
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

	for turn := 0; turn < opts.MaxTurns; turn++ {
		// Send request (streaming)
		resp, err := c.sendAgenticTurn(ctx, messages, tools, opts)
		if err != nil {
			return nil, fmt.Errorf("turn %d: %w", turn+1, err)
		}

		result.InputTokens += resp.inputTokens
		result.OutputTokens += resp.outputTokens
		result.CacheCreationInputTokens += resp.cacheCreationInputTokens
		result.CacheReadInputTokens += resp.cacheReadInputTokens
		result.StopReason = resp.stopReason
		if resp.model != "" {
			result.Model = resp.model
		}

		// Handle compaction: if the API compacted context, replace old messages
		// with the compaction block for subsequent turns.
		if resp.compaction != nil {
			result.Compacted = true
			// Build the compaction block for round-tripping
			var compactBlock compactionBlockJSON
			compactBlock.Type = "compaction"
			compactBlock.Content = resp.compaction.content

			if resp.compaction.content != nil {
				log.Printf("[anthropicclient] context compacted on turn %d, summary_len=%d", turn+1, len(*resp.compaction.content))
				if opts.OnCompaction != nil {
					opts.OnCompaction(*resp.compaction.content)
				}
			} else {
				log.Printf("[anthropicclient] compaction failed on turn %d (null content), round-tripping as no-op", turn+1)
			}

			// Replace all prior messages with the compaction block.
			// The compaction summary covers all messages accumulated so far.
			compactionMsg := agenticMessage{
				Role:    "user",
				Content: []compactionBlockJSON{compactBlock},
			}
			messages = []agenticMessage{compactionMsg}
		}

		// Ensure contentBlocks is never nil (nil slice marshals as JSON null,
		// but the API requires an array for assistant message content).
		if resp.contentBlocks == nil {
			resp.contentBlocks = []agenticBlock{}
		}

		// Add assistant response to messages
		messages = append(messages, agenticMessage{
			Role:    "assistant",
			Content: resp.contentBlocks,
		})

		// Collect text blocks
		for _, block := range resp.contentBlocks {
			if block.Type == "text" && block.Text != "" {
				allText.WriteString(block.Text)
			}
		}

		// If not a tool use stop, we're done
		if resp.stopReason != "tool_use" {
			// If max_tokens was hit, log a warning — the response may be truncated
			if resp.stopReason == "max_tokens" {
				log.Printf("[anthropicclient] turn %d hit max_tokens limit, response may be truncated", turn+1)
			}
			break
		}

		// Execute tools and collect results (initialize as empty slice, not nil,
		// since nil marshals as JSON null and the API requires an array).
		toolUseBlocks := make([]agenticBlock, 0, len(resp.contentBlocks))
		for _, block := range resp.contentBlocks {
			if block.Type != "tool_use" {
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
				Content:   exec.output,
				IsError:   exec.isError,
			})
		}

		// Safety: if stop_reason was tool_use but we found no tool_use blocks,
		// break to avoid an infinite loop sending empty tool results.
		if len(toolResults) == 0 {
			log.Printf("[anthropicclient] stop_reason=tool_use but no tool_use blocks found, breaking")
			break
		}

		// Add tool results as user message
		messages = append(messages, agenticMessage{
			Role:    "user",
			Content: toolResults,
		})
	}

	result.Text = allText.String()

	// Update client history with the final state
	c.History = append(c.History, Message{Role: "user", Content: prompt})
	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})

	return result, nil
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

func runAnthropicToolUse(ctx context.Context, opts *AgenticOptions, name string, input json.RawMessage) (string, bool) {
	log.Printf("[anthropicclient] executing tool %s", name)
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
	contentBlocks            []agenticBlock
	stopReason               string
	model                    string
	inputTokens              int
	outputTokens             int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
	// compaction holds the compaction summary if the API compacted context.
	// nil = no compaction, non-nil with nil *string = failed compaction.
	compaction *compactionResult
}

// compactionResult holds the result of a server-side compaction.
type compactionResult struct {
	// content is the compaction summary. nil means compaction failed.
	content *string
}

// ContextManagementBetaHeader is the beta feature flag for context management.
const ContextManagementBetaHeader = "context-management-2025-06-27"

// sendAgenticTurn sends a single streaming request and returns parsed content blocks.
func (c *Client) sendAgenticTurn(ctx context.Context, messages []agenticMessage, tools []ToolDefinition, opts *AgenticOptions) (*turnResult, error) {
	// Build system prompt as content blocks with cache_control for prompt caching.
	// OAuth tokens require a billing attribution block as the first system entry;
	// without it the API returns 400 "Error".
	var sysBlocks []systemBlock
	if c.auth.APIKey == "" && c.auth.Token != "" {
		sysBlocks = append(sysBlocks, systemBlock{
			Type: "text",
			Text: "x-anthropic-billing-header: cc_version=2.1.78; cc_entrypoint=cli; cch=00000;",
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
	if opts.EnableThinking {
		if opts.BudgetTokens > 0 {
			// Fixed budget: use "enabled" with explicit budget_tokens
			req.Thinking = &thinkingConfig{
				Type:         "enabled",
				BudgetTokens: opts.BudgetTokens,
			}
		} else if strings.Contains(opts.Model, "opus") {
			// Adaptive: API decides thinking budget dynamically (only supported on Opus 4.6+)
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

	// Add context management config to reduce context size.
	// clear_tool_uses: strip tool results from older messages when token threshold exceeded.
	// clear_thinking: strip thinking blocks from older messages (only when thinking is enabled).
	if opts.AutoCompaction {
		threshold := opts.CompactionTokenThreshold
		if threshold == 0 {
			threshold = DefaultCompactionThreshold
		}
		var edits []contextManagementEdit
		// clear_thinking requires thinking to be enabled; only include it when
		// the request actually has a thinking config. Without this guard the API
		// returns 400 "clear_thinking strategy requires thinking to be enabled".
		if req.Thinking != nil {
			edits = append(edits, contextManagementEdit{
				// clear_thinking must be first when provided (API requirement)
				Type: "clear_thinking_20251015",
			})
		}
		edits = append(edits, contextManagementEdit{
			Type: "clear_tool_uses_20250919",
			Trigger: &inputTokensTrigger{
				Type:  "input_tokens",
				Value: threshold,
			},
		})
		req.ContextManagement = &contextManagementConfig{
			Edits: edits,
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
		betaHeaders = append(betaHeaders, ContextManagementBetaHeader)
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
		}
		return httpReq, nil
	}

	resp, err := doWithRetry(ctx, c.httpClient, buildReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// TEMP: dump rate limit headers
	for k, v := range resp.Header {
		kl := strings.ToLower(k)
		if strings.Contains(kl, "ratelimit") || strings.Contains(kl, "rate-limit") || strings.Contains(kl, "retry") || strings.Contains(kl, "x-anthropic") {
			log.Printf("[anthropic-headers] %s: %v", k, v)
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return c.parseAgenticStream(resp.Body, opts.OnText, opts.OnThinking)
}

// parseAgenticStream parses a streaming response, handling text, tool_use, and compaction blocks.
func (c *Client) parseAgenticStream(body io.Reader, onText func(string), onThinking func(string)) (*turnResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	result := &turnResult{
		contentBlocks: make([]agenticBlock, 0), // must be non-nil: nil marshals as JSON null
	}

	// Track content blocks being built
	type blockState struct {
		typ           string
		text          strings.Builder // for text blocks
		thinking      strings.Builder // for thinking blocks
		signature     string          // for thinking blocks (required to echo back)
		id            string          // for tool_use
		name          string          // for tool_use
		inputJSON     strings.Builder // for tool_use (accumulated partial JSON)
		inToolCallTag bool            // true when inside <tool_call>...</tool_call> in text
		tagBuf        strings.Builder // buffer for detecting partial <tool_call> tags
		compaction    strings.Builder // for compaction blocks
	}
	blocks := make(map[int]*blockState)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			ContentBlock json.RawMessage `json:"content_block,omitempty"`
			Delta        json.RawMessage `json:"delta,omitempty"`
			Message      json.RawMessage `json:"message,omitempty"`
			Usage        json.RawMessage `json:"usage,omitempty"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				var msg struct {
					Model string `json:"model"`
					Usage Usage  `json:"usage"`
				}
				if err := json.Unmarshal(event.Message, &msg); err == nil {
					result.model = msg.Model
					result.inputTokens = msg.Usage.InputTokens
					result.cacheCreationInputTokens = msg.Usage.CacheCreationInputTokens + msg.Usage.CacheCreation.InputTokens
					result.cacheReadInputTokens = msg.Usage.CacheReadInputTokens + msg.Usage.CacheRead.InputTokens
				}
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				var cb struct {
					Type    string  `json:"type"`
					ID      string  `json:"id,omitempty"`
					Name    string  `json:"name,omitempty"`
					Content *string `json:"content,omitempty"` // compaction blocks may have initial content
				}
				if err := json.Unmarshal(event.ContentBlock, &cb); err == nil {
					bs := &blockState{
						typ:  cb.Type,
						id:   cb.ID,
						name: cb.Name,
					}
					// Compaction blocks may have content in the start event
					if cb.Type == "compaction" && cb.Content != nil {
						bs.compaction.WriteString(*cb.Content)
					}
					blocks[event.Index] = bs
				}
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			bs := blocks[event.Index]
			if bs == nil {
				continue
			}

			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				Thinking    string `json:"thinking,omitempty"`
				Signature   string `json:"signature,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
				Content     string `json:"content,omitempty"` // for compaction_delta
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
			}

		case "content_block_stop":
			bs := blocks[event.Index]
			if bs == nil {
				continue
			}

			switch bs.typ {
			case "thinking":
				// Thinking blocks are passed through so they can be echoed
				// back to the API in multi-turn conversations.
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type:      "thinking",
					Thinking:  bs.thinking.String(),
					Signature: bs.signature,
				})
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
				result.contentBlocks = append(result.contentBlocks, agenticBlock{
					Type:  "tool_use",
					ID:    bs.id,
					Name:  bs.name,
					Input: json.RawMessage(bs.inputJSON.String()),
				})
			case "compaction":
				summary := bs.compaction.String()
				if summary != "" {
					result.compaction = &compactionResult{content: &summary}
				} else {
					// Failed compaction — content is null
					result.compaction = &compactionResult{content: nil}
				}
				// Compaction blocks are NOT added to contentBlocks — they are
				// tracked separately and round-tripped as user message content.
			}

		case "message_delta":
			if event.Delta != nil {
				var delta struct {
					StopReason string `json:"stop_reason"`
				}
				if err := json.Unmarshal(event.Delta, &delta); err == nil {
					result.stopReason = delta.StopReason
				}
			}
			if event.Usage != nil {
				var usage struct {
					OutputTokens int `json:"output_tokens"`
				}
				if err := json.Unmarshal(event.Usage, &usage); err == nil {
					result.outputTokens = usage.OutputTokens
				}
			}
		}
	}

	return result, scanner.Err()
}

// reToolCallBlock matches <tool_call>...</tool_call> XML blocks that some models
// emit in text output alongside proper API tool use.
var reToolCallBlock = regexp.MustCompile(`(?s)<tool_call>.*?</tool_call>\s*`)

// stripToolCallTags removes <tool_call>...</tool_call> XML blocks from text.
func stripToolCallTags(s string) string {
	return strings.TrimSpace(reToolCallBlock.ReplaceAllString(s, ""))
}
