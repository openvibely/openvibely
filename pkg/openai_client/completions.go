package openaiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"github.com/openvibely/openvibely/internal/llm/streamdecode"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
)

// CompletionsOptions configures a /v1/chat/completions call with tool use.
type CompletionsOptions struct {
	preparedMessages *requestjson.Field
	preparedTools    *requestjson.Field
	responseText     *strings.Builder
	// OnReplay durably saves each assistant response before executing its tools.
	OnReplay func(context.Context, []CompletionsHistoryMessage) error
	// OnReplayChanged synchronously saves a read-only snapshot and its changed suffix.
	OnReplayChanged func(context.Context, []CompletionsHistoryMessage, int) error
	Model           string
	MaxOutputTokens int
	ContextWindow   int
	// ToolOutputTokenLimit bounds each tool result replayed to the model while
	// callbacks and durable transcripts retain the complete output.
	ToolOutputTokenLimit int
	// Temperature preserves explicit zero, which many providers treat
	// differently from their default. Use OmittedTemperature for models that
	// do not accept the parameter.
	Temperature float64
	// ReasoningEffort is sent for first-party GPT-6 Sol/Luna requests. When
	// function tools are present those models require none on Chat Completions.
	ReasoningEffort          string
	ServiceTier              string
	FirstPartyOpenAI         bool
	System                   string
	WorkDir                  string
	MaxTurns                 int
	ContinueFromHistory      bool // Resume recovered history without repeating the consumed prompt.
	OmitExecutionEnvironment bool // Raw utility requests may omit coding environment context.
	DisableTools             bool
	// SkipDefaultTools suppresses built-in local tools while still allowing
	// ExtraTools (for example request-scoped runtime tools) to be sent.
	SkipDefaultTools bool
	Attachments      []*FileAttachment
	// ExtraTools are appended to the default local tools (for example MCP tools).
	ExtraTools []ToolDefinition
	// ExtraHeaders are sent with each Chat Completions request. Values may contain
	// provider secrets and must not be logged by callers.
	ExtraHeaders map[string]string
	// FinalizeRequest runs after the exact JSON body and ordinary headers have
	// been applied. Custom providers use it for body-dependent request signing.
	FinalizeRequest func(*http.Request, []byte) error
	// ExtraBody is merged into each Chat Completions request after OpenVibely-owned
	// fields are set. Protected fields are ignored.
	ExtraBody map[string]interface{}
	// ToolExecutor overrides tool execution. It should return (output, isError, err).
	// If nil, built-in local tool execution is used.
	ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, bool, error)
	// ToolFilter can deny tool execution by name at runtime.
	ToolFilter func(name string) bool

	// OnToolBoundarySteering consumes pending text after all tool results and
	// images have been appended, before the next model request.
	OnToolBoundarySteering func(context.Context) (string, error)

	// Callbacks for real-time output
	OnText       func(text string)
	OnToolUse    func(name string, input json.RawMessage)
	OnToolResult func(name string, output string, isError bool)
}

// OmittedTemperature returns a sentinel that causes SendCompletions to omit
// the temperature field from the request.
func OmittedTemperature() float64 {
	return math.NaN()
}

// completionsMessage represents a message in the /v1/chat/completions format.
type completionsMessage struct {
	rawReplay        *immutableCompletionsReplay
	Role             string                `json:"role"`
	Content          interface{}           `json:"content"` // string or array of content blocks
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	ToolCalls        []CompletionsToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string                `json:"tool_call_id,omitempty"`
}

// SendCompletions sends a message using the /v1/chat/completions API with tool use.
// This is the standard OpenAI API format (not Responses API).
func (c *Client) SendCompletions(ctx context.Context, prompt string, opts *CompletionsOptions) (*AgenticResponse, error) {
	c.lastCompletionsReasoning = ""
	c.lastCompletionsTranscript = nil
	if opts == nil {
		opts = &CompletionsOptions{}
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
	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}

	var toolDefs []ToolDefinition
	if !opts.DisableTools {
		if !opts.SkipDefaultTools {
			toolDefs = append(toolDefs, DefaultTools()...)
		}
		toolDefs = append(toolDefs, opts.ExtraTools...)
		// Retain candidates; filter immediately before each request.
	}

	tools := make([]map[string]interface{}, 0, len(toolDefs))
	for _, td := range toolDefs {
		function := map[string]interface{}{"name": td.Name, "description": td.Description, "parameters": td.ParameterSchema()}
		function["strict"] = false
		tools = append(tools, map[string]interface{}{
			"type":     "function",
			"function": function,
		})
	}

	// Build initial messages from history + new prompt
	messages := make([]completionsMessage, 0, len(c.History)+2)
	// Repair constructs its own slice and never mutates the source messages.
	// SetCompletionsHistory already isolated caller-owned tool-call slices.
	existingCompletionsHistory := c.completionsHistory
	if existingCompletionsHistory == nil && len(c.History) > 0 {
		existingCompletionsHistory = make([]CompletionsHistoryMessage, 0, len(c.History))
		for _, message := range c.History {
			existingCompletionsHistory = append(existingCompletionsHistory, CompletionsHistoryMessage{
				Role:    message.Role,
				Content: message.Content,
			})
		}
	}
	existingCompletionsHistory = repairInterruptedCompletionsToolCalls(existingCompletionsHistory)
	var environmentUpdate []CompletionsHistoryMessage
	if !opts.OmitExecutionEnvironment {
		before := len(existingCompletionsHistory)
		existingCompletionsHistory = ensureCompletionsHistoryEnvironment(existingCompletionsHistory, opts.WorkDir)
		if len(existingCompletionsHistory) != before {
			environmentUpdate = ensureCompletionsHistoryEnvironment(nil, opts.WorkDir)
		}
	}
	if opts.System != "" {
		messages = append(messages, completionsMessage{
			Role:    "system",
			Content: opts.System,
		})
	}
	if existingCompletionsHistory != nil {
		for _, msg := range existingCompletionsHistory {
			var content interface{} = msg.Content
			if len(msg.ImageContent) > 0 {
				content = msg.ImageContent
			}
			if msg.Role == "tool" {
				content = truncateToolOutputForModelInput(msg.Content, opts.ToolOutputTokenLimit)
			}
			raw := msg.rawReplay
			if text, ok := content.(string); !ok || text != msg.Content {
				raw = nil
			}
			messages = append(messages, completionsMessage{
				rawReplay:        raw,
				Role:             msg.Role,
				Content:          content,
				ReasoningContent: msg.ReasoningContent,
				ToolCalls:        append([]CompletionsToolCall(nil), msg.ToolCalls...),
				ToolCallID:       msg.ToolCallID,
			})
		}
	} else {
		for _, msg := range c.History {
			content := msg.Content
			if msg.Role == "tool" {
				content = truncateToolOutputForModelInput(content, opts.ToolOutputTokenLimit)
			}
			messages = append(messages, completionsMessage{
				Role:    msg.Role,
				Content: content,
			})
		}
	}

	var promptBlocks []map[string]interface{}
	// Add current prompt with optional attachments
	if len(opts.Attachments) > 0 && !opts.ContinueFromHistory {
		content := make([]map[string]interface{}, 0, 1+len(opts.Attachments))
		if prompt != "" {
			content = append(content, map[string]interface{}{
				"type": "text",
				"text": prompt,
			})
		}
		for _, att := range opts.Attachments {
			data, err := att.loadData()
			if err != nil {
				return nil, fmt.Errorf("load attachment %s: %w", att.FileName, err)
			}

			if IsImageMediaType(att.MediaType) {
				content = append(content, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": imageDataURL(att.MediaType, data),
					},
				})
			} else if att.MediaType == "application/pdf" {
				return nil, fmt.Errorf("PDF document input is unavailable on this Chat Completions connection; use a document-capable Responses or Anthropic connection")
			} else if IsTextMediaType(att.MediaType) {
				// For non-image files, add as text context
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": fmt.Sprintf("--- File: %s ---\n%s\n--- End of %s ---", att.FileName, string(data), att.FileName),
				})
			}
		}
		messages = append(messages, completionsMessage{
			Role:    "user",
			Content: content,
		})
		promptBlocks = content
	} else if !opts.ContinueFromHistory {
		messages = append(messages, completionsMessage{
			Role:    "user",
			Content: prompt,
		})
	}

	if !opts.OmitExecutionEnvironment {
		messages = ensureCompletionsEnvironment(messages, opts.WorkDir)
	}
	result := &AgenticResponse{Model: opts.Model}
	var allText strings.Builder
	finalReasoningContent := ""
	var currentTranscript []CompletionsHistoryMessage
	if !opts.ContinueFromHistory {
		currentTranscript = []CompletionsHistoryMessage{{Role: "user", Content: prompt, ImageContent: promptBlocks}}
	}

	firstChanged := 0
	replaySnapshot := func() []CompletionsHistoryMessage {
		items := currentTranscript
		if opts.OnReplayChanged == nil {
			items = cloneCompletionsHistory(items)
		}
		snapshot := make([]CompletionsHistoryMessage, 0, len(environmentUpdate)+len(items))
		snapshot = append(snapshot, environmentUpdate...)
		return append(snapshot, items...)
	}
	saveReplay := func(saveCtx context.Context, snapshot []CompletionsHistoryMessage) error {
		var err error
		if opts.OnReplayChanged != nil {
			err = opts.OnReplayChanged(saveCtx, snapshot, firstChanged)
		} else if opts.OnReplay != nil {
			err = opts.OnReplay(saveCtx, snapshot)
		}
		if err == nil {
			firstChanged = len(snapshot)
		}
		return err
	}

	// Publish completed exchanges even when a subsequent request is cancelled.
	defer func() {
		c.completionsHistory = append(existingCompletionsHistory, cloneCompletionsHistory(currentTranscript)...)
		c.lastCompletionsReasoning = finalReasoningContent
		c.lastCompletionsTranscript = append(environmentUpdate, cloneCompletionsHistory(currentTranscript)...)
	}()

	for turn := 0; turn < opts.MaxTurns; turn++ {
		readContext := func(m completionsMessage) (string, string) {
			text, _ := m.Content.(string)
			return m.Role, text
		}
		makeReplayContext := func(text string) CompletionsHistoryMessage {
			return CompletionsHistoryMessage{Role: "user", Content: text}
		}
		makeWireContext := func(text string) completionsMessage { return completionsMessage{Role: "user", Content: text} }
		beforeContext := len(currentTranscript)
		currentTranscript = managedcontext.SeedReplay(ctx, messages, currentTranscript, readContext, makeReplayContext)
		messages = managedcontext.Ensure(ctx, messages, readContext, makeWireContext)
		currentTranscript = attachmentcontext.SeedReplay(ctx, messages, currentTranscript, readContext, makeReplayContext)
		messages = attachmentcontext.Ensure(ctx, messages, readContext, makeWireContext)
		if len(currentTranscript) != beforeContext {
			firstChanged = min(firstChanged, len(environmentUpdate)+max(0, beforeContext-1))
		}
		// Summary compaction sends first and trims only after provider overflow.
		_, summarizing := llmcontracts.SummarySystemPromptFromContext(ctx)
		prepared, prepareErr := prepareCompletionsMessages(messages)
		if prepareErr != nil {
			return nil, prepareErr
		}
		preparedOpts := *opts
		preparedOpts.preparedMessages = prepared
		if len(tools) > 0 {
			preparedOpts.preparedTools, prepareErr = requestjson.Prepare("tools", tools)
			if prepareErr != nil {
				return nil, prepareErr
			}
		}
		if err := ensureCompletionsRequestFits(messages, tools, &preparedOpts); err != nil && !summarizing {
			return nil, err
		}
		// Keep text actually delivered to the caller if this sampling attempt
		// fails before it can publish a completed assistant message.
		var partialText strings.Builder
		streamOpts := preparedOpts
		streamOpts.responseText = &partialText

		turnResult, err := httpretry.DoStreamTurn(ctx, httpretry.StreamTurnPolicy{
			OnRetry: func(event httpretry.RetryEvent) {
				if httpretry.IsConnectionSetupFailure(event.Err) {
					applog.Infof("[openai-completions] reconnecting turn %d after connection error in %v: %v", turn+1, event.Delay, event.Err)
					return
				}
				applog.Infof("[openai-completions] retrying turn %d after stream/transport error, retry attempt %d/%d in %v: %v",
					turn+1, event.Attempt, event.MaxRetries, event.Delay, event.Err)
			},
		}, func(attemptCtx context.Context) (*completionsTurnResult, error) {
			return c.sendCompletionsTurn(attemptCtx, messages, tools, &streamOpts)
		})
		if err != nil {
			if text := partialText.String(); text != "" {
				currentTranscript = append(currentTranscript, CompletionsHistoryMessage{Role: "assistant", Content: text})
			}
			return nil, CategorizeProviderError(fmt.Errorf("turn %d: %w", turn+1, err))
		}

		result.InputTokens += turnResult.inputTokens
		result.LastContextTokens = 0
		if turnResult.inputTokens > 0 {
			result.LastContextTokens = turnResult.inputTokens + turnResult.outputTokens
		}
		result.OutputTokens += turnResult.outputTokens
		result.TotalTokens += turnResult.totalTokens
		result.CachedInputTokens += turnResult.cachedInputTokens
		result.ReasoningTokens += turnResult.reasoningTokens
		result.StopReason = turnResult.stopReason
		if turnResult.model != "" {
			result.Model = turnResult.model
		}

		allText.WriteString(turnResult.text)
		finalReasoningContent = turnResult.reasoningContent
		currentTranscript = append(currentTranscript, CompletionsHistoryMessage{
			Role:             "assistant",
			Content:          turnResult.text,
			ReasoningContent: turnResult.reasoningContent,
			ToolCalls:        append([]CompletionsToolCall(nil), turnResult.toolCalls...),
		})

		if opts.OnReplay != nil || opts.OnReplayChanged != nil {
			if err := saveReplay(ctx, replaySnapshot()); err != nil {
				return nil, err
			}
		}

		// Add assistant message to history
		if len(turnResult.toolCalls) > 0 {
			messages = append(messages, completionsMessage{
				Role:             "assistant",
				Content:          turnResult.text,
				ReasoningContent: turnResult.reasoningContent,
				ToolCalls:        turnResult.toolCalls,
			})
		} else {
			messages = append(messages, completionsMessage{
				Role:    "assistant",
				Content: turnResult.text,
			})
		}

		// If no tool calls, we're done
		if len(turnResult.toolCalls) == 0 {
			break
		}

		// request_user_input must be the only executable call in a model turn. Its
		// answer cannot authorize calls whose arguments were produced before it.
		requestInputIndex := exclusiveCompletionsRequestUserInput(turnResult.toolCalls)
		// Execute tools and add results
		var imageMessages []completionsMessage
		for toolIndex, tc := range turnResult.toolCalls {
			inputJSON := json.RawMessage(tc.Function.Arguments)
			if opts.OnToolUse != nil {
				opts.OnToolUse(tc.Function.Name, inputJSON)
			}

			applog.Debugf("[openai-completions] executing tool %s", tc.Function.Name)
			output := ""
			isError := false
			var err error
			inputJSON, validationErr := normalizeDefinedArguments(toolDefs, tc.Function.Name, inputJSON)
			if validationErr != nil {
				isError = true
				output = validationErr.Error()
			} else if requestInputIndex >= 0 && toolIndex != requestInputIndex {
				isError = true
				output = "tool call rejected: request_user_input must complete in its own model turn"
			} else if opts.ToolFilter != nil && !opts.ToolFilter(tc.Function.Name) {
				isError = true
				output = fmt.Sprintf("tool %s is not allowed by this agent", tc.Function.Name)
			} else if opts.ToolExecutor != nil {
				output, isError, err = opts.ToolExecutor(llmcontracts.WithToolCallID(ctx, tc.ID), tc.Function.Name, inputJSON)
				if err != nil {
					isError = true
					output = err.Error()
				}
			} else {
				output, err = ExecuteTool(ctx, opts.WorkDir, tc.Function.Name, inputJSON)
				if err != nil {
					isError = true
					output = err.Error()
				}
			}

			if opts.OnToolResult != nil {
				opts.OnToolResult(tc.Function.Name, output, isError)
			}

			var inputMap map[string]interface{}
			json.Unmarshal(inputJSON, &inputMap)

			result.ToolCalls = append(result.ToolCalls, ToolCall{
				Name:   tc.Function.Name,
				Input:  inputMap,
				Output: output,
				Error:  isError,
			})

			// Add a bounded tool result to model-facing history. The complete output
			// remains in result.ToolCalls and currentTranscript for durability.
			messages = append(messages, completionsMessage{
				Role:       "tool",
				Content:    truncateToolOutputForModelInput(output, opts.ToolOutputTokenLimit),
				ToolCallID: tc.ID,
			})
			firstChanged = min(firstChanged, len(environmentUpdate)+len(currentTranscript))
			currentTranscript = append(currentTranscript, CompletionsHistoryMessage{
				Role:       "tool",
				Content:    output,
				ToolCallID: tc.ID,
			})
			if image, ok := attachment.TakeToolImage(ctx, output); ok && !isError {
				content := []map[string]interface{}{
					{"type": "text", "text": "Image returned by view_image: " + image.Name},
					{"type": "image_url", "image_url": map[string]interface{}{"url": "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)}},
				}
				imageMessages = append(imageMessages, completionsMessage{Role: "user", Content: content})
			}
			if opts.OnReplay != nil || opts.OnReplayChanged != nil {
				snapshot := replaySnapshot()
				for _, image := range imageMessages {
					snapshot = append(snapshot, CompletionsHistoryMessage{Role: "user", ImageContent: image.Content.([]map[string]interface{})})
				}
				if err := saveReplay(context.WithoutCancel(ctx), snapshot); err != nil {
					applog.Infof("[openai-completions] intermediate tool transcript save failed; continuing with in-memory history: %v", err)
				}
			}
		}
		messages = append(messages, imageMessages...)
		for _, image := range imageMessages {
			currentTranscript = append(currentTranscript, CompletionsHistoryMessage{Role: "user", ImageContent: image.Content.([]map[string]interface{})})
		}
		if opts.OnToolBoundarySteering != nil {
			steering, err := opts.OnToolBoundarySteering(ctx)
			if err != nil {
				return nil, fmt.Errorf("tool-boundary steering: %w", err)
			}
			if steering = strings.TrimSpace(steering); steering != "" {
				messages = append(messages, completionsMessage{Role: "user", Content: steering})
				currentTranscript = append(currentTranscript, CompletionsHistoryMessage{Role: "user", Content: steering})
			}
		}

	}

	result.Text = allText.String()

	// Update client history
	if !opts.OmitExecutionEnvironment {
		c.History = ensureHistoryEnvironment(c.History, opts.WorkDir)
	}
	c.History = append(c.History, Message{Role: "user", Content: prompt})
	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})

	return result, nil
}

func exclusiveCompletionsRequestUserInput(calls []CompletionsToolCall) int {
	if len(calls) < 2 {
		return -1
	}
	for i, call := range calls {
		if call.Function.Name == "request_user_input" {
			return i
		}
	}
	return -1
}

func mergeCompletionsExtraBody(payload map[string]interface{}, extra map[string]interface{}) {
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" || isProtectedCompletionsBodyField(key) {
			continue
		}
		payload[key] = value
	}
}

func isProtectedCompletionsBodyField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "model", "messages", "stream", "tools", "tool_choice":
		return true
	default:
		return false
	}
}

type completionsTurnResult struct {
	text              string
	reasoningContent  string
	toolCalls         []CompletionsToolCall
	stopReason        string
	model             string
	inputTokens       int
	outputTokens      int
	totalTokens       int
	cachedInputTokens int
	reasoningTokens   int
}

func ensureCompletionsRequestFits(messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) error {
	if opts == nil {
		return nil
	}
	window := opts.ContextWindow
	if window <= 0 {
		window = 128000
	}
	safe := window - opts.MaxOutputTokens - max(1024, window/50)
	prepared := opts.preparedMessages
	if prepared == nil {
		var err error
		prepared, err = prepareCompletionsMessages(messages)
		if err != nil {
			return err
		}
	}
	metadata := requestjson.Metadata{Value: struct{}{}, Fields: map[string]*requestjson.Field{}}
	if len(tools) > 0 {
		field := opts.preparedTools
		if field == nil {
			var err error
			field, err = requestjson.Prepare("tools", tools)
			if err != nil {
				return err
			}
		}
		metadata.Fields["tools"] = field
	}
	size, err := prepared.Size(metadata)
	if err != nil {
		return err
	}
	imageTokens := 2000 // Compatible-provider fallback; not a billing estimate.
	if opts.FirstPartyOpenAI {
		imageTokens = 1844
	}
	adjustment, err := tokenestimate.MessageArrayAdjustment(prepared.JSON(), "image_url", imageTokens)
	if err != nil {
		return err
	}
	tokens := tokenestimate.FromByteCount(size + adjustment)
	if tokens <= safe {
		return nil
	}
	return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Chat Completions preflight", fmt.Errorf("complete request requires %d input tokens; safe limit is %d", tokens, safe))
}

func (c *Client) sendCompletionsTurn(ctx context.Context, messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) (*completionsTurnResult, error) {
	policy := httpretry.DefaultPolicy()
	policy.MaxRetries = 0
	policy.AllowReplay = true
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[openai-client] chat completions stream error before output, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	result, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*completionsTurnResult, bool, error) {
		attemptOpts := *opts
		observed := false
		attemptOpts.OnText = func(text string) {
			observed = true
			if opts.OnText != nil {
				opts.OnText(text)
			}
		}
		result, err := c.sendCompletionsTurnOnce(attemptCtx, messages, tools, &attemptOpts)
		return result, observed, err
	})
	return result, err
}

func isGPT6SolOrLuna(model string) bool {
	spec, ok := models.LookupModel(models.ProviderOpenAI, model)
	return ok && spec.ChatCompletionsReasoningMode == models.ChatCompletionsReasoningWhenToolFree
}

func isGPT61Sol(model string) bool {
	spec, ok := models.LookupModel(models.ProviderOpenAI, model)
	return ok && spec.ChatCompletionsReasoningMode == models.ChatCompletionsReasoningRequired
}

func completionsReasoningEffort(model, value string, hasTools bool) string {
	if isGPT61Sol(model) {
		if effort := normalizeReasoningEffort(value); effort != "" && effort != "none" {
			return effort
		}
		return "medium"
	}
	if !isGPT6SolOrLuna(model) {
		return ""
	}
	if hasTools {
		return "none"
	}
	if effort := normalizeReasoningEffort(value); effort != "" {
		return effort
	}
	return "medium"
}

func completionsSupportsTemperature(model, reasoningEffort string) bool {
	if isGPT61Sol(model) {
		return false
	}
	return !isGPT6SolOrLuna(model) || reasoningEffort == "none"
}

func (c *Client) sendCompletionsTurnOnce(ctx context.Context, messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) (*completionsTurnResult, error) {
	selected := make([]int, 0, len(tools))
	for i := range tools {
		selected = append(selected, i)
	}
	if opts.ToolFilter != nil {
		permitted := make([]map[string]interface{}, 0, len(tools))
		selected = selected[:0]
		for index, tool := range tools {
			function, _ := tool["function"].(map[string]interface{})
			name, _ := function["name"].(string)
			if opts.ToolFilter(name) {
				permitted = append(permitted, tool)
				selected = append(selected, index)
			}
		}
		tools = permitted
	}
	payload := map[string]interface{}{
		"model":    opts.Model,
		"messages": messages,
		"stream":   true,
	}
	effort := ""
	if opts.FirstPartyOpenAI {
		applyServiceTier(payload, opts.Model, opts.ServiceTier)
		effort = completionsReasoningEffort(opts.Model, opts.ReasoningEffort, len(tools) > 0)
	}
	if effort != "" {
		payload["reasoning_effort"] = effort
	}
	if !math.IsNaN(opts.Temperature) && (!opts.FirstPartyOpenAI || completionsSupportsTemperature(opts.Model, effort)) {
		payload["temperature"] = opts.Temperature
	}

	if opts.MaxOutputTokens > 0 {
		payload["max_tokens"] = opts.MaxOutputTokens
	}

	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	mergeCompletionsExtraBody(payload, opts.ExtraBody)

	var body []byte
	var err error
	if opts.preparedMessages != nil {
		payload["messages"] = nil
		metadata := requestjson.Metadata{Value: payload, Fields: map[string]*requestjson.Field{}}
		if len(tools) > 0 && opts.preparedTools != nil {
			delete(payload, "tools")
			field, selectErr := opts.preparedTools.Select(selected, nil)
			if selectErr != nil {
				return nil, selectErr
			}
			metadata.Fields["tools"] = field
		}
		body, err = opts.preparedMessages.Marshal(metadata)
	} else {
		body, err = json.Marshal(payload)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint, err := c.completionsEndpoint()
	if err != nil {
		return nil, err
	}
	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(httpReq, false)
		for k, v := range opts.ExtraHeaders {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			httpReq.Header.Set(k, v)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if opts.FinalizeRequest != nil {
			if err := opts.FinalizeRequest(httpReq, body); err != nil {
				return nil, err
			}
		}
		return httpReq, nil
	}

	resp, err := c.doWithOAuthRecovery(ctx, endpoint, c.oauthUnauthorizedHandler != nil, buildReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		trimmed := strings.TrimSpace(string(errBody))
		if trimmed == "" {
			return nil, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %d %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode)))
		}
		return nil, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %d %s %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode), trimmed))
	}

	result, err := c.parseCompletionsStreamCapturing(resp.Body, opts.OnText, opts.responseText)
	if err != nil {
		return result, httpretry.NewStreamError(err)
	}
	return result, nil
}

func (c *Client) parseCompletionsStream(body io.Reader, onText func(string)) (*completionsTurnResult, error) {
	return c.parseCompletionsStreamCapturing(body, onText, nil)
}
func (c *Client) parseCompletionsStreamCapturing(body io.Reader, onText func(string), textBuilder *strings.Builder) (*completionsTurnResult, error) {
	scanner := streamdecode.Scanner(body, 10*1024*1024)

	result := &completionsTurnResult{}
	if textBuilder == nil {
		textBuilder = new(strings.Builder)
	}
	textStart := textBuilder.Len()
	var reasoning strings.Builder
	var arguments []*strings.Builder
	terminal := false

	for scanner.Scan() {
		data, ok := streamdecode.Data(scanner.Bytes(), false)
		if !ok {
			continue
		}

		if bytes.Equal(data, []byte("[DONE]")) {
			terminal = true
			continue
		}
		if len(data) == 0 {
			continue
		}

		var chunk completionsStreamChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if result.model == "" {
			result.model = chunk.Model
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta == nil {
			continue
		}
		choice := chunk.Choices[0]
		delta := choice.Delta
		if delta.Content != "" {
			textBuilder.WriteString(delta.Content)
			if onText != nil {
				onText(delta.Content)
			}
		}
		reasoning.WriteString(delta.ReasoningContent)
		for _, tc := range delta.ToolCalls {
			idx := len(result.toolCalls)
			if tc.Index != nil && *tc.Index >= 0 {
				idx = int(*tc.Index)
			}
			for len(result.toolCalls) <= idx {
				result.toolCalls = append(result.toolCalls, CompletionsToolCall{})
				arguments = append(arguments, new(strings.Builder))
			}
			if tc.ID != nil {
				result.toolCalls[idx].ID = *tc.ID
			}
			if tc.Type != nil {
				result.toolCalls[idx].Type = *tc.Type
			}
			if tc.Function.Name != nil {
				result.toolCalls[idx].Function.Name = *tc.Function.Name
			}
			arguments[idx].WriteString(tc.Function.Arguments)
		}
		if choice.FinishReason != "" {
			result.stopReason = choice.FinishReason
			terminal = true
		}
		if u := chunk.Usage; u != nil {
			if u.PromptTokens != nil {
				result.inputTokens = int(*u.PromptTokens)
			}
			if u.CompletionTokens != nil {
				result.outputTokens = int(*u.CompletionTokens)
			}
			if u.TotalTokens != nil {
				result.totalTokens = int(*u.TotalTokens)
			}
			if u.PromptDetails.CachedTokens != nil {
				result.cachedInputTokens = int(*u.PromptDetails.CachedTokens)
			}
			if u.CompletionDetails.ReasoningTokens != nil {
				result.reasoningTokens = int(*u.CompletionDetails.ReasoningTokens)
			}
			if u.ReasoningTokens != nil {
				result.reasoningTokens = int(*u.ReasoningTokens)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !terminal {
		return nil, io.ErrUnexpectedEOF
	}

	result.text = textBuilder.String()[textStart:]
	result.reasoningContent = reasoning.String()
	for i, b := range arguments {
		result.toolCalls[i].Function.Arguments = b.String()
	}
	if result.stopReason == "" {
		result.stopReason = "stop"
	}

	return result, nil
}

// Typed chunks avoid a map and boxed interface value for every text delta.
type completionsStreamChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta *struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    *float64 `json:"index"`
				ID       *string  `json:"id"`
				Type     *string  `json:"type"`
				Function struct {
					Name      *string `json:"name"`
					Arguments string  `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *float64 `json:"prompt_tokens"`
		CompletionTokens *float64 `json:"completion_tokens"`
		TotalTokens      *float64 `json:"total_tokens"`
		ReasoningTokens  *float64 `json:"reasoning_tokens"`
		PromptDetails    struct {
			CachedTokens *float64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			ReasoningTokens *float64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func prepareCompletionsMessages(messages []completionsMessage) (*requestjson.Field, error) {
	items := make([]any, len(messages))
	for i, m := range messages {
		var raw json.RawMessage
		if text, ok := m.Content.(string); ok && m.rawReplay != nil {
			raw = (CompletionsHistoryMessage{Role: m.Role, Content: text, ReasoningContent: m.ReasoningContent, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, rawReplay: m.rawReplay}).wireReplay()
		}
		if len(raw) > 0 {
			items[i] = raw
		} else {
			items[i] = m
		}
	}
	if messages == nil {
		items = nil
	}
	return requestjson.PrepareItems("messages", items)
}
