package geminiclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/streamdecode"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// SkipThoughtSignature is Google's documented placeholder for function calls
// whose signature was not produced by the current model (for example replay
// converted from another provider).
const SkipThoughtSignature = "skip_thought_signature_validator"

// UnauthorizedHandler refreshes OAuth credentials after a 401 response.
type UnauthorizedHandler func(ctx context.Context, tokenUsed string) (newToken string, recovered bool, err error)

type Client struct {
	httpClient    *http.Client
	apiKey        string
	accessToken   string
	project       string
	baseURL       string
	codeAssistURL string
	unauthorized  UnauthorizedHandler
	history       []Content
	lastExchange  []Content
}

// NewWithAPIKey uses the public Gemini API.
func NewWithAPIKey(apiKey string) *Client {
	return &Client{httpClient: httpclient.NewModel(nil), apiKey: apiKey, baseURL: DefaultAPIBaseURL}
}

// NewWithOAuth uses Gemini Code Assist with a Google account and project.
func NewWithOAuth(accessToken, project string) *Client {
	return &Client{httpClient: httpclient.NewModel(nil), accessToken: accessToken, project: project, codeAssistURL: CodeAssistBaseURL}
}

func (c *Client) SetHTTPClient(client *http.Client)            { c.httpClient = client }
func (c *Client) SetBaseURL(url string)                        { c.baseURL = strings.TrimRight(url, "/") }
func (c *Client) SetCodeAssistURL(url string)                  { c.codeAssistURL = strings.TrimRight(url, "/") }
func (c *Client) SetUnauthorizedHandler(h UnauthorizedHandler) { c.unauthorized = h }
func (c *Client) SetHistory(history []Content)                 { c.history = CloneContents(history) }
func (c *Client) History() []Content                           { return CloneContents(c.history) }
func (c *Client) isOAuth() bool                                { return c.apiKey == "" }
func (c *Client) LastExchange() []Content                      { return CloneContents(c.lastExchange) }
func (c *Client) setLastExchange(items []Content, prefix []Content) {
	c.lastExchange = append(CloneContents(prefix), CloneContents(items)...)
}

// Options configures one agentic exchange.
type Options struct {
	Model           string
	System          string
	MaxOutputTokens int
	ContextWindow   int
	// Temperature is omitted when nil.
	Temperature              *float64
	WorkDir                  string
	MaxTurns                 int
	ContinueFromHistory      bool
	OmitExecutionEnvironment bool
	DisableTools             bool
	SkipDefaultTools         bool
	ToolOutputTokenLimit     int
	Attachments              []Attachment
	ExtraTools               []ToolDefinition
	ToolExecutor             func(ctx context.Context, name string, input json.RawMessage) (string, bool, error)
	ToolFilter               func(name string) bool
	// OnReplayChanged saves the exchange snapshot and the first changed index.
	OnReplayChanged        func(context.Context, []Content, int) error
	OnToolBoundarySteering func(context.Context) (string, error)
	OnText                 func(string)
	OnToolUse              func(name string, input json.RawMessage)
	OnToolResult           func(name, output string, isError bool)
}

// DefaultTools returns the shared local coding tools.
func DefaultTools() []ToolDefinition {
	defs := agenttools.Definitions()
	out := make([]ToolDefinition, 0, len(defs))
	for _, d := range defs {
		out = append(out, ToolDefinition{Name: d.Name, Description: d.Description, Schema: d.Schema})
	}
	return out
}

// ExecuteTool runs a shared local coding tool.
func ExecuteTool(ctx context.Context, workDir, name string, input json.RawMessage) (string, error) {
	return agenttools.Execute(ctx, workDir, name, input)
}

func readContent(c Content) (string, string) {
	role := c.Role
	if role == "model" {
		role = "assistant"
	}
	return role, contentText(c)
}

func makeUserText(text string) Content { return TextContent("user", text) }

// Send runs the model/tool loop until the model stops calling tools.
func (c *Client) Send(ctx context.Context, prompt string, opts *Options) (*Response, error) {
	c.lastExchange = nil
	if opts == nil {
		opts = &Options{}
	}
	if opts.MaxOutputTokens <= 0 {
		opts.MaxOutputTokens = 16384
	}
	if opts.MaxTurns <= 0 {
		opts.MaxTurns = math.MaxInt32
	}
	if opts.WorkDir == "" {
		opts.WorkDir = "."
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, fmt.Errorf("gemini model is required")
	}

	var toolDefs []ToolDefinition
	if !opts.DisableTools {
		if !opts.SkipDefaultTools {
			toolDefs = append(toolDefs, DefaultTools()...)
		}
		toolDefs = append(toolDefs, opts.ExtraTools...)
	}

	history := CloneContents(c.history)
	var environmentUpdate []Content
	if !opts.OmitExecutionEnvironment {
		before := len(history)
		history = executionenv.Ensure(history, opts.WorkDir, readContent, makeUserText)
		if len(history) != before {
			environmentUpdate = executionenv.Ensure(nil, opts.WorkDir, readContent, makeUserText)
		}
	}
	contents := append([]Content(nil), history...)

	var exchange transcript.ExchangeTracker[Content]
	exchange.Append(environmentUpdate...)
	environmentCount := len(environmentUpdate)
	if !opts.ContinueFromHistory {
		user := Content{Role: "user"}
		if prompt != "" {
			user.Parts = append(user.Parts, Part{Text: prompt})
		}
		for _, att := range opts.Attachments {
			part, err := attachmentPart(att)
			if err != nil {
				return nil, err
			}
			user.Parts = append(user.Parts, part)
		}
		if len(user.Parts) == 0 {
			user.Parts = []Part{{Text: " "}}
		}
		contents = append(contents, user)
		exchange.Append(user)
	}
	if !opts.OmitExecutionEnvironment {
		contents = executionenv.Ensure(contents, opts.WorkDir, readContent, makeUserText)
	}

	saveReplay := func(saveCtx context.Context) error {
		if opts.OnReplayChanged == nil {
			return nil
		}
		err := opts.OnReplayChanged(saveCtx, CloneContents(exchange.Items()), exchange.FirstChanged())
		if err == nil {
			exchange.Committed()
		}
		return err
	}
	defer func() {
		c.history = append(history, CloneContents(exchange.Items()[environmentCount:])...)
		c.setLastExchange(exchange.Items()[environmentCount:], environmentUpdate)
	}()

	result := &Response{Model: opts.Model}
	var allText strings.Builder
	for turn := 0; turn < opts.MaxTurns; turn++ {
		currentTranscript := exchange.Items()[environmentCount:]
		beforeContext := len(currentTranscript)
		currentTranscript = managedcontext.SeedReplay(ctx, contents, currentTranscript, readContent, makeUserText)
		contents = managedcontext.Ensure(ctx, contents, readContent, makeUserText)
		currentTranscript = attachmentcontext.SeedReplay(ctx, contents, currentTranscript, readContent, makeUserText)
		contents = attachmentcontext.Ensure(ctx, contents, readContent, makeUserText)
		if len(currentTranscript) != beforeContext {
			exchange.ReplaceTail(environmentCount+max(0, beforeContext-1), currentTranscript[max(0, beforeContext-1):]...)
		}

		body, err := c.buildRequestBody(contents, toolDefs, opts)
		if err != nil {
			return nil, err
		}
		if _, summarizing := llmcontracts.SummarySystemPromptFromContext(ctx); !summarizing {
			if err := ensureRequestFits(body, opts); err != nil {
				return nil, err
			}
		}

		var partial strings.Builder
		turnResult, err := httpretry.DoStreamTurn(ctx, httpretry.StreamTurnPolicy{
			OnRetry: func(event httpretry.RetryEvent) {
				applog.Infof("[gemini] retrying turn %d, attempt %d/%d in %v: %v", turn+1, event.Attempt, event.MaxRetries, event.Delay, event.Err)
			},
		}, func(attemptCtx context.Context) (*turnResult, error) {
			partial.Reset()
			return c.sendTurn(attemptCtx, body, opts, &partial)
		})
		if err != nil {
			if text := partial.String(); text != "" {
				exchange.Append(TextContent("model", text))
			}
			return nil, categorizeError(fmt.Errorf("turn %d: %w", turn+1, err))
		}

		result.InputTokens += turnResult.inputTokens
		result.OutputTokens += turnResult.outputTokens
		result.TotalTokens += turnResult.totalTokens
		result.CachedInputTokens += turnResult.cachedTokens
		result.ReasoningTokens += turnResult.thoughtTokens
		result.LastContextTokens = 0
		if turnResult.inputTokens > 0 {
			result.LastContextTokens = turnResult.inputTokens + turnResult.outputTokens
		}
		result.StopReason = turnResult.finishReason
		if turnResult.model != "" {
			result.Model = turnResult.model
		}
		allText.WriteString(turnResult.text)

		modelContent := Content{Role: "model", Parts: turnResult.parts}
		if len(modelContent.Parts) == 0 {
			modelContent.Parts = []Part{{Text: ""}}
		}
		contents = append(contents, modelContent)
		exchange.Append(modelContent)
		if err := saveReplay(ctx); err != nil {
			return nil, err
		}

		calls := turnResult.calls()
		if len(calls) == 0 {
			break
		}

		requestInputIndex := exclusiveRequestUserInput(calls)
		response := Content{Role: "user"}
		var images []Part
		for i, call := range calls {
			input := call.Args
			if len(input) == 0 || bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
				input = json.RawMessage(`{}`)
			}
			if opts.OnToolUse != nil {
				opts.OnToolUse(call.Name, input)
			}
			output, isError := "", false
			normalized, validationErr := validateArguments(toolDefs, call.Name, input)
			switch {
			case validationErr != nil:
				output, isError = validationErr.Error(), true
			case requestInputIndex >= 0 && i != requestInputIndex:
				output, isError = "tool call rejected: request_user_input must complete in its own model turn", true
			case opts.ToolFilter != nil && !opts.ToolFilter(call.Name):
				output, isError = fmt.Sprintf("tool %s is not allowed by this agent", call.Name), true
			case opts.ToolExecutor != nil:
				var execErr error
				output, isError, execErr = opts.ToolExecutor(llmcontracts.WithToolCallID(ctx, callID(call, turn, i)), call.Name, normalized)
				if execErr != nil {
					output, isError = execErr.Error(), true
				}
			default:
				var execErr error
				output, execErr = ExecuteTool(ctx, opts.WorkDir, call.Name, normalized)
				if execErr != nil {
					output, isError = execErr.Error(), true
				}
			}
			if opts.OnToolResult != nil {
				opts.OnToolResult(call.Name, agenttools.ImageResultSummary(output), isError)
			}
			var inputMap map[string]any
			_ = json.Unmarshal(normalized, &inputMap)
			result.ToolCalls = append(result.ToolCalls, ToolCall{Name: call.Name, Input: inputMap, Output: output, Error: isError})

			modelOutput, outputImages := toolOutputForModel(output, opts.ToolOutputTokenLimit)
			images = append(images, outputImages...)
			key := "output"
			if isError {
				key = "error"
			}
			response.Parts = append(response.Parts, Part{FunctionResponse: &FunctionResponse{
				ID: call.ID, Name: call.Name, Response: map[string]any{key: modelOutput},
			}})
			if image, ok := attachment.TakeToolImage(ctx, output); ok && !isError {
				images = append(images, Part{Text: "Image returned by view_image: " + image.Name},
					Part{InlineData: &InlineData{MimeType: image.MediaType, Data: base64.StdEncoding.EncodeToString(image.Data)}})
			}
		}
		response.Parts = append(response.Parts, images...)
		contents = append(contents, response)
		exchange.Append(response)
		if opts.OnReplayChanged != nil {
			if err := saveReplay(context.WithoutCancel(ctx)); err != nil {
				applog.Infof("[gemini] intermediate tool transcript save failed; continuing with in-memory history: %v", err)
			}
		}
		if opts.OnToolBoundarySteering != nil {
			steering, err := opts.OnToolBoundarySteering(ctx)
			if err != nil {
				return nil, fmt.Errorf("tool-boundary steering: %w", err)
			}
			if steering = strings.TrimSpace(steering); steering != "" {
				msg := TextContent("user", steering)
				contents = append(contents, msg)
				exchange.Append(msg)
			}
		}
	}
	result.Text = allText.String()
	return result, nil
}

func callID(call FunctionCall, turn, index int) string {
	if call.ID != "" {
		return call.ID
	}
	return fmt.Sprintf("gemini_call_%d_%d", turn, index)
}

func exclusiveRequestUserInput(calls []FunctionCall) int {
	if len(calls) < 2 {
		return -1
	}
	for i, call := range calls {
		if call.Name == "request_user_input" {
			return i
		}
	}
	return -1
}

func validateArguments(defs []ToolDefinition, name string, input json.RawMessage) (json.RawMessage, error) {
	for _, def := range defs {
		if (def.Name == name || def.Name == "shell" && (name == "bash" || name == "Bash")) && (def.Schema != nil || len(def.Parameters) > 0) {
			return toolschema.ValidateArguments(def.Schema, def.Parameters, input)
		}
	}
	return input, nil
}

func toolOutputForModel(output string, limit int) (string, []Part) {
	if result, ok := agenttools.DecodeImageResult(output); ok {
		parts := []Part{{Text: "Current project computer screenshot. Inspect before continuing."}}
		for _, img := range result.Content {
			parts = append(parts, Part{InlineData: &InlineData{MimeType: img.MIME, Data: img.Data}})
		}
		return "Project computer screenshot captured.", parts
	}
	return tokenestimate.ToolOutputForModelInput(output, limit), nil
}

func attachmentPart(att Attachment) (Part, error) {
	media := strings.ToLower(strings.TrimSpace(att.MediaType))
	switch {
	case strings.HasPrefix(media, "image/"), media == "application/pdf":
		return Part{InlineData: &InlineData{MimeType: media, Data: base64.StdEncoding.EncodeToString(att.Data)}}, nil
	case attachment.IsTextMediaType(media):
		return Part{Text: fmt.Sprintf("--- File: %s ---\n%s\n--- End of %s ---", att.FileName, string(att.Data), att.FileName)}, nil
	default:
		return Part{}, fmt.Errorf("unsupported Gemini attachment type %q for %s", att.MediaType, att.FileName)
	}
}

// wireContents merges adjacent same-role turns and supplies the documented
// placeholder signature for replayed function calls that lack one.
func wireContents(contents []Content) []Content {
	out := make([]Content, 0, len(contents))
	for _, c := range contents {
		if len(c.Parts) == 0 {
			continue
		}
		parts := append([]Part(nil), c.Parts...)
		if n := len(out); n > 0 && out[n-1].Role == c.Role {
			out[n-1].Parts = append(out[n-1].Parts, parts...)
			continue
		}
		out = append(out, Content{Role: c.Role, Parts: parts})
	}
	for i := range out {
		if out[i].Role != "model" {
			continue
		}
		for j := range out[i].Parts {
			if out[i].Parts[j].FunctionCall != nil {
				if out[i].Parts[j].ThoughtSignature == "" {
					out[i].Parts[j].ThoughtSignature = SkipThoughtSignature
				}
				break
			}
		}
	}
	return out
}

type functionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type generateRequest struct {
	Contents          []Content        `json:"contents"`
	SystemInstruction *Content         `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolConfig        map[string]any   `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
}

func (c *Client) buildRequestBody(contents []Content, toolDefs []ToolDefinition, opts *Options) ([]byte, error) {
	req := generateRequest{Contents: wireContents(contents)}
	if strings.TrimSpace(opts.System) != "" {
		req.SystemInstruction = &Content{Parts: []Part{{Text: opts.System}}}
	}
	var decls []functionDeclaration
	for _, def := range toolDefs {
		if opts.ToolFilter != nil && !opts.ToolFilter(def.Name) {
			continue
		}
		decls = append(decls, functionDeclaration{Name: def.Name, Description: def.Description, ParametersJSONSchema: def.parameterSchema()})
	}
	if len(decls) > 0 {
		req.Tools = []map[string]any{{"functionDeclarations": decls}}
		req.ToolConfig = map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
	}
	gen := map[string]any{"maxOutputTokens": opts.MaxOutputTokens}
	if opts.Temperature != nil {
		gen["temperature"] = *opts.Temperature
	}
	req.GenerationConfig = gen
	if !c.isOAuth() {
		return json.Marshal(req)
	}
	return json.Marshal(map[string]any{"model": opts.Model, "project": c.project, "request": req})
}

func ensureRequestFits(body []byte, opts *Options) error {
	window := opts.ContextWindow
	if window <= 0 {
		window = 1048576
	}
	safe := window - opts.MaxOutputTokens - max(1024, window/50)
	tokens := tokenestimate.FromByteCount(len(body))
	if tokens <= safe {
		return nil
	}
	return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Gemini preflight",
		fmt.Errorf("complete request requires %d input tokens; safe limit is %d", tokens, safe))
}

func (c *Client) endpoint(model string) string {
	if c.isOAuth() {
		return c.codeAssistURL + ":streamGenerateContent?alt=sse"
	}
	return c.baseURL + "/models/" + model + ":streamGenerateContent?alt=sse"
}

func (c *Client) sendTurn(ctx context.Context, body []byte, opts *Options, partial *strings.Builder) (*turnResult, error) {
	endpoint := c.endpoint(opts.Model)
	tokenUsed := c.accessToken
	resp, err := c.post(ctx, endpoint, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && c.isOAuth() && c.unauthorized != nil {
		resp.Body.Close()
		token, recovered, recoverErr := c.unauthorized(ctx, tokenUsed)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if !recovered {
			return nil, fmt.Errorf("gemini: 401 Unauthorized: OAuth recovery did not refresh token")
		}
		c.accessToken = token
		if resp, err = c.post(ctx, endpoint, body); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, httpretry.NewResponseError(resp, &APIError{StatusCode: resp.StatusCode, Message: boundedErrorText(raw)})
	}
	result, err := parseStream(resp.Body, opts.OnText, partial)
	if err != nil {
		return nil, httpretry.NewStreamError(err)
	}
	return result, nil
}

func (c *Client) post(ctx context.Context, endpoint string, body []byte) (*http.Response, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[gemini] request retry %d/%d in %v (status %d): %v", event.Attempt, event.MaxRetries, event.Delay, event.StatusCode, event.Err)
	}
	return httpretry.Do(ctx, c.httpClient, func() (*http.Request, error) {
		if err := llmcontracts.CheckBeforeModelSend(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.isOAuth() {
			req.Header.Set("Authorization", "Bearer "+c.accessToken)
		} else {
			req.Header.Set("x-goog-api-key", c.apiKey)
		}
		return req, nil
	}, policy)
}

// APIError is a non-2xx Gemini response with a bounded message.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini API error: HTTP %d: %s", e.StatusCode, e.Message)
}

func categorizeError(err error) error {
	var categorized *llmcontracts.CategorizedError
	if errors.As(err, &categorized) {
		return err
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
		msg := strings.ToLower(apiErr.Message)
		if strings.Contains(msg, "token count") || strings.Contains(msg, "exceeds the maximum number of tokens") || strings.Contains(msg, "context length") {
			return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "Gemini provider request", err)
		}
	}
	if httpretry.IsRetryableNetworkError(err) {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorTransportFailure, "Gemini provider transport", err)
	}
	return err
}

type turnResult struct {
	parts         []Part
	text          string
	finishReason  string
	model         string
	inputTokens   int
	outputTokens  int
	totalTokens   int
	cachedTokens  int
	thoughtTokens int
}

func (r *turnResult) calls() []FunctionCall {
	var out []FunctionCall
	for _, p := range r.parts {
		if p.FunctionCall != nil {
			out = append(out, *p.FunctionCall)
		}
	}
	return out
}

type streamChunk struct {
	Candidates []struct {
		Content      *Content `json:"content"`
		FinishReason string   `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
		ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	ModelVersion   string `json:"modelVersion"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type codeAssistChunk struct {
	Response *streamChunk `json:"response"`
}

func parseStream(body io.Reader, onText func(string), partial *strings.Builder) (*turnResult, error) {
	scanner := streamdecode.Scanner(body, 32*1024*1024)
	result := &turnResult{}
	var text strings.Builder
	pendingSignature := ""
	terminal := false
	appendPart := func(p Part) {
		if p.ThoughtSignature == "" && pendingSignature != "" {
			p.ThoughtSignature = pendingSignature
			pendingSignature = ""
		}
		result.parts = append(result.parts, p)
	}
	for scanner.Scan() {
		data, ok := streamdecode.Data(scanner.Bytes(), false)
		if !ok || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var chunk streamChunk
		var wrapped codeAssistChunk
		if json.Unmarshal(data, &wrapped) == nil && wrapped.Response != nil {
			chunk = *wrapped.Response
		} else if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return nil, &APIError{StatusCode: chunk.Error.Code, Message: chunk.Error.Message}
		}
		if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" && len(chunk.Candidates) == 0 {
			return nil, fmt.Errorf("gemini blocked the prompt: %s", chunk.PromptFeedback.BlockReason)
		}
		if chunk.ModelVersion != "" {
			result.model = chunk.ModelVersion
		}
		if u := chunk.UsageMetadata; u != nil {
			result.inputTokens = u.PromptTokenCount
			result.outputTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
			result.thoughtTokens = u.ThoughtsTokenCount
			result.cachedTokens = u.CachedContentTokenCount
			result.totalTokens = u.TotalTokenCount
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		cand := chunk.Candidates[0]
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				switch {
				case p.Thought:
					if p.ThoughtSignature != "" {
						pendingSignature = p.ThoughtSignature
					}
				case p.FunctionCall != nil:
					appendPart(Part{FunctionCall: p.FunctionCall, ThoughtSignature: p.ThoughtSignature})
				case p.Text != "":
					text.WriteString(p.Text)
					if partial != nil {
						partial.WriteString(p.Text)
					}
					if onText != nil {
						onText(p.Text)
					}
					if n := len(result.parts); n > 0 && result.parts[n-1].FunctionCall == nil && result.parts[n-1].InlineData == nil && result.parts[n-1].ThoughtSignature == "" {
						result.parts[n-1].Text += p.Text
						result.parts[n-1].ThoughtSignature = p.ThoughtSignature
					} else {
						appendPart(Part{Text: p.Text, ThoughtSignature: p.ThoughtSignature})
					}
				case p.ThoughtSignature != "":
					if n := len(result.parts); n > 0 && result.parts[n-1].ThoughtSignature == "" {
						result.parts[n-1].ThoughtSignature = p.ThoughtSignature
					} else {
						pendingSignature = p.ThoughtSignature
					}
				}
			}
		}
		if cand.FinishReason != "" {
			result.finishReason = cand.FinishReason
			terminal = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !terminal {
		return nil, io.ErrUnexpectedEOF
	}
	if pendingSignature != "" {
		for i := len(result.parts) - 1; i >= 0; i-- {
			if result.parts[i].ThoughtSignature == "" {
				result.parts[i].ThoughtSignature = pendingSignature
				break
			}
		}
	}
	switch result.finishReason {
	case "STOP", "MAX_TOKENS", "FINISH_REASON_UNSPECIFIED", "":
	default:
		if len(result.parts) == 0 {
			return nil, fmt.Errorf("gemini response stopped: %s", result.finishReason)
		}
	}
	result.text = text.String()
	return result, nil
}
