package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
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
	openAICompactionTranscriptLimit                    = 200000
	openAICompactionTranscriptGap                      = "\n\n[Middle conversation content omitted before compaction]\n\n"
	openAIEffectiveContextPercent                      = 95
	openAIRemoteCompactionV2RetainedMessageTokenBudget = 64000
	openAIApproxBytesPerToken                          = 4
	openAIResizedImageBytesEstimate                    = 7373
	openAIOriginalImagePatchSize                       = 32
	openAIOriginalImageMaxPatches                      = 10000
	// Mirrors Codex truncation policy defaults in models_cache.json.
	openAIToolOutputTokenLimitDefault = 10000
	// OpenAI native web search tool type for the Responses API.
	openAIWebSearchToolType = "web_search"
)

// AgenticOptions configures an agentic send with tool use.
type AgenticOptions struct {
	Model           string
	MaxOutputTokens int
	System          string
	// CompactionPrompt overrides the instruction text used for API-key /responses/compact.
	// ChatGPT OAuth compaction mirrors Codex v2 and uses the base system instructions.
	// When empty, openAICompactionInstructions is used.
	CompactionPrompt string
	WorkDir          string // working directory for tool execution
	MaxTurns         int    // max agentic loop iterations (0 means no limit)
	DisableTools     bool   // when true, no tools are sent (chat orchestrator mode)
	// SkipDefaultTools suppresses built-in local tools while still allowing
	// ExtraTools (for example request-scoped runtime tools) to be sent.
	SkipDefaultTools bool
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

	// WebSearchEnabled adds the provider-native web search tool to the request
	// when the model supports it. The search is executed server-side by OpenAI;
	// no local tool execution is needed.
	WebSearchEnabled bool

	// Callbacks for real-time output
	OnText       func(text string)                              // called for each text delta
	OnThinking   func(text string)                              // called for reasoning/summary deltas
	OnToolUse    func(name string, input json.RawMessage)       // called when a tool is invoked
	OnToolResult func(name string, output string, isError bool) // called when a tool completes
	// OnToolBoundarySteering is called after local tool results are appended and before the next model request.
	OnToolBoundarySteering func(ctx context.Context) (string, error)
	OnCompaction           func(summary string) // called when history is compacted
}

// AgenticResponse is the result of an agentic send.
type AgenticResponse struct {
	Text              string // final text output (all turns concatenated)
	Model             string
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
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

	if err := c.ensureValidToken(); err != nil {
		return nil, err
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
		applog.Infof("[openai-client] compacting context force=%v transcript_tokens=%d session_tokens=%d threshold=%d items=%d",
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
		turnResult, err := httpretry.DoStreamTurn(ctx, httpretry.StreamTurnPolicy{
			RetryableError:                       isRetryableResponsesTransportError,
			RetryConnectionFailuresWithoutBudget: true,
			Recover: func(err error) (bool, error) {
				if !opts.AutoCompaction || overflowRecovered || !isContextLengthExceededError(err) {
					return false, nil
				}
				compactedItems, compactErr := compactIfNeeded(inputItems, compactionThreshold, true)
				if compactErr != nil {
					return false, fmt.Errorf("turn %d overflow recovery compaction: %w", turn+1, compactErr)
				}
				inputItems = compactedItems
				overflowRecovered = true
				return true, nil
			},
			OnRetry: func(event httpretry.RetryEvent) {
				if httpretry.IsConnectionSetupFailure(event.Err) {
					applog.Infof("[openai-client] reconnecting agentic turn %d after connection error in %v: %v", turn+1, event.Delay, event.Err)
					return
				}
				applog.Infof("[openai-client] retrying agentic turn %d after stream/transport error, retry attempt %d/%d in %v: %v",
					turn+1, event.Attempt, event.MaxRetries, event.Delay, event.Err)
			},
		}, func(attemptCtx context.Context) (*agenticTurnResult, error) {
			return c.sendAgenticTurn(attemptCtx, inputItems, tools, opts, isChatGPTOAuth)
		})
		if err != nil {
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

		// Add the response output items to input for multi-turn. Stateless
		// requests use store=false, so reasoning items can only be replayed when
		// they carry their encrypted state rather than just a temporary rs_* ID.
		outputItems := turnResult.outputItems
		if isChatGPTOAuth || isResponsesLiteWebsocketModel(opts.Model) {
			outputItems = statelessOAuthOutputItems(outputItems)
		}
		inputItems = append(inputItems, outputItems...)

		// If no tool calls, we're done
		if len(turnResult.toolCalls) == 0 {
			break
		}

		// Execute tools and add results.
		tasks := make([]openAIToolExecutionTask, 0, len(turnResult.toolCalls))
		for _, tc := range turnResult.toolCalls {
			inputJSON := json.RawMessage(tc.Arguments)
			if opts.OnToolUse != nil {
				opts.OnToolUse(tc.Name, inputJSON)
			}
			var inputMap map[string]interface{}
			_ = json.Unmarshal(inputJSON, &inputMap)
			tasks = append(tasks, openAIToolExecutionTask{
				call:     tc,
				input:    inputJSON,
				inputMap: inputMap,
			})
		}

		executed := executeOpenAIToolTasks(ctx, opts, tasks)
		localItemsAfterResponse := make([]any, 0, len(executed))
		for _, exec := range executed {
			if opts.OnToolResult != nil {
				opts.OnToolResult(exec.call.Name, exec.output, exec.isError)
			}

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:   exec.call.Name,
				Input:  exec.inputMap,
				Output: exec.output,
				Error:  exec.isError,
			})

			modelOutput := truncateToolOutputForModelInput(exec.output, toolOutputTokenLimit)
			if len(modelOutput) < len(exec.output) {
				applog.Infof("[openai-client] truncated tool output for model input tool=%s call_id=%s original_chars=%d truncated_chars=%d token_limit=%d",
					exec.call.Name, exec.call.CallID, len(exec.output), len(modelOutput), toolOutputTokenLimit)
			}
			toolResultItem := agenticInputItem{
				"type":    "function_call_output",
				"call_id": exec.call.CallID,
				"output":  modelOutput,
			}
			inputItems = append(inputItems, toolResultItem)
			localItemsAfterResponse = append(localItemsAfterResponse, toolResultItem)
		}

		if opts.OnToolBoundarySteering != nil {
			steering, err := opts.OnToolBoundarySteering(ctx)
			if err != nil {
				return nil, fmt.Errorf("turn %d tool-boundary steering: %w", turn+1, err)
			}
			if steering = strings.TrimSpace(steering); steering != "" {
				inputItems = append(inputItems, agenticInputItem{
					"type":    "message",
					"role":    "user",
					"content": steering,
				})
			}
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

type openAIToolExecutionTask struct {
	call     toolCallInfo
	input    json.RawMessage
	inputMap map[string]interface{}
}

type openAIToolExecutionResult struct {
	call     toolCallInfo
	inputMap map[string]interface{}
	output   string
	isError  bool
}

func normalizeOpenAIFunctionCallArguments(raw any) string {
	args := strings.TrimSpace(stringFromAny(raw))
	if args == "" {
		return "{}"
	}
	return args
}

func completedOpenAIFunctionCallArguments(raw any, streamedArgs string) string {
	args := strings.TrimSpace(stringFromAny(raw))
	if args != "" {
		return args
	}
	return normalizeOpenAIFunctionCallArguments(streamedArgs)
}

func executeOpenAIToolTasks(ctx context.Context, opts *AgenticOptions, tasks []openAIToolExecutionTask) []openAIToolExecutionResult {
	if len(tasks) == 0 {
		return nil
	}

	results := make([]openAIToolExecutionResult, len(tasks))
	runOne := func(i int) {
		task := tasks[i]
		output, isError := runOpenAIToolTask(ctx, opts, task.call.Name, task.input)
		results[i] = openAIToolExecutionResult{
			call:     task.call,
			inputMap: task.inputMap,
			output:   output,
			isError:  isError,
		}
	}

	if len(tasks) > 1 && allOpenAIToolsReadOnly(tasks) {
		var wg sync.WaitGroup
		wg.Add(len(tasks))
		for i := range tasks {
			go func(idx int) {
				defer wg.Done()
				runOne(idx)
			}(i)
		}
		wg.Wait()
		return results
	}

	for i := range tasks {
		runOne(i)
	}
	return results
}

func runOpenAIToolTask(ctx context.Context, opts *AgenticOptions, name string, input json.RawMessage) (string, bool) {
	applog.Infof("[openai-client] executing tool %s", name)
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

func allOpenAIToolsReadOnly(tasks []openAIToolExecutionTask) bool {
	for _, task := range tasks {
		switch task.call.Name {
		case "read_file", "list_files", "grep_search":
			continue
		default:
			return false
		}
	}
	return true
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
	if len(inputItems) == 0 {
		return 0
	}
	total := 0
	for _, item := range inputItems {
		total += estimateAgenticInputItemTokens(item)
	}
	return total
}

func estimateAgenticInputItemTokens(item any) int {
	bytes := estimateAgenticResponseItemModelVisibleBytes(item)
	if bytes > 0 {
		return approxOpenAITokensFromByteCount(bytes)
	}
	text := openAIInputItemsTranscript([]any{item})
	if text == "" {
		return 0
	}
	return approxOpenAITokenCount(text)
}

func estimateAgenticResponseItemModelVisibleBytes(item any) int {
	if itemMap, ok := item.(map[string]any); ok {
		switch strings.TrimSpace(stringFromAny(itemMap["type"])) {
		case "reasoning", "context_compaction":
			if encryptedContent := stringFromAny(itemMap["encrypted_content"]); encryptedContent != "" {
				return estimateOpenAIReasoningLength(len(encryptedContent))
			}
		case "compaction":
			encryptedContent := stringFromAny(itemMap["encrypted_content"])
			return estimateOpenAIReasoningLength(len(encryptedContent))
		}
	}

	serialized, err := json.Marshal(item)
	if err != nil || len(serialized) == 0 {
		return 0
	}
	raw := len(serialized)
	imagePayloadBytes, imageReplacementBytes := agenticImageDataURLEstimateAdjustment(item)
	audioPayloadBytes, audioReplacementBytes := agenticAudioDataURLEstimateAdjustment(item)
	encryptedPayloadBytes, encryptedReplacementBytes := agenticEncryptedFunctionOutputEstimateAdjustment(item)
	raw = raw - imagePayloadBytes + imageReplacementBytes - audioPayloadBytes + audioReplacementBytes
	raw = raw - encryptedPayloadBytes + encryptedReplacementBytes
	if raw < 0 {
		return 0
	}
	return raw
}

func estimateOpenAIReasoningLength(encodedLen int) int {
	if encodedLen <= 0 {
		return 0
	}
	estimated := encodedLen*3/4 - 650
	if estimated < 0 {
		return 0
	}
	return estimated
}

func estimateOpenAIEncryptedFunctionOutputLength(encodedLen int) int {
	if encodedLen <= 0 {
		return 0
	}
	return (encodedLen*9 + 15) / 16
}

func agenticImageDataURLEstimateAdjustment(item any) (int, int) {
	payloadBytes := 0
	replacementBytes := 0
	forEachAgenticContentBlock(item, func(block map[string]any) {
		if strings.TrimSpace(stringFromAny(block["type"])) != "input_image" {
			return
		}
		imageURL := firstNonEmpty(stringFromAny(block["image_url"]), stringFromAny(block["url"]))
		payload, ok := parseAgenticBase64DataURL(imageURL, "image/")
		if !ok {
			return
		}
		payloadBytes += len(payload)
		if strings.EqualFold(strings.TrimSpace(stringFromAny(block["detail"])), "original") {
			replacementBytes += estimateAgenticOriginalImageBytes(imageURL)
			return
		}
		replacementBytes += openAIResizedImageBytesEstimate
	})
	return payloadBytes, replacementBytes
}

func agenticAudioDataURLEstimateAdjustment(item any) (int, int) {
	payloadBytes := 0
	replacementBytes := 0
	forEachAgenticContentBlock(item, func(block map[string]any) {
		if strings.TrimSpace(stringFromAny(block["type"])) != "input_audio" {
			return
		}
		audioURL := firstNonEmpty(stringFromAny(block["audio_url"]), stringFromAny(block["url"]))
		payload, ok := parseAgenticBase64DataURL(audioURL, "audio/")
		if !ok {
			return
		}
		payloadBytes += len(payload)
		replacementBytes += approxOpenAIBytesForTokens(approxOpenAITokenCount(audioURL))
	})
	return payloadBytes, replacementBytes
}

func agenticEncryptedFunctionOutputEstimateAdjustment(item any) (int, int) {
	payloadBytes := 0
	replacementBytes := 0
	itemMap, ok := item.(map[string]any)
	if !ok {
		return 0, 0
	}
	itemType := strings.ToLower(strings.TrimSpace(stringFromAny(itemMap["type"])))
	switch itemType {
	case "function_call_output", "custom_tool_call_output", "agent_message":
	default:
		return 0, 0
	}
	forEachAgenticContentBlock(itemMap, func(block map[string]any) {
		if strings.TrimSpace(stringFromAny(block["type"])) != "encrypted_content" {
			return
		}
		encryptedContent := stringFromAny(block["encrypted_content"])
		if encryptedContent == "" {
			return
		}
		payloadBytes += len(encryptedContent)
		replacementBytes += estimateOpenAIEncryptedFunctionOutputLength(len(encryptedContent))
	})
	return payloadBytes, replacementBytes
}

func forEachAgenticContentBlock(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			forEachAgenticContentBlock(item, visit)
		}
	case map[string]any:
		visit(typed)
		for _, nested := range typed {
			switch nested.(type) {
			case []any, map[string]any:
				forEachAgenticContentBlock(nested, visit)
			}
		}
	}
}

func parseAgenticBase64DataURL(url string, mediaTypePrefix string) (string, bool) {
	if len(url) < len("data:") || !strings.EqualFold(url[:len("data:")], "data:") {
		return "", false
	}
	commaIndex := strings.IndexByte(url, ',')
	if commaIndex < 0 {
		return "", false
	}
	metadata := url[len("data:"):commaIndex]
	payload := url[commaIndex+1:]
	parts := strings.Split(metadata, ";")
	if len(parts) == 0 || len(parts[0]) < len(mediaTypePrefix) || !strings.EqualFold(parts[0][:len(mediaTypePrefix)], mediaTypePrefix) {
		return "", false
	}
	hasBase64Marker := false
	for _, part := range parts[1:] {
		if strings.EqualFold(part, "base64") {
			hasBase64Marker = true
			break
		}
	}
	if !hasBase64Marker {
		return "", false
	}
	return payload, true
}

func estimateAgenticOriginalImageBytes(imageURL string) int {
	payload, ok := parseAgenticBase64DataURL(imageURL, "image/")
	if !ok {
		return openAIResizedImageBytesEstimate
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return openAIResizedImageBytesEstimate
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return openAIResizedImageBytesEstimate
	}
	patchesWide := (config.Width + openAIOriginalImagePatchSize - 1) / openAIOriginalImagePatchSize
	patchesHigh := (config.Height + openAIOriginalImagePatchSize - 1) / openAIOriginalImagePatchSize
	patchCount := patchesWide * patchesHigh
	if patchCount > openAIOriginalImageMaxPatches {
		patchCount = openAIOriginalImageMaxPatches
	}
	return approxOpenAIBytesForTokens(patchCount)
}

func approxOpenAITokenCount(text string) int {
	return approxOpenAITokensFromByteCount(len(text))
}

func approxOpenAITokensFromByteCount(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + openAIApproxBytesPerToken - 1) / openAIApproxBytesPerToken
}

func approxOpenAIBytesForTokens(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * openAIApproxBytesPerToken
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
	case "gpt-6-astra":
		return 1050000, true
	case "gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna":
		return 272000, true
	case "gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.3-codex",
		"gpt-5.2-codex",
		"gpt-5.1-codex-max",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5-codex",
		"gpt-5-codex-mini":
		// Mirrors Codex model metadata currently shipped in codex-rs/core/models.json.
		return 272000, true
	case "gpt-5.3-codex-spark":
		return 128000, true
	default:
		return 0, false
	}
}

func (c *Client) compactAgenticInputItems(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions, isChatGPTOAuth bool) ([]any, string, error) {
	if len(inputItems) == 0 {
		return nil, "", fmt.Errorf("cannot compact empty conversation transcript")
	}

	instructions := compactionInstructions(opts)
	if isChatGPTOAuth {
		instructions = openAICompactionV2Instructions(opts, isChatGPTOAuth)
	}

	trimmedInput := trimCompactionInputItemsToFitContextWindow(inputItems, tools, instructions, opts.Model)
	if len(trimmedInput) == 0 {
		return nil, "", fmt.Errorf("compaction input is empty after trimming")
	}

	if isChatGPTOAuth {
		return c.compactAgenticInputItemsViaResponsesV2(ctx, trimmedInput, tools, opts)
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
	if isResponsesLiteWebsocketModel(opts.Model) {
		if len(reasoningPayload) == 0 {
			reasoningPayload["effort"] = responsesLiteDefaultReasoningEffort(opts.Model)
		}
		reasoningPayload["context"] = "all_turns"
		payload["reasoning"] = reasoningPayload
		payload["parallel_tool_calls"] = false
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
		if isResponsesLiteWebsocketModel(opts.Model) {
			httpReq.Header.Set("x-openai-internal-codex-responses-lite", "true")
		}
		return httpReq, nil
	}

	resp, err := c.doWithOAuthRecovery(ctx, endpoint, isChatGPTOAuth, buildReq)
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

func (c *Client) compactAgenticInputItemsViaResponsesV2(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions) ([]any, string, error) {
	compactionInput := append([]any(nil), inputItems...)
	compactionInput = append(compactionInput, agenticInputItem{"type": "compaction_trigger"})

	payload := map[string]any{
		"model":   opts.Model,
		"input":   compactionInput,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	system := openAICompactionV2Instructions(opts, true)
	if system != "" {
		payload["instructions"] = system
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["parallel_tool_calls"] = openAIModelSupportsParallelToolCalls(opts.Model)
	}

	reasoningPayload := map[string]any{}
	if effort := normalizeReasoningEffort(opts.ReasoningEffort); effort != "" {
		reasoningPayload["effort"] = effort
	}
	if summary := normalizeReasoningSummary(opts.ReasoningSummary); summary != "" {
		reasoningPayload["summary"] = summary
	}
	if isResponsesLiteWebsocketModel(opts.Model) {
		if len(reasoningPayload) == 0 {
			reasoningPayload["effort"] = responsesLiteDefaultReasoningEffort(opts.Model)
		}
		reasoningPayload["context"] = "all_turns"
	}
	if len(reasoningPayload) > 0 {
		payload["reasoning"] = reasoningPayload
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal compaction response request: %w", err)
	}

	endpoint, err := c.responsesEndpoint(true)
	if err != nil {
		return nil, "", err
	}

	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(httpReq, true)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if isResponsesLiteWebsocketModel(opts.Model) {
			httpReq.Header.Set("x-openai-internal-codex-responses-lite", "true")
		}
		return httpReq, nil
	}

	resp, err := c.doWithOAuthRecovery(ctx, endpoint, true, buildReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		apiErr := parseAPIError(resp.StatusCode, errBody)
		return nil, "", fmt.Errorf("POST %q (compaction response): %w", endpoint, apiErr)
	}

	result, err := c.parseAgenticStream(resp.Body, nil, nil)
	if err != nil {
		return nil, "", fmt.Errorf("parse compaction response: %w", err)
	}
	compactionItems := make([]any, 0, 1)
	for _, raw := range result.outputItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "compaction") {
			compactionItems = append(compactionItems, item)
		}
	}
	if len(compactionItems) != 1 {
		return nil, "", fmt.Errorf("compaction response returned %d compaction items, want 1", len(compactionItems))
	}

	summary := extractCompactionSummaryFromOutputItems(compactionItems)
	return buildAgenticRemoteCompactionV2History(inputItems, compactionItems[0]), summary, nil
}

func buildAgenticRemoteCompactionV2History(inputItems []any, compactionItem any) []any {
	retained := make([]any, 0, len(inputItems)+1)
	remaining := openAIRemoteCompactionV2RetainedMessageTokenBudget

	for i := len(inputItems) - 1; i >= 0 && remaining > 0; i-- {
		item, ok := inputItems[i].(map[string]any)
		if !ok || !isRetainedForOpenAIRemoteCompactionV2(item) {
			continue
		}
		tokens := retainedMessageTokenCountForOpenAIRemoteCompactionV2(item)
		if tokens <= 0 {
			tokens = 1
		}
		if tokens > remaining {
			truncated, ok := truncateRetainedMessageForOpenAIRemoteCompactionV2(item, remaining)
			if !ok {
				continue
			}
			retained = append(retained, truncated)
			break
		}
		retained = append(retained, cloneAgenticMap(item))
		remaining -= tokens
	}

	for i, j := 0, len(retained)-1; i < j; i, j = i+1, j-1 {
		retained[i], retained[j] = retained[j], retained[i]
	}
	retained = append(retained, compactionItem)
	return retained
}

func isRetainedForOpenAIRemoteCompactionV2(item map[string]any) bool {
	itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
	switch strings.ToLower(strings.TrimSpace(stringFromAny(item["role"]))) {
	case "user", "developer", "system":
		return itemType == "message"
	case "assistant":
		if itemType != "message" {
			return false
		}
		text := openAIInputItemContentText(item["content"])
		return !strings.HasPrefix(text, "Message Type: FINAL_ANSWER\n")
	default:
		if itemType != "agent_message" {
			return false
		}
		text := openAIInputItemContentText(item["content"])
		return !strings.HasPrefix(text, "Message Type: FINAL_ANSWER\n") &&
			estimateAgenticInputItemTokens(item) <= openAIToolOutputTokenLimitDefault
	}
}

func retainedMessageTokenCountForOpenAIRemoteCompactionV2(item map[string]any) int {
	if strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "message") {
		if textTokens := messageTextTokenCountForOpenAIRemoteCompactionV2(item); textTokens > 0 {
			return textTokens
		}
	}
	return estimateAgenticInputItemTokens(item)
}

func messageTextTokenCountForOpenAIRemoteCompactionV2(item map[string]any) int {
	switch content := item["content"].(type) {
	case string:
		return approxOpenAITokenCount(content)
	case []any:
		total := 0
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch strings.TrimSpace(stringFromAny(block["type"])) {
			case "input_text", "output_text":
				total += approxOpenAITokenCount(firstNonEmpty(stringFromAny(block["text"]), stringFromAny(block["content"])))
			}
		}
		return total
	default:
		return 0
	}
}

func truncateRetainedMessageForOpenAIRemoteCompactionV2(item map[string]any, tokenBudget int) (map[string]any, bool) {
	if tokenBudget <= 0 {
		return nil, false
	}
	cloned := cloneAgenticMap(item)
	switch content := cloned["content"].(type) {
	case string:
		truncated := truncateTextToOpenAITokenBudget(strings.TrimSpace(content), tokenBudget)
		if truncated == "" {
			return nil, false
		}
		cloned["content"] = truncated
		return cloned, true
	case []any:
		remaining := tokenBudget
		out := make([]any, 0, len(content))
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				out = append(out, raw)
				continue
			}
			block = cloneAgenticMap(block)
			switch strings.TrimSpace(stringFromAny(block["type"])) {
			case "input_text", "output_text":
				text := strings.TrimSpace(firstNonEmpty(stringFromAny(block["text"]), stringFromAny(block["content"])))
				if text == "" || remaining <= 0 {
					continue
				}
				truncated := truncateTextToOpenAITokenBudget(text, remaining)
				if truncated == "" {
					continue
				}
				if _, ok := block["text"]; ok {
					block["text"] = truncated
				} else {
					block["content"] = truncated
				}
				remaining -= approxOpenAITokenCount(truncated)
			}
			out = append(out, block)
		}
		if len(out) == 0 {
			return nil, false
		}
		cloned["content"] = out
		return cloned, true
	default:
		return nil, false
	}
}

func cloneAgenticMap(item map[string]any) map[string]any {
	cloned := make(map[string]any, len(item))
	for key, value := range item {
		cloned[key] = value
	}
	return cloned
}

func truncateTextToOpenAITokenBudget(text string, maxTokens int) string {
	text = strings.TrimSpace(text)
	if maxTokens <= 0 || text == "" {
		return ""
	}
	maxBytes := approxOpenAIBytesForTokens(maxTokens)
	if len(text) <= maxBytes {
		return text
	}
	return strings.TrimSpace(truncateMiddleByByteEstimate(text, maxBytes, true))
}

func truncateMiddleByByteEstimate(text string, maxBytes int, useTokens bool) string {
	if text == "" {
		return ""
	}
	totalChars := len([]rune(text))
	if maxBytes <= 0 {
		return openAITruncationMarker(useTokens, openAIRemovedUnits(useTokens, len(text), totalChars))
	}
	if len(text) <= maxBytes {
		return text
	}

	leftBudget := maxBytes / 2
	rightBudget := maxBytes - leftBudget
	removedChars, left, right := splitOpenAITruncationString(text, leftBudget, rightBudget)
	marker := openAITruncationMarker(useTokens, openAIRemovedUnits(useTokens, len(text)-maxBytes, removedChars))
	return left + marker + right
}

func splitOpenAITruncationString(text string, beginningBytes, endBytes int) (int, string, string) {
	if text == "" {
		return 0, "", ""
	}
	textLen := len(text)
	tailStartTarget := textLen - endBytes
	if tailStartTarget < 0 {
		tailStartTarget = 0
	}

	prefixEnd := 0
	suffixStart := textLen
	removedChars := 0
	suffixStarted := false
	for idx, ch := range text {
		charEnd := idx + len(string(ch))
		if charEnd <= beginningBytes {
			prefixEnd = charEnd
			continue
		}
		if idx >= tailStartTarget {
			if !suffixStarted {
				suffixStart = idx
				suffixStarted = true
			}
			continue
		}
		removedChars++
	}
	if suffixStart < prefixEnd {
		suffixStart = prefixEnd
	}
	return removedChars, text[:prefixEnd], text[suffixStart:]
}

func openAITruncationMarker(useTokens bool, removedCount int) string {
	if useTokens {
		return fmt.Sprintf("…%d tokens truncated…", removedCount)
	}
	return fmt.Sprintf("…%d chars truncated…", removedCount)
}

func openAIRemovedUnits(useTokens bool, removedBytes, removedChars int) int {
	if useTokens {
		return approxOpenAITokensFromByteCount(removedBytes)
	}
	return removedChars
}

func openAICompactionV2Instructions(opts *AgenticOptions, isChatGPTOAuth bool) string {
	if opts != nil {
		if system := strings.TrimSpace(opts.System); system != "" {
			return system
		}
	}
	if isChatGPTOAuth {
		return "You are a helpful assistant."
	}
	return ""
}

func openAIModelSupportsParallelToolCalls(model string) bool {
	return !isResponsesLiteWebsocketModel(model)
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
		total += approxOpenAITokenCount(instructions)
	}
	if len(tools) > 0 {
		if encoded, err := json.Marshal(tools); err == nil {
			total += approxOpenAITokensFromByteCount(len(encoded))
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

func statelessOAuthOutputItems(items []any) []any {
	filtered := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if ok && strings.EqualFold(strings.TrimSpace(stringFromAny(item["type"])), "reasoning") &&
			strings.TrimSpace(stringFromAny(item["encrypted_content"])) == "" {
			continue
		}
		filtered = append(filtered, raw)
	}
	return filtered
}

// sendAgenticTurn sends a single request and returns parsed results.
func (c *Client) sendAgenticTurn(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions, isChatGPTOAuth bool) (*agenticTurnResult, error) {
	policy := httpretry.DefaultPolicy()
	policy.MaxRetries = 0
	policy.AllowReplay = true
	policy.RetryableError = isRetryableResponsesTransportError
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[openai-client] stream error before output, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	result, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*agenticTurnResult, bool, error) {
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
		result, err := c.sendAgenticTurnOnce(attemptCtx, inputItems, tools, &attemptOpts, isChatGPTOAuth)
		return result, observed, err
	})
	return result, err
}

func (c *Client) sendAgenticTurnOnce(ctx context.Context, inputItems []any, tools []ToolDefinition, opts *AgenticOptions, isChatGPTOAuth bool) (*agenticTurnResult, error) {
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
		payload["include"] = []string{"reasoning.encrypted_content"}
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

	// Add provider-native web search tool when enabled and model supports it.
	// This is a native provider tool (not a function tool) executed server-side.
	if opts.WebSearchEnabled && openAIModelSupportsWebSearch(opts.Model) {
		existing, _ := payload["tools"].([]ToolDefinition)
		rawTools := make([]any, 0, len(existing)+1)
		for _, t := range existing {
			rawTools = append(rawTools, t)
		}
		rawTools = append(rawTools, map[string]any{"type": openAIWebSearchToolType})
		payload["tools"] = rawTools
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

	if isResponsesLiteWebsocketModel(opts.Model) {
		wsPayload := buildResponsesLiteWebsocketPayload(payload, system, c.sessionID)
		openStream := func(useWebsocket bool) (io.ReadCloser, error) {
			if useWebsocket {
				return c.openResponsesWebsocketStream(ctx, wsPayload, isChatGPTOAuth)
			}
			return c.openResponsesLiteHTTPStream(ctx, wsPayload, isChatGPTOAuth)
		}
		useWebsocket := !c.responsesTransportState.websocketDisabled.Load()
		body, wsErr := openStream(useWebsocket)
		if useWebsocket && shouldFallbackResponsesWebsocket(ctx, wsErr) {
			c.responsesTransportState.disableWebsocket()
			return nil, wsErr
		}
		if wsErr != nil {
			return nil, wsErr
		}
		onText := func(text string) {
			if opts.OnText != nil {
				opts.OnText(text)
			}
		}
		onThinking := func(text string) {
			if opts.OnThinking != nil {
				opts.OnThinking(text)
			}
		}
		onToolUse := func(name string, input json.RawMessage) {
			if opts.OnToolUse != nil {
				opts.OnToolUse(name, input)
			}
		}
		onToolResult := func(name, output string, isError bool) {
			if opts.OnToolResult != nil {
				opts.OnToolResult(name, output, isError)
			}
		}
		result, wsErr := c.parseAgenticStreamWithToolCallbacks(body, onText, onThinking, onToolUse, onToolResult)
		body.Close()
		if wsErr != nil && useWebsocket && shouldFallbackResponsesWebsocket(ctx, wsErr) {
			c.responsesTransportState.disableWebsocket()
		}
		if wsErr != nil {
			return result, httpretry.NewStreamError(wsErr)
		}
		return result, nil
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

	resp, err := c.doWithOAuthRecovery(ctx, endpoint, isChatGPTOAuth, buildReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	for k, v := range resp.Header {
		kl := strings.ToLower(k)
		if strings.Contains(kl, "ratelimit") || strings.Contains(kl, "rate-limit") || strings.Contains(kl, "retry") || strings.Contains(kl, "x-openai") || strings.Contains(kl, "x-request") {
			applog.Debugf("[openai-headers] %s: %v", k, v)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		trimmed := strings.TrimSpace(string(errBody))
		if trimmed == "" {
			return nil, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %d %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode)))
		}
		return nil, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %d %s %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode), trimmed))
	}

	result, err := c.parseAgenticStreamWithToolCallbacks(resp.Body, opts.OnText, opts.OnThinking, opts.OnToolUse, opts.OnToolResult)
	if err != nil {
		return result, httpretry.NewStreamError(err)
	}
	return result, nil
}

// parseAgenticStream parses a streaming response, handling text deltas and function calls.
func (c *Client) parseAgenticStream(body io.Reader, onText func(string), onThinking func(string)) (*agenticTurnResult, error) {
	return c.parseAgenticStreamWithToolCallbacks(body, onText, onThinking, nil, nil)
}

func (c *Client) parseAgenticStreamWithToolCallbacks(body io.Reader, onText func(string), onThinking func(string), onToolUse func(string, json.RawMessage), onToolResult func(string, string, bool)) (*agenticTurnResult, error) {
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
	// bool value indicates whether the emitted tool-use had display detail.
	providerNativeToolUseEmitted := make(map[string]bool)

	emitter := newOpenAITextStreamEmitter(func(text string) {
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
				emitter.Write(delta)
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
			emitter.FlushBoundary()
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
					emitter.FlushBoundary()
					fnCalls[idx] = &fnCallState{
						callID: stringFromAny(item["call_id"]),
						name:   stringFromAny(item["name"]),
					}
				} else if isProviderNativeOutputItem(itemType) {
					emitter.FlushBoundary()
					// Provider-native tool items can carry query/url details on
					// the .added event and only status on .done; surface use details
					// as soon as we see them.
					if evt, ok := providerNativeToolEventFromOutputItem(item); ok {
						if !evt.hasDisplayDetail {
							// Wait for .done when .added is detail-less so we don't
							// lock in an empty secondary label.
							continue
						}
						key := providerNativeOutputItemKey(item, idx)
						if onToolUse != nil {
							if _, seen := providerNativeToolUseEmitted[key]; !seen {
								onToolUse(evt.name, evt.input)
								providerNativeToolUseEmitted[key] = evt.hasDisplayDetail
							}
						}
					}
				}
			}

		case "response.output_item.done":
			if item, ok := ev["item"].(map[string]any); ok {
				itemType := stringFromAny(item["type"])
				switch itemType {
				case "function_call":
					emitter.FlushBoundary()
					callID := stringFromAny(item["call_id"])
					name := stringFromAny(item["name"])
					fc := fnCalls[intFromAny(ev["output_index"])]
					if fc == nil && callID != "" {
						for _, candidate := range fnCalls {
							if candidate != nil && candidate.callID == callID {
								fc = candidate
								break
							}
						}
					}
					streamedArgs := ""
					if fc != nil {
						streamedArgs = fc.args.String()
						if callID == "" {
							callID = fc.callID
						}
						if name == "" {
							name = fc.name
						}
					}
					args := completedOpenAIFunctionCallArguments(item["arguments"], streamedArgs)
					if name != "" {
						result.toolCalls = append(result.toolCalls, toolCallInfo{
							CallID:    callID,
							Name:      name,
							Arguments: args,
						})
						// Add to output items for round-tripping. OpenAI expects
						// function_call input items to carry arguments as a JSON string;
						// an empty object is valid, but an omitted field can make the
						// next turn replay invalid.
						item["arguments"] = args
						result.outputItems = append(result.outputItems, item)
					}
				case "message":
					// Text message items are handled via deltas
					result.outputItems = append(result.outputItems, item)
				case "reasoning":
					result.outputItems = append(result.outputItems, item)
					if onThinking != nil {
						if text := openAIReasoningTextFromItem(item); text != "" {
							onThinking(text)
							sawThinking = true
						}
					}
				case "compaction":
					result.outputItems = append(result.outputItems, item)
				default:
					// Provider-native output items (web_search_call, etc.) are
					// round-tripped but not locally executed.
					if isProviderNativeOutputItem(itemType) {
						result.outputItems = append(result.outputItems, item)
						if evt, ok := providerNativeToolEventFromOutputItem(item); ok {
							key := providerNativeOutputItemKey(item, intFromAny(ev["output_index"]))
							if onToolUse != nil {
								emittedWithDetail, seen := providerNativeToolUseEmitted[key]
								if !seen || (!emittedWithDetail && evt.hasDisplayDetail) {
									onToolUse(evt.name, evt.input)
									providerNativeToolUseEmitted[key] = evt.hasDisplayDetail
								}
							}
							if onToolResult != nil && evt.hasResult {
								onToolResult(evt.name, evt.output, evt.isError)
							}
						}
					}
				}
			}

		case "response.failed", "response.incomplete", "response.error", "error":
			return nil, responsesStreamTerminalError(typ, ev)

		case "response.completed":
			if m, ok := ev["response"].(map[string]any); ok {
				completed = m
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if completed == nil {
		return nil, io.ErrUnexpectedEOF
	}

	emitter.Flush()

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

type providerNativeToolEvent struct {
	name             string
	input            json.RawMessage
	output           string
	hasResult        bool
	isError          bool
	hasDisplayDetail bool
}

func providerNativeToolEventFromOutputItem(item map[string]any) (providerNativeToolEvent, bool) {
	itemType := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
	switch itemType {
	case "web_search_call", "tool_search_call":
		input := map[string]any{}
		query := strings.TrimSpace(openAIExtractWebSearchQuery(item))
		if query != "" {
			input["query"] = query
		}
		url := strings.TrimSpace(openAIExtractWebSearchURL(item))
		if url != "" {
			input["url"] = url
		}
		pattern := strings.TrimSpace(stringFromAny(item["pattern"]))

		var sourceCount int
		if action, ok := item["action"].(map[string]any); ok {
			actionType := strings.TrimSpace(stringFromAny(action["type"]))
			if actionType != "" {
				input["action"] = actionType
			}
			if url := strings.TrimSpace(stringFromAny(action["url"])); url != "" {
				input["url"] = url
			}
			if p := strings.TrimSpace(stringFromAny(action["pattern"])); p != "" {
				pattern = p
				input["pattern"] = p
			}
			if sources, ok := action["sources"].([]any); ok {
				sourceCount = len(sources)
			}
		}
		if pattern != "" {
			input["pattern"] = pattern
		}

		inputJSON := json.RawMessage(`{}`)
		if len(input) > 0 {
			if b, err := json.Marshal(input); err == nil {
				inputJSON = b
			}
		}

		status := strings.ToLower(strings.TrimSpace(stringFromAny(item["status"])))
		if status == "" {
			status = "completed"
		}
		isError := strings.Contains(status, "error") || strings.Contains(status, "fail")
		output := "status: " + status
		if sourceCount > 0 {
			output += fmt.Sprintf(", sources: %d", sourceCount)
		}

		return providerNativeToolEvent{
			name:             "web_search",
			input:            inputJSON,
			output:           output,
			hasResult:        true,
			isError:          isError,
			hasDisplayDetail: query != "" || url != "" || pattern != "",
		}, true
	default:
		return providerNativeToolEvent{}, false
	}
}

func providerNativeOutputItemKey(item map[string]any, outputIndex int) string {
	if id := strings.TrimSpace(stringFromAny(item["id"])); id != "" {
		return id
	}
	typ := strings.ToLower(strings.TrimSpace(stringFromAny(item["type"])))
	if outputIndex >= 0 {
		return fmt.Sprintf("%s:%d", typ, outputIndex)
	}
	return typ
}

// openAIModelSupportsWebSearch returns true if the model supports the native
// web_search tool. Based on models_cache.json supports_search_tool
// field; gpt-5.2+ families support it.
func openAIModelSupportsWebSearch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "gpt-6-astra") ||
		strings.HasPrefix(m, "gpt-5.5") ||
		strings.HasPrefix(m, "gpt-5.4") ||
		strings.HasPrefix(m, "gpt-5.3") ||
		strings.HasPrefix(m, "gpt-5.2")
}

// isProviderNativeOutputItem returns true if the output item type is a
// provider-executed tool result that should be round-tripped in the
// conversation but not locally executed.
func isProviderNativeOutputItem(itemType string) bool {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "web_search_call", "tool_search_call":
		return true
	default:
		return false
	}
}
