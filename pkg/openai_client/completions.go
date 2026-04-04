package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
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
	Attachments     []*FileAttachment
	// ExtraTools are appended to the default local tools (for example MCP tools).
	ExtraTools []ToolDefinition
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

	if err := c.EnsureValidToken(); err != nil {
		return nil, err
	}

	var tools []map[string]interface{}
	if !opts.DisableTools {
		for _, td := range DefaultTools() {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        td.Name,
					"description": td.Description,
					"parameters":  td.Parameters,
				},
			})
		}
	}
	for _, td := range opts.ExtraTools {
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
		result.CachedInputTokens += turnResult.cachedInputTokens
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

			log.Printf("[openai-completions] executing tool %s", tc.Function.Name)
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
	cachedInputTokens int
}

func (c *Client) sendCompletionsTurn(ctx context.Context, messages []completionsMessage, tools []map[string]interface{}, opts *CompletionsOptions) (*completionsTurnResult, error) {
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
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := "https://api.openai.com/v1/chat/completions"
	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(httpReq, false)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}

	resp, err := doWithRetry(ctx, c.httpClient, buildReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(resp.Body)
		trimmed := strings.TrimSpace(string(errBody))
		if trimmed == "" {
			return nil, fmt.Errorf("POST %q: %d %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		return nil, fmt.Errorf("POST %q: %d %s %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode), trimmed)
	}

	return c.parseCompletionsStream(resp.Body, opts.OnText)
}

func (c *Client) parseCompletionsStream(body io.Reader, onText func(string)) (*completionsTurnResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	result := &completionsTurnResult{}
	var textBuilder strings.Builder

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
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

				idx := int(tcMap["index"].(float64))
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
		}

		// Handle usage (appears in last chunk)
		if usage, ok := chunk["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				result.inputTokens = int(prompt)
			}
			if completion, ok := usage["completion_tokens"].(float64); ok {
				result.outputTokens = int(completion)
			}
			// Note: standard completions API doesn't report cached tokens
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	result.text = textBuilder.String()
	if result.stopReason == "" {
		result.stopReason = "stop"
	}

	return result, nil
}
