package geminiclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
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
	legacyOAuth   bool
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

func (c *Client) SetLegacyOAuth(legacy bool) { c.legacyOAuth = legacy }

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
	ReasoningEffort string
	// MaxContinuationRequests bounds automatic continuation; zero defaults to eight.
	MaxContinuationRequests int
	System                  string
	MaxOutputTokens         int
	ContextWindow           int
	// Temperature is omitted when nil.
	Temperature              *float64
	WorkDir                  string
	MaxTurns                 int
	ContinueFromHistory      bool
	OmitExecutionEnvironment bool
	DisableTools             bool
	WebSearchEnabled         bool
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

	nativeSearch := opts.WebSearchEnabled && !opts.DisableTools && !opts.SkipDefaultTools && (opts.ToolFilter == nil || opts.ToolFilter("web_search"))
	for _, def := range toolDefs {
		if def.Name == "web_search" {
			nativeSearch = false
		}
	}
	if nativeSearch {
		toolDefs = append(toolDefs, webSearchDefinition())
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
	preparedRequest := &requestPreparation{}
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

		body, tokens, err := c.prepareRequest(contents, toolDefs, opts, preparedRequest)
		if err != nil {
			return nil, err
		}
		if _, summarizing := llmcontracts.SummarySystemPromptFromContext(ctx); !summarizing {
			if err := ensureTokensFit(tokens, opts); err != nil {
				return nil, err
			}
		}

		var partial streamdecode.Text
		turnResult, err := httpretry.DoStream(ctx, httpretry.Policy{
			AllowReplay: true,
			MaxRetries:  httpretry.DefaultMaxRetries,
			OnRetry: func(event httpretry.RetryEvent) {
				applog.Infof("[gemini] retrying turn %d, attempt %d/%d in %v: %v", turn+1, event.Attempt, event.MaxRetries, event.Delay, event.Err)
			},
		}, func(attemptCtx context.Context) (*turnResult, bool, error) {
			partial = streamdecode.Text{}
			result, err := c.sendTurn(attemptCtx, body, opts, &partial)
			return result, result != nil, err
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
		result.LastContextTokens = turnResult.lastContextTokens
		result.StopReason = turnResult.finishReason
		if turnResult.model != "" {
			result.Model = turnResult.model
		}
		allText.WriteString(turnResult.text)

		assignMissingCallIDs(turnResult.parts, turn)
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
			if note := earlyStopNote(turnResult.finishReason); note != "" {
				allText.WriteString(note)
				if opts.OnText != nil {
					opts.OnText(note)
				}
			}
			break
		}

		requestInputIndex := exclusiveRequestUserInput(calls)
		response := Content{Role: "user"}
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
			case call.Name == "web_search" && nativeSearch:
				search, searchErr := c.webSearch(ctx, opts.Model, normalized)
				if searchErr != nil {
					output, isError = searchErr.Error(), true
				} else {
					output = search.output
					result.InputTokens += search.usage.inputTokens
					result.OutputTokens += search.usage.outputTokens
					result.TotalTokens += search.usage.totalTokens
					result.CachedInputTokens += search.usage.cachedTokens
					result.ReasoningTokens += search.usage.thoughtTokens
				}
			case opts.ToolExecutor != nil:
				var execErr error
				output, isError, execErr = opts.ToolExecutor(llmcontracts.WithToolCallID(ctx, call.ID), call.Name, normalized)
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
			var responseMedia []FunctionResponsePart
			for _, part := range outputImages {
				if part.InlineData != nil {
					responseMedia = append(responseMedia, FunctionResponsePart{InlineData: part.InlineData})
				}
			}
			key := "output"
			if isError {
				key = "error"
			}
			response.Parts = append(response.Parts, Part{FunctionResponse: &FunctionResponse{
				ID: call.ID, Name: call.Name, Response: map[string]any{key: modelOutput}, Parts: responseMedia,
			}})
			if image, ok := attachment.TakeToolImage(ctx, output); ok && !isError {
				response.Parts[len(response.Parts)-1].FunctionResponse.Parts = append(response.Parts[len(response.Parts)-1].FunctionResponse.Parts, FunctionResponsePart{InlineData: &InlineData{MimeType: image.MediaType, Data: base64.StdEncoding.EncodeToString(image.Data)}})
			}
		}
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

// The ID is stored on the call so its saved result and later replays pair by ID.
func assignMissingCallIDs(parts []Part, turn int) {
	now := time.Now().UnixMilli()
	index := 0
	for _, p := range parts {
		if p.FunctionCall == nil {
			continue
		}
		if p.FunctionCall.ID == "" {
			p.FunctionCall.ID = fmt.Sprintf("gemini_call_%d_%d_%d", now, turn, index)
		}
		index++
	}
}

// Shown to the user only; not added to the model's history.
func earlyStopNote(reason string) string {
	switch reason {
	case "STOP", "MAX_TOKENS", "FINISH_REASON_UNSPECIFIED", "":
		return ""
	}
	return fmt.Sprintf("\n\n[Gemini stopped the response early: %s]", reason)
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
	ResponseJSONSchema   json.RawMessage `json:"responseJsonSchema,omitempty"`
}

type generateRequest struct {
	Contents          []Content        `json:"contents"`
	SystemInstruction *Content         `json:"systemInstruction,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolConfig        map[string]any   `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any   `json:"generationConfig,omitempty"`
}

func ensureTokensFit(tokens int, opts *Options) error {
	window := opts.ContextWindow
	if window <= 0 {
		window = 1048576
	}
	safe := window - opts.MaxOutputTokens - max(1024, window/50)
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

func (c *Client) sendTurn(ctx context.Context, body []byte, opts *Options, partial *streamdecode.Text) (*turnResult, error) {
	limit := opts.MaxContinuationRequests
	if limit <= 0 {
		limit = 8
	}
	combined := &turnResult{}
	seen := map[string]bool{}
	assembler := &callAssembler{}
	for step := 0; ; step++ {
		segment, err := c.sendSegment(ctx, body, opts, partial, combined, assembler)
		if err != nil {
			if !combined.observed && step == 0 {
				return nil, err
			}
			return combined, err
		}

		token := segment.continuationToken
		if token == "" || segment.finishReason != "MAX_TOKENS" {
			if len(assembler.pending) > 0 {
				return combined, fmt.Errorf("gemini incomplete streamed function arguments")
			}
			return combined, nil
		}
		if step >= limit || seen[token] {
			return combined, fmt.Errorf("gemini continuation limit or repeated token")
		}
		seen[token] = true
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			return combined, err
		}
		request := envelope
		if c.isOAuth() {
			var ok bool
			request, ok = envelope["request"].(map[string]any)
			if !ok {
				return combined, fmt.Errorf("gemini missing continuation request envelope")
			}
			envelope["requestId"] = newRequestID()
		}
		for _, key := range []string{"contents", "tools", "systemInstruction", "generationConfig"} {
			delete(request, key)
		}
		request["continuationToken"] = token
		body, err = json.Marshal(envelope)
		if err != nil {
			return combined, err
		}
	}
}

func (c *Client) sendSegment(ctx context.Context, body []byte, opts *Options, partial *streamdecode.Text, result *turnResult, assembler *callAssembler) (*turnResult, error) {
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
	err = parseStreamInto(resp.Body, opts.OnText, partial, result, assembler)
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
			if !c.legacyOAuth {
				req.Header.Set("User-Agent", codeAssistUserAgent())
			}
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
	lastContextTokens int
	observed          bool
	continuationToken string
	grounding         json.RawMessage
	parts             []Part
	text              string
	finishReason      string
	model             string
	inputTokens       int
	outputTokens      int
	totalTokens       int
	cachedTokens      int
	thoughtTokens     int
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
		Content           *Content        `json:"content"`
		FinishReason      string          `json:"finishReason"`
		ContinuationToken string          `json:"continuationToken"`
		GroundingMetadata json.RawMessage `json:"groundingMetadata"`
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

func parseStreamInto(body io.Reader, onText func(string), partial *streamdecode.Text, result *turnResult, assembler *callAssembler) error {
	scanner := streamdecode.Scanner(body, 32*1024*1024)
	result.continuationToken = ""
	result.finishReason = ""
	usage := &turnResult{}
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
		payload := data
		if err := jsonview.Object(data, func(key string, value json.RawMessage) error {
			if strings.EqualFold(key, "response") && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				payload = value
			}
			return nil
		}); err != nil {
			return fmt.Errorf("invalid Gemini stream JSON: %w", err)
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return fmt.Errorf("invalid Gemini stream JSON: %w", err)
		}

		if chunk.Error != nil {
			return &APIError{StatusCode: chunk.Error.Code, Message: chunk.Error.Message}
		}
		if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" && len(chunk.Candidates) == 0 {
			return fmt.Errorf("gemini blocked the prompt: %s", chunk.PromptFeedback.BlockReason)
		}
		if chunk.ModelVersion != "" {
			result.model = chunk.ModelVersion
		}
		if u := chunk.UsageMetadata; u != nil {
			usage.inputTokens = u.PromptTokenCount
			usage.outputTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
			usage.thoughtTokens = u.ThoughtsTokenCount
			usage.cachedTokens = u.CachedContentTokenCount
			usage.totalTokens = u.TotalTokenCount
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		cand := chunk.Candidates[0]
		if len(cand.GroundingMetadata) > 0 {
			result.grounding = append(json.RawMessage(nil), cand.GroundingMetadata...)
		}
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				result.observed = true
				switch {
				case p.Thought:
					if p.ThoughtSignature != "" {
						pendingSignature = p.ThoughtSignature
					}
				case p.FunctionCall != nil:
					if p.ThoughtSignature == "" {
						p.ThoughtSignature = pendingSignature
						pendingSignature = ""
					}
					if err := assembler.add(result, p); err != nil {
						return err
					}
				case p.Text != "":
					text.WriteString(p.Text)
					if partial != nil {
						partial.View(&text, text.String(), 0)
					}
					if onText != nil {
						onText(p.Text)
					}
					if n := len(result.parts); n > 0 && result.parts[n-1].FunctionCall == nil && result.parts[n-1].Text != "" && result.parts[n-1].ThoughtSignature == "" && p.ThoughtSignature == "" && pendingSignature == "" {
						result.parts[n-1].Text += p.Text
						result.parts[n-1].ThoughtSignature = p.ThoughtSignature
					} else {
						appendPart(Part{Text: p.Text, ThoughtSignature: p.ThoughtSignature})
					}
				case p.InlineData != nil || p.FileData != nil || len(p.ExecutableCode) > 0 || len(p.CodeExecutionResult) > 0:
					appendPart(p)
				case p.ThoughtSignature != "":
					if n := len(result.parts); n > 0 && result.parts[n-1].ThoughtSignature == "" {
						result.parts[n-1].ThoughtSignature = p.ThoughtSignature
					} else {
						pendingSignature = p.ThoughtSignature
					}
				}
			}
		}
		if cand.ContinuationToken != "" {
			result.continuationToken = cand.ContinuationToken
		}
		if cand.FinishReason != "" {
			result.finishReason = cand.FinishReason
			terminal = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !terminal {
		return io.ErrUnexpectedEOF
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
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL":
		if len(result.calls()) == 0 {
			return fmt.Errorf("gemini response stopped: %s", result.finishReason)
		}
	default:
		if len(result.calls()) == 0 && text.Len() == 0 {
			return fmt.Errorf("gemini response stopped: %s", result.finishReason)
		}
	}
	result.text += text.String()
	// Each stream leg reports its own usage; repeated metadata within a leg replaces it.
	result.inputTokens += usage.inputTokens
	result.outputTokens += usage.outputTokens
	result.thoughtTokens += usage.thoughtTokens
	result.cachedTokens += usage.cachedTokens
	result.totalTokens += usage.totalTokens
	if usage.inputTokens > 0 {
		result.lastContextTokens = usage.inputTokens + usage.outputTokens
	}
	return nil
}

func newRequestID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return "ov-" + hex.EncodeToString(id[:])
}

func codeAssistUserAgent() string {
	return "\x61\x6e\x74\x69\x67\x72\x61\x76\x69\x74\x79/1.5.14 (" + runtime.GOOS + "; " + runtime.GOARCH + ") OpenVibely"
}

func thinkingLevel(model, effort string) (string, error) {
	switch effort {
	case "low", "medium", "high":
		return strings.ToUpper(effort), nil
	case "xhigh":
		if model == "gemini-4-neon" || model == "gemini-4-hydrogen" {
			return "XHIGH", nil
		}
	}
	return "", fmt.Errorf("unsupported Gemini reasoning effort %q for %s", effort, model)
}
