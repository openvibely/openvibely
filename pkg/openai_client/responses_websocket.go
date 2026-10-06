package openaiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

var errResponsesWebsocketTransport = errors.New("Responses websocket transport error")
var errResponsesWebsocketStale = errors.New("Responses websocket stale connection")

func isRetryableResponsesTransportError(err error) bool {
	return errors.Is(err, errResponsesWebsocketTransport) || errors.Is(err, errResponsesWebsocketStale)
}

// ResponsesTransportState holds connection/fallback state that may be shared
// by short-lived clients for the same configured model.
type ResponsesTransportState struct {
	websocketDisabled     atomic.Bool
	sessionID             string
	mu                    sync.Mutex
	conn                  *websocket.Conn
	lastProperties        string
	lastBaseline          []any
	lastResponseID        string
	astraRequestEffort    string
	astraConfiguredEffort string
	astraConfigUpdates    []astraConfigurationUpdate
}

type astraConfigurationUpdate struct {
	UserHistoryIndex int    `json:"user_history_index"`
	Effort           string `json:"effort"`
}

type astraReasoningSessionState struct {
	Version          int                        `json:"version"`
	Model            string                     `json:"model"`
	RequestEffort    string                     `json:"request_effort"`
	ConfiguredEffort string                     `json:"configured_effort"`
	Updates          []astraConfigurationUpdate `json:"updates,omitempty"`
}

func NewResponsesTransportState() *ResponsesTransportState {
	return &ResponsesTransportState{sessionID: newSessionID()}
}

// Close releases any WebSocket owned by this transport state.
func (s *ResponsesTransportState) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetConnectionLocked()
}

func (s *ResponsesTransportState) disableWebsocket() {
	s.websocketDisabled.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetConnectionLocked()
}

func (s *ResponsesTransportState) resetConnectionLocked() {
	if s.conn != nil {
		_ = s.conn.CloseNow()
	}
	s.conn = nil
	s.lastProperties = ""
	s.lastBaseline = nil
	s.lastResponseID = ""
}

func shouldFallbackResponsesWebsocket(ctx context.Context, err error) bool {
	// An explicitly unsupported upgrade can fall back immediately. Transient
	// connection and stream failures must exhaust WebSocket retries first.
	var responseErr *httpretry.ResponseError
	return err != nil && ctx.Err() == nil && errors.Is(err, errResponsesWebsocketTransport) &&
		errors.As(err, &responseErr) && responseErr.StatusCode == http.StatusUpgradeRequired
}

// doResponsesStreamTurn gives WebSocket its full retry budget before switching
// the session to HTTP, which then gets its own retry budget (as in Codex).
func doResponsesStreamTurn[T any](ctx context.Context, c *Client, _ string, policy httpretry.StreamTurnPolicy, fn func(context.Context) (T, error)) (T, error) {
	state := c.responsesTransportState
	result, err := httpretry.DoStreamTurn(ctx, policy, fn)
	// Recovery failures end the turn even when their underlying cause is a
	// retryable transport error. HTTP fallback must not resume generation.
	if llmcontracts.ErrorIs(err, llmcontracts.ErrorMidTurnCompactionFailed) {
		return result, err
	}
	if err == nil || ctx.Err() != nil || !c.supportsResponsesWebsockets || state.websocketDisabled.Load() ||
		!(isRetryableResponsesTransportError(err) || httpretry.IsRetryableError(err)) {
		return result, err
	}
	state.disableWebsocket()
	return httpretry.DoStreamTurn(ctx, policy, fn)
}

const (
	openAIResponsesWebsocketBeta = "responses_websockets=2026-02-06"
	responsesLiteMetadataKey     = "ws_request_header_x_openai_internal_codex_responses_lite"
	responsesWebsocketReadLimit  = 64 << 20
)

func isResponsesLiteWebsocketModel(model string) bool {
	spec, ok := models.LookupModel(models.ProviderOpenAI, model)
	return ok && spec.ResponsesLiteWebsocket
}

func isGPT6WorkflowModel(model string) bool {
	spec, ok := models.LookupModel(models.ProviderOpenAI, model)
	return ok && spec.GPT6Workflow
}

func responsesLiteDefaultReasoningEffort(model string) string {
	if spec, ok := models.LookupModel(models.ProviderOpenAI, model); ok && spec.DefaultReasoningEffort != "" {
		return spec.DefaultReasoningEffort
	}
	return "medium"
}

func (s *ResponsesTransportState) lastAstraReasoningEffort(model string) string {
	_, configured, _ := s.astraReasoningState(model)
	return configured
}

func (s *ResponsesTransportState) astraReasoningState(model string) (string, string, []astraConfigurationUpdate) {
	if s == nil || !isGPT6WorkflowModel(model) {
		return "", "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updates := append([]astraConfigurationUpdate(nil), s.astraConfigUpdates...)
	return s.astraRequestEffort, s.astraConfiguredEffort, updates
}

func (s *ResponsesTransportState) astraReasoningStateJSON(model string) string {
	requestEffort, configuredEffort, updates := s.astraReasoningState(model)
	if requestEffort == "" || configuredEffort == "" {
		return ""
	}
	raw, err := json.Marshal(astraReasoningSessionState{
		Version: 1, Model: strings.TrimSpace(model), RequestEffort: requestEffort,
		ConfiguredEffort: configuredEffort, Updates: updates,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

func (s *ResponsesTransportState) restoreAstraReasoningStateJSON(model, raw string) error {
	if s == nil || !isGPT6WorkflowModel(model) || strings.TrimSpace(raw) == "" {
		return nil
	}
	var restored astraReasoningSessionState
	if err := json.Unmarshal([]byte(raw), &restored); err != nil {
		return fmt.Errorf("decode Astra reasoning session state: %w", err)
	}
	if restored.Version != 1 || !strings.EqualFold(strings.TrimSpace(restored.Model), strings.TrimSpace(model)) {
		return fmt.Errorf("Astra reasoning session state is incompatible with model %q", model)
	}
	requestEffort := normalizeReasoningEffort(restored.RequestEffort)
	configuredEffort := normalizeReasoningEffort(restored.ConfiguredEffort)
	if requestEffort == "" || configuredEffort == "" {
		return errors.New("Astra reasoning session state contains an invalid effort")
	}
	updates := make([]astraConfigurationUpdate, len(restored.Updates))
	for i, update := range restored.Updates {
		update.Effort = normalizeReasoningEffort(update.Effort)
		if update.UserHistoryIndex < 0 || update.Effort == "" {
			return errors.New("Astra reasoning session state contains an invalid configuration update")
		}
		updates[i] = update
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A live transport is authoritative. Durable state is only a cold-start
	// recovery mechanism and must not rewind a newer in-memory session.
	if s.astraRequestEffort != "" || s.astraConfiguredEffort != "" {
		return nil
	}
	s.astraRequestEffort = requestEffort
	s.astraConfiguredEffort = configuredEffort
	s.astraConfigUpdates = updates
	return nil
}

func (s *ResponsesTransportState) setAstraReasoningEffort(model, effort string) {
	if s == nil || !isGPT6WorkflowModel(model) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	effort = strings.TrimSpace(effort)
	if s.astraRequestEffort == "" {
		s.astraRequestEffort = effort
	}
	s.astraConfiguredEffort = effort
}

func (s *ResponsesTransportState) commitAstraReasoningEffort(model, effort string, userHistoryIndex int, changed, compacted bool) {
	if s == nil || !isGPT6WorkflowModel(model) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	effort = strings.TrimSpace(effort)
	if s.astraRequestEffort == "" {
		s.astraRequestEffort = effort
	}
	if compacted {
		s.astraConfigUpdates = nil
	}
	if changed && !compacted {
		s.astraConfigUpdates = append(s.astraConfigUpdates, astraConfigurationUpdate{
			UserHistoryIndex: userHistoryIndex,
			Effort:           effort,
		})
	}
	s.astraConfiguredEffort = effort
}

func responsesLiteTools(tools any) []any {
	if tools == nil {
		return []any{}
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return []any{}
	}
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return []any{}
	}
	filtered := decoded[:0]
	for _, raw := range decoded {
		tool, _ := raw.(map[string]any)
		switch strings.ToLower(strings.TrimSpace(stringFromAny(tool["type"]))) {
		case "web_search", "web_search_preview", "image_generation":
			continue
		default:
			// Responses Lite does not implement the standard Responses async
			// tool protocol. Never forward async metadata into a Lite request.
			delete(tool, "async")
			filtered = append(filtered, raw)
		}
	}
	return filtered
}

func responsesToolsContainHostedTool(tools any) bool {
	encoded, err := json.Marshal(tools)
	if err != nil {
		return false
	}
	var decoded []any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return false
	}
	for _, raw := range decoded {
		tool, _ := raw.(map[string]any)
		switch strings.ToLower(strings.TrimSpace(stringFromAny(tool["type"]))) {
		case "web_search", "web_search_preview", "image_generation":
			return true
		}
	}
	return false
}

func filterResponsesLiteImageDetails(value any, model string) {
	supportsAutoDetail := isResponsesLiteWebsocketModel(model)
	switch current := value.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(stringFromAny(current["type"])), "input_image") {
			if !supportsAutoDetail || stringFromAny(current["detail"]) != "auto" {
				delete(current, "detail")
			}
		}
		for _, nested := range current {
			filterResponsesLiteImageDetails(nested, model)
		}
	case []any:
		for _, nested := range current {
			filterResponsesLiteImageDetails(nested, model)
		}
	}
}

func (c *Client) responsesWebsocketEndpoint(isChatGPTOAuth bool) (string, error) {
	base := strings.TrimSpace(OpenAIAPIBaseURL)
	if isChatGPTOAuth {
		base = strings.TrimSpace(OpenAIChatGPTAPIBaseURL)
	}
	if base == "" {
		return "", fmt.Errorf("missing OpenAI base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse OpenAI base URL %q: %w", base, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("OpenAI base URL must use http, https, ws, or wss")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/responses"
	return u.String(), nil
}

func applyDefaultResponsesTextVerbosity(payload map[string]any, model string) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(normalized, "gpt-5") && !strings.HasPrefix(normalized, "gpt-6") {
		return
	}
	text, _ := payload["text"].(map[string]any)
	if text == nil {
		text = map[string]any{}
		payload["text"] = text
	}
	if _, exists := text["verbosity"]; !exists {
		text["verbosity"] = "low"
	}
}

func buildResponsesLiteWebsocketPayload(payload map[string]any, system, sessionID string) map[string]any {
	request := make(map[string]any, len(payload)+6)
	for key, value := range payload {
		request[key] = value
	}

	input, _ := request["input"].([]any)
	filterResponsesLiteImageDetails(input, stringFromAny(request["model"]))
	prefix := make([]any, 0, 2)
	prefix = append(prefix, map[string]any{
		"type":  "additional_tools",
		"role":  "developer",
		"tools": responsesLiteTools(request["tools"]),
	})
	if strings.TrimSpace(system) != "" {
		prefix = append(prefix, map[string]any{
			"type": "message",
			"role": "developer",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": system,
			}},
		})
	}
	request["input"] = append(prefix, input...)
	delete(request, "instructions")
	delete(request, "tools")

	reasoning, _ := request["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{"effort": responsesLiteDefaultReasoningEffort(stringFromAny(request["model"]))}
	}
	reasoning["context"] = "all_turns"
	request["reasoning"] = reasoning
	request["type"] = "response.create"
	request["store"] = false
	request["stream"] = true
	request["tool_choice"] = "auto"
	request["parallel_tool_calls"] = false
	request["include"] = []string{"reasoning.encrypted_content"}
	delete(request, "max_output_tokens")
	delete(request, "truncation")
	request["client_metadata"] = map[string]string{
		responsesLiteMetadataKey: "true",
		"session_id":             sessionID,
	}
	return request
}

// buildStandardResponsesWebsocketPayload preserves the public Responses API
// request shape and adds only the WebSocket event envelope. OpenAI WebSocket
// mode does not use HTTP transport fields such as stream or background.
func buildStandardResponsesWebsocketPayload(payload map[string]any) map[string]any {
	request := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		request[key] = value
	}
	request["type"] = "response.create"
	delete(request, "stream")
	delete(request, "background")
	return request
}

type responsesWebsocketStreamOptions struct {
	Model string
}

func (c *Client) openResponsesWebsocketStream(ctx context.Context, payload map[string]any, isChatGPTOAuth bool, opts responsesWebsocketStreamOptions) (io.ReadCloser, error) {
	state := c.responsesTransportState
	state.mu.Lock()
	endpoint, err := c.responsesWebsocketEndpoint(isChatGPTOAuth)
	if err != nil {
		state.mu.Unlock()
		return nil, err
	}

	dial := func() (*websocket.Conn, *http.Response, error) {
		headers := http.Header{}
		req, reqErr := http.NewRequest(http.MethodGet, endpoint, nil)
		if reqErr != nil {
			return nil, nil, reqErr
		}
		c.applyAuthHeaders(req, isChatGPTOAuth)
		for key, values := range req.Header {
			headers[key] = append([]string(nil), values...)
		}
		headers.Set("OpenAI-Beta", openAIResponsesWebsocketBeta)
		headers.Set("session-id", c.sessionID)
		headers.Set("thread-id", c.sessionID)
		conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
			HTTPClient:      c.httpClient,
			HTTPHeader:      headers,
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err == nil {
			// Match Codex's maximum complete WebSocket message size. The
			// coder/websocket default is only 32 KiB, which is too small for
			// Responses events containing large tool output or completion data.
			conn.SetReadLimit(responsesWebsocketReadLimit)
		}
		return conn, resp, err
	}

	connect := func() (*websocket.Conn, error) {
		tokenUsed := c.auth.Token
		conn, resp, connectErr := dial()
		if connectErr != nil && isChatGPTOAuth && resp != nil && resp.StatusCode == http.StatusUnauthorized && c.oauthUnauthorizedHandler != nil {
			tokens, recovered, recoverErr := c.oauthUnauthorizedHandler(ctx, tokenUsed)
			if (recovered || recoverErr != nil) && resp.Body != nil {
				resp.Body.Close()
			}
			if recoverErr != nil {
				return nil, recoverErr
			}
			if recovered {
				c.applyOAuthTokens(tokens)
				conn, resp, connectErr = dial()
			}
		}
		if connectErr == nil {
			return conn, nil
		}
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			providerErr := fmt.Errorf("connect %q: %w", endpoint, parseAPIError(resp.StatusCode, body))
			// A rejected handshake is not necessarily a broken transport. Keep
			// terminal authentication/request errors out of reconnect and fallback.
			if httpretry.IsRetryableStatus(resp.StatusCode) || resp.StatusCode == http.StatusUpgradeRequired {
				providerErr = fmt.Errorf("%w: %w", errResponsesWebsocketTransport, providerErr)
			}
			return nil, httpretry.NewResponseError(resp, providerErr)
		}
		return nil, fmt.Errorf("%w: connect %q: %w", errResponsesWebsocketTransport, endpoint, connectErr)
	}

	conn := state.conn
	reusedConnection := conn != nil
	if conn == nil {
		conn, err = connect()
	}
	if err != nil {
		state.mu.Unlock()
		return nil, err
	}
	state.conn = conn

	wirePayload, fullInput, properties := incrementalResponsesWebsocketPayload(payload, state)

	body, err := json.Marshal(wirePayload)
	if err != nil {
		state.resetConnectionLocked()
		state.mu.Unlock()
		return nil, fmt.Errorf("marshal websocket request: %w", err)
	}
	writeErr := conn.Write(ctx, websocket.MessageText, body)
	if writeErr != nil {
		state.resetConnectionLocked()
		state.mu.Unlock()
		if reusedConnection {
			return nil, fmt.Errorf("%w: send request: %w", errResponsesWebsocketStale, writeErr)
		}
		return nil, fmt.Errorf("%w: send request: %w", errResponsesWebsocketTransport, writeErr)
	}

	reader, writer := io.Pipe()
	go func() {
		defer func() {
			state.mu.Unlock()
			_ = writer.Close()
		}()
		var outputItems []any
		responseID := ""
		receivedFrame := false
		forwardEventData := func(data []byte) bool {
			// A WebSocket frame is a complete JSON event and may contain line
			// breaks (notably provider error envelopes). Our downstream SSE
			// parsers consume one data line, so compact before framing it.
			var compact bytes.Buffer
			if err := json.Compact(&compact, data); err != nil {
				state.resetConnectionLocked()
				writer.CloseWithError(fmt.Errorf("invalid Responses websocket JSON event: %w", err))
				return false
			}
			data = compact.Bytes()
			if _, writeErr := fmt.Fprintf(writer, "data: %s\n\n", data); writeErr != nil {
				state.resetConnectionLocked()
				return false
			}
			return true
		}
		recordCompletedResponse := func(id string) {
			state.lastProperties = properties
			state.lastBaseline = append(append([]any(nil), fullInput...), outputItems...)
			state.lastResponseID = id
		}
		for {
			messageType, data, readErr := conn.Read(ctx)
			if readErr != nil {
				state.resetConnectionLocked()
				kind := errResponsesWebsocketTransport
				if reusedConnection && !receivedFrame {
					kind = errResponsesWebsocketStale
				}
				writer.CloseWithError(fmt.Errorf("%w: read response: %w", kind, readErr))
				return
			}
			receivedFrame = true
			if messageType != websocket.MessageText {
				state.resetConnectionLocked()
				writer.CloseWithError(fmt.Errorf("%w: unexpected binary frame", errResponsesWebsocketTransport))
				return
			}
			var event map[string]any
			if json.Unmarshal(data, &event) == nil {
				eventType := stringFromAny(event["type"])
				if eventType == "error" {
					providerErr := responsesStreamTerminalError(eventType, event)
					var apiErr *APIError
					if errors.As(providerErr, &apiErr) && (apiErr.Code == "previous_response_not_found" || apiErr.Code == "websocket_connection_limit_reached") {
						// Reconnect and replay the full transcript, without disabling WebSocket.
						state.resetConnectionLocked()
						writer.CloseWithError(fmt.Errorf("%w: %w", errResponsesWebsocketStale, providerErr))
						return
					}
				}
				if eventType == "response.output_item.done" {
					if item, ok := event["item"].(map[string]any); ok {
						outputItems = append(outputItems, item)
					}
				}
				if !forwardEventData(data) {
					return
				}
				if isTerminalResponsesWebsocketEvent(eventType) {
					if eventType == "response.completed" {
						responseID = responseIDFromEvent(event)
						recordCompletedResponse(responseID)
					}
					return
				}
				continue
			}
			if !forwardEventData(data) {
				return
			}
		}
	}()
	return reader, nil
}

func responseIDFromEvent(event map[string]any) string {
	if event == nil {
		return ""
	}
	if response, ok := event["response"].(map[string]any); ok {
		if id := strings.TrimSpace(stringFromAny(response["id"])); id != "" {
			return id
		}
	}
	return strings.TrimSpace(firstNonEmpty(stringFromAny(event["response_id"]), stringFromAny(event["id"])))
}

func incrementalResponsesWebsocketPayload(payload map[string]any, state *ResponsesTransportState) (map[string]any, []any, string) {
	wire := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		wire[key] = value
	}
	fullInput, _ := payload["input"].([]any)
	propertiesPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		if key != "input" && key != "client_metadata" && key != "previous_response_id" {
			propertiesPayload[key] = value
		}
	}
	encoded, _ := json.Marshal(propertiesPayload)
	properties := string(encoded)
	if state.lastResponseID != "" && state.lastProperties == properties && hasInputPrefix(fullInput, state.lastBaseline) {
		wire["input"] = append([]any(nil), fullInput[len(state.lastBaseline):]...)
		wire["previous_response_id"] = state.lastResponseID
	}
	return wire, append([]any(nil), fullInput...), properties
}

func hasInputPrefix(input, prefix []any) bool {
	if len(input) < len(prefix) {
		return false
	}
	for i := range prefix {
		if !reflect.DeepEqual(input[i], prefix[i]) {
			return false
		}
	}
	return true
}

func (c *Client) openResponsesHTTPStream(ctx context.Context, websocketPayload map[string]any, isChatGPTOAuth, responsesLite bool) (io.ReadCloser, error) {
	payload := make(map[string]any, len(websocketPayload))
	for key, value := range websocketPayload {
		payload[key] = value
	}
	delete(payload, "type")
	delete(payload, "client_metadata")
	payload["stream"] = true
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Responses HTTP request: %w", err)
	}
	endpoint, err := c.responsesEndpoint(isChatGPTOAuth)
	if err != nil {
		return nil, err
	}
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.applyAuthHeaders(req, isChatGPTOAuth)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if responsesLite {
			req.Header.Set("x-openai-internal-codex-responses-lite", "true")
		}
		return req, nil
	}
	resp, err := c.doWithOAuthRecovery(ctx, endpoint, isChatGPTOAuth, buildReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(resp.Body)
		return nil, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %w", endpoint, parseAPIError(resp.StatusCode, errBody)))
	}
	return resp.Body, nil
}

func (c *Client) openResponsesLiteHTTPStream(ctx context.Context, websocketPayload map[string]any, isChatGPTOAuth bool) (io.ReadCloser, error) {
	return c.openResponsesHTTPStream(ctx, websocketPayload, isChatGPTOAuth, true)
}

func isTerminalResponsesWebsocketEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.failed", "response.incomplete", "response.error", "error":
		return true
	default:
		return false
	}
}
