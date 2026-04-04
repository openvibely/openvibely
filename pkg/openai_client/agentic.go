package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
)

// DefaultCompactionThreshold is the default approximate token count that
// triggers Codex-style history compaction for OpenAI agentic turns.
const DefaultCompactionThreshold = 200000

const (
	openAICompactionInstructions = `You are compacting a coding-agent conversation so it can continue in a fresh context window.
Produce a concise working summary that preserves the current objective, relevant constraints, important tool calls and outputs, code changes, blockers, and the next useful step.
Preserve completed one-time setup actions (for example required project-guidance reads) as already done, and continue from the latest in-progress implementation state.
Do not restart the task from scratch.
Keep the summary actionable and specific. Omit chit-chat and duplication.
Return only the summary text.`
	openAICompactionTranscriptLimit = 200000
	openAICompactionTranscriptGap   = "\n\n[Middle conversation content omitted before compaction]\n\n"
	openAIEffectiveContextPercent   = 95
	// Mirrors Codex truncation policy defaults in models_cache.json.
	openAIToolOutputTokenLimitDefault = 10000
)

// AgenticOptions configures an agentic send with tool use.
type AgenticOptions struct {
	Model           string
	MaxOutputTokens int
	System          string
	// CompactionPrompt overrides the instruction text used for /responses/compact.
	// When empty, openAICompactionInstructions is used.
	CompactionPrompt string
	WorkDir          string // working directory for tool execution
	MaxTurns         int    // max agentic loop iterations (0 means no limit)
	DisableTools     bool   // when true, no tools are sent (chat orchestrator mode)
	ReasoningEffort  string
	ReasoningSummary string // reasoning summary mode (e.g. auto, concise, detailed, none)
	// AutoCompaction enables client-side Codex-style history compaction for
	// OpenAI Responses API turns.
	AutoCompaction bool
	// CompactionTokenThreshold is the approximate token count that triggers
	// compaction. Defaults to DefaultCompactionThreshold when zero.
	CompactionTokenThreshold int
	// ToolOutputTokenLimit is the approximate maximum tool output token budget
	// to round-trip back to the model in function_call_output items.
	// When zero, openAIToolOutputTokenLimitDefault is used.
	ToolOutputTokenLimit int

	// Attachments are files to include with the initial message.
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
	OnThinking   func(text string)                              // called for reasoning/summary deltas
	OnToolUse    func(name string, input json.RawMessage)       // called when a tool is invoked
	OnToolResult func(name string, output string, isError bool) // called when a tool completes
	OnCompaction func(summary string)                           // called when history is compacted
}

// AgenticResponse is the result of an agentic send.
type AgenticResponse struct {
	Text              string // final text output (all turns concatenated)
	Model             string
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	ReasoningTokens   int
	StopReason        string
	ToolCalls         []ToolCall // log of all tool calls made
	Compacted         bool       // true if history was compacted during this call
}

// agenticInputItem represents an item in the Responses API input array.
type agenticInputItem = map[string]any

// toolCallInfo tracks a pending tool call from the model's response.
type toolCallInfo struct {
	CallID    string
	Name      string
	Arguments string
}

// SendAgentic sends a message with tool use enabled, executing an agentic loop
// until the model finishes (no more tool calls) or MaxTurns is reached.
// Uses the OpenAI Responses API with function calling.
func (c *Client) SendAgentic(ctx context.Context, prompt string, opts *AgenticOptions) (*AgenticResponse, error) {
	if opts == nil {
		opts = &AgenticOptions{}
	}
	if opts.Model == "" {
		opts.Model = DefaultModel
	}
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = 16384
	}
	// MaxTurns=0 means no limit (matches Anthropic/OpenVibely behavior).
	if opts.MaxTurns == 0 {
		opts.MaxTurns = math.MaxInt32
	}
	if opts.WorkDir == "" {
		opts.WorkDir = "."
	}

	isChatGPTOAuth := strings.TrimSpace(c.auth.APIKey) == ""

	if err := c.EnsureValidToken(); err != nil {
		return nil, err
	}

	var tools []ToolDefinition
	if !opts.DisableTools {
		tools = DefaultTools()
		if len(opts.ExtraTools) > 0 {
			tools = append(tools, opts.ExtraTools...)
		}
	}

	// Build initial input items from prior history.
	inputItems := make([]any, 0, len(c.History)+1)
	for _, msg := range c.History {
		inputItems = append(inputItems, agenticInputItem{
			"type":    "message",
			"role":    roleForMessage(msg.Role),
			"content": msg.Content,
		})
	}

	result := &AgenticResponse{Model: opts.Model}
	compactionThreshold := normalizedCompactionThresholdForModel(opts.CompactionTokenThreshold, opts.Model)
	tokenLedger := &agenticSessionTokenLedger{}
	toolOutputTokenLimit := normalizedToolOutputTokenLimit(opts.ToolOutputTokenLimit)
	compactIfNeeded := func(items []any, sessionTokenEstimate int, force bool) ([]any, error) {
		if !opts.AutoCompaction {
			return items, nil
		}
		transcriptEstimate := estimateInputItemsTokens(items)
		compactByTranscript := transcriptEstimate >= compactionThreshold
		compactBySession := sessionTokenEstimate >= compactionThreshold
		if !force && !compactByTranscript && !compactBySession {
			return items, nil
		}
		log.Printf("[openai-client] compacting context force=%v transcript_tokens=%d session_tokens=%d threshold=%d items=%d",
			force, transcriptEstimate, sessionTokenEstimate, compactionThreshold, len(items))

		compactedItems, summary, err := c.compactAgenticInputItems(ctx, items, tools, opts, isChatGPTOAuth)
		if err != nil {
			return nil, err
		}

		result.Compacted = true

		if opts.OnCompaction != nil {
			opts.OnCompaction(strings.TrimSpace(summary))
		}
		tokenLedger.reset()
		return compactedItems, nil
	}

	if len(inputItems) > 0 {
		var err error
		inputItems, err = compactIfNeeded(inputItems, 0, false)
		if err != nil {
			return nil, fmt.Errorf("pre-turn compaction: %w", err)
		}
	}

	// Add current prompt with optional attachments
	if len(opts.Attachments) > 0 {
		content := make([]any, 0, 1+len(opts.Attachments))
		if prompt != "" {
			content = append(content, map[string]any{
				"type": "input_text",
				"text": prompt,
			})
		}
		for _, att := range opts.Attachments {
			block, err := att.toInputContent()
			if err != nil {
				return nil, fmt.Errorf("attachment %s: %w", att.FileName, err)
			}
			content = append(content, block)
		}
		inputItems = append(inputItems, agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": content,
		})
	} else {
		inputItems = append(inputItems, agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": prompt,
		})
	}

	var allText strings.Builder

	for turn := 0; turn < opts.MaxTurns; turn++ {
		var turnResult *agenticTurnResult
		overflowRecovered := false
		for {
			var err error
			turnResult, err = c.sendAgenticTurn(ctx, inputItems, tools, opts, isChatGPTOAuth)
			if err == nil {
				break
			}
			if opts.AutoCompaction && !overflowRecovered && isContextLengthExceededError(err) {
				compactedItems, compactErr := compactIfNeeded(inputItems, compactionThreshold, true)
				if compactErr != nil {
					return nil, fmt.Errorf("turn %d overflow recovery compaction: %w", turn+1, compactErr)
				}
				inputItems = compactedItems
				overflowRecovered = true
				continue
			}
			return nil, fmt.Errorf("turn %d: %w", turn+1, err)
		}

		result.InputTokens += turnResult.inputTokens
		result.OutputTokens += turnResult.outputTokens
		result.CachedInputTokens += turnResult.cachedInputTokens
		result.ReasoningTokens += turnResult.reasoningTokens
		result.StopReason = turnResult.stopReason
		if turnResult.model != "" {
			result.Model = turnResult.model
		}
		tokenLedger.observeTurn(turnResult)

		// Collect text from this turn
		allText.WriteString(turnResult.text)

		// Add the response output items to input for multi-turn
		inputItems = append(inputItems, turnResult.outputItems...)

		// If no tool calls, we're done
		if len(turnResult.toolCalls) == 0 {
			break
		}

		// Execute tools and add results
		localItemsAfterResponse := make([]any, 0, len(turnResult.toolCalls))
		for _, tc := range turnResult.toolCalls {
			inputJSON := json.RawMessage(tc.Arguments)
			if opts.OnToolUse != nil {
				opts.OnToolUse(tc.Name, inputJSON)
			}

			log.Printf("[openai-client] executing tool %s", tc.Name)
			output := ""
			isError := false
			var err error
			if opts.ToolFilter != nil && !opts.ToolFilter(tc.Name) {
				isError = true
				output = fmt.Sprintf("tool %s is not allowed by this agent", tc.Name)
			} else if opts.ToolExecutor != nil {
				output, isError, err = opts.ToolExecutor(ctx, tc.Name, inputJSON)
				if err != nil {
					isError = true
					output = err.Error()
				}
			} else {
				output, err = ExecuteTool(ctx, opts.WorkDir, tc.Name, inputJSON)
				if err != nil {
					isError = true
					output = err.Error()
				}
			}

			if opts.OnToolResult != nil {
				opts.OnToolResult(tc.Name, output, isError)
			}

			// Parse input for logging
			var inputMap map[string]interface{}
			json.Unmarshal(inputJSON, &inputMap)

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:   tc.Name,
				Input:  inputMap,
				Output: output,
				Error:  isError,
			})

			// Add tool result to input items
			modelOutput := truncateToolOutputForModelInput(output, toolOutputTokenLimit)
			if len(modelOutput) < len(output) {
				log.Printf("[openai-client] truncated tool output for model input tool=%s call_id=%s original_chars=%d truncated_chars=%d token_limit=%d",
					tc.Name, tc.CallID, len(output), len(modelOutput), toolOutputTokenLimit)
			}
			toolResultItem := agenticInputItem{
				"type":    "function_call_output",
				"call_id": tc.CallID,
				"output":  modelOutput,
			}
			inputItems = append(inputItems, toolResultItem)
			localItemsAfterResponse = append(localItemsAfterResponse, toolResultItem)
		}

		compactedItems, err := compactIfNeeded(inputItems, tokenLedger.projectedTokens(localItemsAfterResponse), false)
		if err != nil {
			return nil, fmt.Errorf("turn %d compaction: %w", turn+1, err)
		}
		inputItems = compactedItems
	}

	result.Text = allText.String()

	// Update client history
	c.History = append(c.History, Message{Role: "user", Content: prompt})
	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})

	return result, nil
}

func shouldAutoCompactInputItems(inputItems []any, threshold int) bool {
	if len(inputItems) == 0 {
		return false
	}
	return estimateInputItemsTokens(inputItems) >= normalizedCompactionThreshold(threshold)
}

func isContextLengthExceededError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrContextLengthExceeded) {
		return true
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "context_length_exceeded") ||
		strings.Contains(msg, "context length") ||
		strings.Contains(msg, "context window") ||
		strings.Contains(msg, "maximum context") ||
		strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "input too long")
}

func estimateInputItemsTokens(inputItems []any) int {
	text := openAIInputItemsTranscript(inputItems)
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func normalizedCompactionThreshold(threshold int) int {
	if threshold <= 0 {
		return DefaultCompactionThreshold
	}
	return threshold
}

func normalizedCompactionThresholdForModel(threshold int, model string) int {
	limit := openAIAutoCompactionTokenLimit(model)
	if threshold <= 0 {
		return limit
	}
	if limit > 0 && threshold > limit {
		return limit
	}
	return threshold
}

func normalizedToolOutputTokenLimit(limit int) int {
	if limit <= 0 {
		return openAIToolOutputTokenLimitDefault
	}
	return limit
}

func truncateToolOutputForModelInput(output string, tokenLimit int) string {
	if output == "" {
		return output
	}

	limit := normalizedToolOutputTokenLimit(tokenLimit)
	if limit <= 0 {
		return output
	}

	maxChars := limit * 4 // approximate token->char conversion
	runes := []rune(output)
	if len(runes) <= maxChars {
		return output
	}

	const truncationNote = "\n\n[Tool output truncated to fit model context; middle content omitted]\n\n"
	noteRunes := []rune(truncationNote)
	if len(noteRunes) >= maxChars {
		return string(runes[:maxChars])
	}

	available := maxChars - len(noteRunes)
	headLen := available / 2
	tailLen := available - headLen
	if headLen <= 0 || tailLen <= 0 {
		return string(runes[:maxChars])
	}

	head := string(runes[:headLen])
	tail := string(runes[len(runes)-tailLen:])
	return head + truncationNote + tail
}

func openAIAutoCompactionTokenLimit(model string) int {
	contextWindow, ok := openAIModelContextWindow(model)
	if !ok || contextWindow <= 0 {
		return DefaultCompactionThreshold
	}
	return (contextWindow * openAIEffectiveContextPercent) / 100
}

func openAIModelContextWindow(model string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.4",
		"gpt-5.3-codex",
		"gpt-5.2-codex",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5-codex",
		"gpt-5-codex-mini":
		// Mirrors Codex model metadata currently shipped in codex-rs/core/models.json.
		return 272000, true
	default:
		return 0, false
	}
}

func (c *Client) compactAgenticInputItems(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions, isChatGPTOAuth bool) ([]any, string, error) {
	if len(inputItems) == 0 {
		return nil, "", fmt.Errorf("cannot compact empty conversation transcript")
	}

	instructions := compactionInstructions(opts)

	trimmedInput := trimCompactionInputItemsToFitContextWindow(inputItems, tools, instructions, opts.Model)
	if len(trimmedInput) == 0 {
		return nil, "", fmt.Errorf("compaction input is empty after trimming")
	}

	payload := map[string]any{
		"model":               opts.Model,
		"input":               trimmedInput,
		"instructions":        instructions,
		"tools":               tools,
		"parallel_tool_calls": len(tools) > 0,
	}

	reasoningPayload := map[string]any{}
	if effort := normalizeReasoningEffort(opts.ReasoningEffort); effort != "" {
		reasoningPayload["effort"] = effort
	}
	if summary := normalizeReasoningSummary(opts.ReasoningSummary); summary != "" {
		reasoningPayload["summary"] = summary
	}
	if len(reasoningPayload) > 0 {
		payload["reasoning"] = reasoningPayload
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal compaction request: %w", err)
	}

	endpoint, err := c.responsesCompactEndpoint(isChatGPTOAuth)
	if err != nil {
		return nil, "", err
	}

	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(httpReq, isChatGPTOAuth)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		return httpReq, nil
	}

	resp, err := doWithRetry(ctx, c.httpClient, buildReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		apiErr := parseAPIError(resp.StatusCode, errBody)
		return nil, "", fmt.Errorf("POST %q (compaction): %w", endpoint, apiErr)
	}

	var compacted struct {
		Output []any `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&compacted); err != nil {
		return nil, "", fmt.Errorf("decode compaction response: %w", err)
	}

	if len(compacted.Output) == 0 {
		return nil, "", fmt.Errorf("compaction returned empty history")
	}

	summary := extractCompactionSummaryFromOutputItems(compacted.Output)
	return append([]any(nil), compacted.Output...), summary, nil
}

func compactionInstructions(opts *AgenticOptions) string {
	if opts != nil {
		if prompt := strings.TrimSpace(opts.CompactionPrompt); prompt != "" {
			return prompt
		}
	}
	return openAICompactionInstructions
}

func trimCompactionInputItemsToFitContextWindow(inputItems []any, tools []ToolDefinition, instructions, model string) []any {
	contextWindow, ok := openAIModelContextWindow(model)
	if !ok || contextWindow <= 0 || len(inputItems) == 0 {
		return append([]any(nil), inputItems...)
	}

	trimmed := append([]any(nil), inputItems...)
	for estimateCompactionRequestTokens(trimmed, tools, instructions) > contextWindow {
		objectiveIndex := compactionObjectiveIndex(trimmed)
		recentIndex := compactionRecentContextIndex(trimmed)
		trimIndex := nextCompactionTrimIndex(trimmed, objectiveIndex, recentIndex)
		if trimIndex < 0 {
			break
		}
		trimmed = append(trimmed[:trimIndex], trimmed[trimIndex+1:]...)
		if len(trimmed) == 0 {
			break
		}
	}
	return trimmed
}

func compactionObjectiveIndex(items []any) int {
	firstMessage := -1
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
		if itemType != "message" {
			continue
		}
		if firstMessage < 0 {
			firstMessage = i
		}
		role := strings.ToLower(strings.TrimSpace(stringFromAny(item["role"])))
		if role == "user" {
			return i
		}
	}
	if firstMessage >= 0 {
		return firstMessage
	}
	return 0
}

func compactionRecentContextIndex(items []any) int {
	for i := len(items) - 1; i >= 0; i-- {
		item, ok := items[i].(map[string]any)
		if !ok {
			continue
		}
		if !isCodexGeneratedInputItem(item) {
			return i
		}
	}
	if len(items) == 0 {
		return -1
	}
	return len(items) - 1
}

func nextCompactionTrimIndex(items []any, protectedIndexes ...int) int {
	if len(items) == 0 {
		return -1
	}

	isProtected := func(index int) bool {
		for _, protected := range protectedIndexes {
			if protected == index {
				return true
			}
		}
		return false
	}

	// First pass: trim oldest codex-generated/tool-heavy items.
	for i, raw := range items {
		if isProtected(i) {
			continue
		}
		item, ok := raw.(map[string]any)
		if !ok {
			return i
		}
		if isCodexGeneratedInputItem(item) {
			return i
		}
	}

	// Fallback: trim oldest non-protected item.
	for i := range items {
		if !isProtected(i) {
			return i
		}
	}
	return -1
}

func estimateCompactionRequestTokens(inputItems []any, tools []ToolDefinition, instructions string) int {
	total := estimateInputItemsTokens(inputItems)
	if instructions != "" {
		total += (len(instructions) + 3) / 4
	}
	if len(tools) > 0 {
		if encoded, err := json.Marshal(tools); err == nil {
			total += (len(encoded) + 3) / 4
		}
	}
	return total
}

func isCodexGeneratedInputItem(item map[string]any) bool {
	itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
	switch itemType {
	case "function_call",
		"function_call_output",
		"custom_tool_call",
		"custom_tool_call_output",
		"tool_search_call",
		"tool_search_output",
		"web_search_call",
		"image_generation_call":
		return true
	case "message":
		return strings.EqualFold(strings.TrimSpace(stringFromAny(item["role"])), "developer")
	default:
		return false
	}
}

func extractCompactionSummaryFromOutputItems(items []any) string {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
		if itemType != "compaction" {
			continue
		}
		content := strings.TrimSpace(firstNonEmpty(
			stringFromAny(item["encrypted_content"]),
			stringFromAny(item["content"]),
			stringFromAny(item["summary"]),
		))
		if content != "" {
			return content
		}
	}

	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
		switch itemType {
		case "message":
			if strings.EqualFold(strings.TrimSpace(stringFromAny(item["role"])), "user") {
				content := strings.TrimSpace(openAIInputItemContentText(item["content"]))
				if content != "" {
					return content
				}
			}
		}
	}
	return ""
}

func openAICompactionOutputTokens(maxOutputTokens int) int {
	if maxOutputTokens <= 0 || maxOutputTokens > 4096 {
		maxOutputTokens = 4096
	}
	if maxOutputTokens < 512 {
		return 512
	}
	return maxOutputTokens
}

func clampCompactionTranscript(transcript string) string {
	runes := []rune(transcript)
	if len(runes) <= openAICompactionTranscriptLimit {
		return transcript
	}

	gapRunes := []rune(openAICompactionTranscriptGap)
	if len(gapRunes) >= openAICompactionTranscriptLimit {
		return string(runes[len(runes)-openAICompactionTranscriptLimit:])
	}

	headLen := openAICompactionTranscriptLimit / 4
	tailLen := openAICompactionTranscriptLimit - headLen - len(gapRunes)
	if tailLen < headLen {
		tailLen = openAICompactionTranscriptLimit / 2
		headLen = openAICompactionTranscriptLimit - tailLen - len(gapRunes)
	}
	if headLen <= 0 || tailLen <= 0 {
		return string(runes[len(runes)-openAICompactionTranscriptLimit:])
	}

	head := string(runes[:headLen])
	tail := string(runes[len(runes)-tailLen:])
	return head + openAICompactionTranscriptGap + tail
}

func openAIInputItemsTranscript(inputItems []any) string {
	var sb strings.Builder
	for _, raw := range inputItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}

		switch strings.TrimSpace(stringFromAny(item["type"])) {
		case "message":
			role := firstNonEmpty(stringFromAny(item["role"]), "user")
			text := openAIInputItemContentText(item["content"])
			if strings.TrimSpace(text) == "" {
				continue
			}
			fmt.Fprintf(&sb, "%s:\n%s\n\n", strings.ToUpper(role), strings.TrimSpace(text))
		case "function_call":
			name := firstNonEmpty(stringFromAny(item["name"]), "tool")
			args := strings.TrimSpace(stringFromAny(item["arguments"]))
			if args == "" {
				args = "{}"
			}
			fmt.Fprintf(&sb, "TOOL_CALL %s:\n%s\n\n", name, args)
		case "function_call_output":
			output := openAIExtractToolOutputText(item["output"])
			if output == "" {
				output = "(no output)"
			}
			fmt.Fprintf(&sb, "TOOL_RESULT %s:\n%s\n\n", firstNonEmpty(stringFromAny(item["call_id"]), "call"), output)
		default:
			blob, err := json.Marshal(item)
			if err != nil {
				continue
			}
			fmt.Fprintf(&sb, "EVENT:\n%s\n\n", string(blob))
		}
	}
	return strings.TrimSpace(sb.String())
}

func openAIInputItemContentText(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringFromAny(block["type"])) {
			case "input_text", "output_text":
				if text := strings.TrimSpace(firstNonEmpty(stringFromAny(block["text"]), stringFromAny(block["content"]))); text != "" {
					parts = append(parts, text)
				}
			case "input_image":
				parts = append(parts, "[image attachment]")
			default:
				if text := strings.TrimSpace(firstNonEmpty(stringFromAny(block["text"]), stringFromAny(block["content"]))); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

// agenticTurnResult holds the parsed result of a single API turn.
type agenticTurnResult struct {
	text              string
	outputItems       []any
	toolCalls         []toolCallInfo
	stopReason        string
	model             string
	inputTokens       int
	outputTokens      int
	cachedInputTokens int
	reasoningTokens   int
}

// agenticSessionTokenLedger keeps a running estimate of immediate next-turn
// token pressure. It tracks the most recent observed turn footprint and adds
// locally appended items after that turn (for example tool outputs).
type agenticSessionTokenLedger struct {
	lastObservedTotalTokens int
	hasObservedTotalTokens  bool
}

func (l *agenticSessionTokenLedger) observeTurn(result *agenticTurnResult) {
	if result == nil {
		return
	}
	observedTotal := result.inputTokens + result.outputTokens
	if observedTotal <= 0 {
		return
	}
	l.lastObservedTotalTokens = observedTotal
	l.hasObservedTotalTokens = true
}

func (l *agenticSessionTokenLedger) projectedTokens(localItemsAfterResponse []any) int {
	localEstimate := estimateInputItemsTokens(localItemsAfterResponse)
	if !l.hasObservedTotalTokens {
		return localEstimate
	}
	return l.lastObservedTotalTokens + localEstimate
}

func (l *agenticSessionTokenLedger) reset() {
	l.lastObservedTotalTokens = 0
	l.hasObservedTotalTokens = false
}

// sendAgenticTurn sends a single request and returns parsed results.
func (c *Client) sendAgenticTurn(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions, isChatGPTOAuth bool) (*agenticTurnResult, error) {
	payload := map[string]any{
		"model":  opts.Model,
		"input":  inputItems,
		"stream": true,
	}

	if !isChatGPTOAuth {
		payload["max_output_tokens"] = opts.MaxOutputTokens
	}

	if isChatGPTOAuth {
		payload["store"] = false
	}

	system := strings.TrimSpace(opts.System)
	if system == "" && isChatGPTOAuth {
		system = "You are a helpful assistant."
	}
	if system != "" {
		payload["instructions"] = system
	}

	reasoningPayload := map[string]any{}
	if effort := normalizeReasoningEffort(opts.ReasoningEffort); effort != "" {
		reasoningPayload["effort"] = effort
	}
	if summary := normalizeReasoningSummary(opts.ReasoningSummary); summary != "" {
		reasoningPayload["summary"] = summary
	}
	if len(reasoningPayload) > 0 {
		payload["reasoning"] = reasoningPayload
	}

	if len(tools) > 0 {
		payload["tools"] = tools
	}

	// Enable automatic context truncation so the model can keep working when
	// the conversation grows beyond the context window. Without this (default
	// is "disabled"), the model fills context with tool results (file reads)
	// and then silently stops making tool calls — appearing to "complete"
	// without doing any work.
	// Only supported on the direct API (api.openai.com), not the ChatGPT OAuth endpoint.
	if !isChatGPTOAuth {
		payload["truncation"] = "auto"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint, err := c.responsesEndpoint(isChatGPTOAuth)
	if err != nil {
		return nil, err
	}

	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(httpReq, isChatGPTOAuth)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
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
		if strings.Contains(kl, "ratelimit") || strings.Contains(kl, "rate-limit") || strings.Contains(kl, "retry") || strings.Contains(kl, "x-openai") || strings.Contains(kl, "x-request") {
			log.Printf("[openai-headers] %s: %v", k, v)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		trimmed := strings.TrimSpace(string(errBody))
		if trimmed == "" {
			return nil, fmt.Errorf("POST %q: %d %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		return nil, fmt.Errorf("POST %q: %d %s %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode), trimmed)
	}

	return c.parseAgenticStream(resp.Body, opts.OnText, opts.OnThinking)
}

// parseAgenticStream parses a streaming response, handling text deltas and function calls.
func (c *Client) parseAgenticStream(body io.Reader, onText func(string), onThinking func(string)) (*agenticTurnResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	result := &agenticTurnResult{}
	var textBuilder strings.Builder
	var completed map[string]any
	sawThinking := false
	currentEventType := ""

	// Track function calls being built incrementally
	type fnCallState struct {
		callID string
		name   string
		args   strings.Builder
	}
	fnCalls := make(map[int]*fnCallState)

	sanitizer := newOpenAIStreamSanitizer(func(text string) {
		textBuilder.WriteString(text)
		if onText != nil {
			onText(text)
		}
	})

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			currentEventType = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		typ := strings.TrimSpace(firstNonEmpty(stringFromAny(ev["type"]), currentEventType))
		switch typ {
		case "response.output_text.delta":
			delta := stringFromAny(ev["delta"])
			if delta != "" {
				sanitizer.Write(delta)
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if onThinking != nil {
				delta := stringFromAny(ev["delta"])
				if delta != "" {
					onThinking(delta)
					sawThinking = true
				}
			}
		case "response.reasoning_summary_part.added":
			if onThinking != nil {
				onThinking("\n\n")
				sawThinking = true
			}

		case "response.function_call_arguments.delta":
			// Incremental function call argument building
			idx := intFromAny(ev["output_index"])
			delta := stringFromAny(ev["delta"])
			if fc := fnCalls[idx]; fc != nil && delta != "" {
				fc.args.WriteString(delta)
			}

		case "response.output_item.added":
			// A new output item is being streamed
			if item, ok := ev["item"].(map[string]any); ok {
				itemType := stringFromAny(item["type"])
				idx := intFromAny(ev["output_index"])
				if itemType == "function_call" {
					fnCalls[idx] = &fnCallState{
						callID: stringFromAny(item["call_id"]),
						name:   stringFromAny(item["name"]),
					}
				}
			}

		case "response.output_item.done":
			if item, ok := ev["item"].(map[string]any); ok {
				itemType := stringFromAny(item["type"])
				switch itemType {
				case "function_call":
					callID := stringFromAny(item["call_id"])
					name := stringFromAny(item["name"])
					args := stringFromAny(item["arguments"])
					if name != "" {
						result.toolCalls = append(result.toolCalls, toolCallInfo{
							CallID:    callID,
							Name:      name,
							Arguments: args,
						})
						// Add to output items for round-tripping
						result.outputItems = append(result.outputItems, item)
					}
				case "message":
					// Text message items are handled via deltas
					result.outputItems = append(result.outputItems, item)
				case "reasoning":
					if onThinking != nil {
						if text := openAIReasoningTextFromItem(item); text != "" {
							onThinking(text)
							sawThinking = true
						}
					}
				}
			}

		case "response.error", "error":
			if msg := extractErrorMessage(ev); msg != "" {
				return nil, fmt.Errorf("stream error: %s", msg)
			}
			return nil, fmt.Errorf("stream error: unknown")

		case "response.completed":
			if m, ok := ev["response"].(map[string]any); ok {
				completed = m
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	sanitizer.Flush()

	result.text = textBuilder.String()
	if completed != nil {
		if result.text == "" {
			extracted := extractOutputText(completed)
			result.text = extracted
			// Some Responses turns only include text in response.completed.output
			// without prior output_text deltas. Emit the recovered text so
			// streaming UIs receive the final assistant content.
			if extracted != "" && onText != nil {
				onText(extracted)
			}
		}
		if !sawThinking && onThinking != nil {
			// Some responses omit reasoning deltas and only include reasoning in
			// response.completed.output. Surface that as thinking fallback.
			if fallback := extractReasoningText(completed); fallback != "" {
				onThinking(fallback)
				sawThinking = true
			}
		}
		result.model = stringFromAny(completed["model"])
		result.inputTokens, result.outputTokens = extractUsage(completed)
		result.cachedInputTokens = extractCachedInputTokens(completed)
		result.reasoningTokens = extractReasoningTokens(completed)
		result.stopReason = responseStopReasonMap(completed)
	}
	if result.stopReason == "" {
		result.stopReason = "completed"
	}

	return result, nil
}
