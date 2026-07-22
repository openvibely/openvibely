package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
)

// CompletionsOptions configures a /v1/chat/completions call with tool use.
type CompletionsOptions struct {
	Model           string
	MaxOutputTokens int
	Temperature     float64
	System          string
	WorkDir         string
	MaxTurns        int
	DisableTools    bool
	// SkipDefaultTools suppresses built-in local tools while still allowing
	// ExtraTools (for example request-scoped runtime tools) to be sent.
	SkipDefaultTools bool
	Attachments      []*FileAttachment
	// ExtraTools are appended to the default local tools (for example MCP tools).
	ExtraTools []ToolDefinition
	// ExtraHeaders are sent with each Chat Completions request. Values may contain
	// provider secrets and must not be logged by callers.
	ExtraHeaders map[string]string
	// ExtraBody is merged into each Chat Completions request after OpenVibely-owned
	// fields are set. Protected fields are ignored.
	ExtraBody map[string]interface{}
	// ToolExecutor overrides tool execution. It should return (output, isError, err).
	// If nil, built-in local tool execution is used.
	ToolExecutor func(ctx context.Context, name string, input json.RawMessage) (string, bool, error)
	// ToolFilter can deny tool execution by name at runtime.
	ToolFilter func(name string) bool

	// Callbacks for real-time output
	OnText       func(text string)
	OnToolUse    func(name string, input json.RawMessage)
	OnToolResult func(name string, output string, isError bool)
}

// completionsMessage represents a message in the /v1/chat/completions format.
type completionsMessage struct {
	Role      string      `json:"role"`
	Content   interface{} `json:"content"` // string or array of content blocks
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// SendCompletions sends a message using the /v1/chat/completions API with tool use.
// This is the standard OpenAI API format (not Responses API).
func (c *Client) SendCompletions(ctx context.Context, prompt string, opts *CompletionsOptions) (*AgenticResponse, error) {
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
	if opts.Temperature == 0 {
		opts.Temperature = 0.7
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
		toolDefs = filterToolDefinitions(toolDefs, opts.ToolFilter)
	}

	tools := make([]map[string]interface{}, 0, len(toolDefs))
	for _, td := range toolDefs {
		tools = append(tools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        td.Name,
				"description": td.Description,
				"parameters":  td.Parameters,
			},
		})
	}

	// Build initial messages from history + new prompt
	messages := make([]completionsMessage, 0, len(c.History)+2)
	if opts.System != "" {
		messages = append(messages, completionsMessage{
			Role:    "system",
			Content: opts.System,
		})
	}
	for _, msg := range c.History {
		messages = append(messages, completionsMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// Add current prompt with optional attachments
	if len(opts.Attachments) > 0 {
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
						"url": fmt.Sprintf("data:%s;base64,%s", att.MediaType, base64.StdEncoding.EncodeToString(data)),
					},
				})
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
	} else {
		messages = append(messages, completionsMessage{
			Role:    "user",
			Content: prompt,
		})
	}

	result := &AgenticResponse{Model: opts.Model}
	var allText strings.Builder

	for turn := 0; turn < opts.MaxTurns; turn++ {
		turnResult, err := c.sendCompletionsTurn(ctx, messages, tools, opts)
		if err != nil {
			return nil, fmt.Errorf("turn %d: %w", turn+1, err)
		}

		result.InputTokens += turnResult.inputTokens
		result.OutputTokens += turnResult.outputTokens
		result.TotalTokens += turnResult.totalTokens
		result.CachedInputTokens += turnResult.cachedInputTokens
		result.ReasoningTokens += turnResult.reasoningTokens
		result.StopReason = turnResult.stopReason
		if turnResult.model != "" {
			result.Model = turnResult.model
		}

		allText.WriteString(turnResult.text)

		// Add assistant message to history
		if len(turnResult.toolCalls) > 0 {
			messages = append(messages, completionsMessage{
				Role:      "assistant",
				Content:   turnResult.text,
				ToolCalls: turnResult.toolCalls,
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

		// Execute tools and add results
		for _, tc := range turnResult.toolCalls {
			inputJSON := json.RawMessage(tc.Function.Arguments)
			if opts.OnToolUse != nil {
				opts.OnToolUse(tc.Function.Name, inputJSON)
			}

			applog.Infof("[openai-completions] executing tool %s", tc.Function.Name)
			output := ""
			isError := false
			var err error
			if opts.ToolFilter != nil && !opts.ToolFilter(tc.Function.Name) {
				isError = true
				output = fmt.Sprintf("tool %s is not allowed by this agent", tc.Function.Name)
			} else if opts.ToolExecutor != nil {
				output, isError, err = opts.ToolExecutor(ctx, tc.Function.Name, inputJSON)
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

			// Add tool result message
			messages = append(messages, completionsMessage{
				Role:       "tool",
				Content:    output,
				ToolCallID: tc.ID,
			})
		}
	}

	result.Text = allText.String()

	// Update client history
	c.History = append(c.History, Message{Role: "user", Content: prompt})
	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})

	return result, nil
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
	text      string
	toolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	stopReason        string
	model             string
	inputTokens       int
	outputTokens      int
	totalTokens       int
	cachedInputTokens int
	reasoningTokens   int
}

func (c *Client) sendCompletionsTurn(ctx context.Context, messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) (*completionsTurnResult, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[openai-client] chat completions stream error before output, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	return httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*completionsTurnResult, bool, error) {
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
}

func (c *Client) sendCompletionsTurnOnce(ctx context.Context, messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) (*completionsTurnResult, error) {
	payload := map[string]interface{}{
		"model":       opts.Model,
		"messages":    messages,
		"stream":      true,
		"temperature": opts.Temperature,
	}

	if opts.MaxOutputTokens > 0 {
		payload["max_tokens"] = opts.MaxOutputTokens
	}

	if len(tools) > 0 {
		payload["tools"] = tools
		payload["tool_choice"] = "auto"
	}
	mergeCompletionsExtraBody(payload, opts.ExtraBody)

	body, err := json.Marshal(payload)
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

	result, err := c.parseCompletionsStream(resp.Body, opts.OnText)
	if err != nil {
		return result, httpretry.NewStreamError(err)
	}
	return result, nil
}

func (c *Client) parseCompletionsStream(body io.Reader, onText func(string)) (*completionsTurnResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	result := &completionsTurnResult{}
	var textBuilder strings.Builder
	terminal := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			terminal = true
			continue
		}
		if data == "" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if model, ok := chunk["model"].(string); ok && result.model == "" {
			result.model = model
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}

		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		// Handle text content
		if content, ok := delta["content"].(string); ok && content != "" {
			textBuilder.WriteString(content)
			if onText != nil {
				onText(content)
			}
		}

		// Handle tool calls
		if toolCalls, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}

				idx := len(result.toolCalls)
				if rawIndex, ok := tcMap["index"].(float64); ok && rawIndex >= 0 {
					idx = int(rawIndex)
				}
				for len(result.toolCalls) <= idx {
					result.toolCalls = append(result.toolCalls, struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					}{})
				}

				if id, ok := tcMap["id"].(string); ok {
					result.toolCalls[idx].ID = id
				}
				if typ, ok := tcMap["type"].(string); ok {
					result.toolCalls[idx].Type = typ
				}
				if fn, ok := tcMap["function"].(map[string]interface{}); ok {
					if name, ok := fn["name"].(string); ok {
						result.toolCalls[idx].Function.Name = name
					}
					if args, ok := fn["arguments"].(string); ok {
						result.toolCalls[idx].Function.Arguments += args
					}
				}
			}
		}

		// Handle finish reason
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
			result.stopReason = reason
			terminal = true
		}

		// Handle usage (appears in last chunk)
		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				result.inputTokens = int(prompt)
			}
			if completion, ok := usage["completion_tokens"].(float64); ok {
				result.outputTokens = int(completion)
			}
			if total, ok := usage["total_tokens"].(float64); ok {
				result.totalTokens = int(total)
			}
			if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
				if cached, ok := details["cached_tokens"].(float64); ok {
					result.cachedInputTokens = int(cached)
				}
			}
			if details, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
				if reasoning, ok := details["reasoning_tokens"].(float64); ok {
					result.reasoningTokens = int(reasoning)
				}
			}
			if reasoning, ok := usage["reasoning_tokens"].(float64); ok {
				result.reasoningTokens = int(reasoning)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !terminal {
		return nil, io.ErrUnexpectedEOF
	}

	result.text = textBuilder.String()
	if result.stopReason == "" {
		result.stopReason = "stop"
	}

	return result, nil
}
