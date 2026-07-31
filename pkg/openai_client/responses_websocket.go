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
)

var errResponsesWebsocketTransport = errors.New("Responses websocket transport error")
var errResponsesWebsocketStale = errors.New("Responses websocket stale connection")

func isRetryableResponsesTransportError(err error) bool {
	return errors.Is(err, errResponsesWebsocketTransport) || errors.Is(err, errResponsesWebsocketStale)
}

// ResponsesTransportState holds connection/fallback state that may be shared
// by short-lived clients for the same configured model.
type ResponsesTransportState struct {
	websocketDisabled atomic.Bool
	sessionID         string
	mu                sync.Mutex
	conn              *websocket.Conn
	lastProperties    string
	lastBaseline      []any
	lastResponseID    string
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
	return err != nil && ctx.Err() == nil && errors.Is(err, errResponsesWebsocketTransport)
}

const (
	openAIResponsesWebsocketBeta = "responses_websockets=2026-02-06"
	responsesLiteMetadataKey     = "ws_request_header_x_openai_internal_codex_responses_lite"
)

func isResponsesLiteWebsocketModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	default:
		return false
	}
}

func responsesLiteDefaultReasoningEffort(model string) string {
	return "medium"
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
			filtered = append(filtered, raw)
		}
	}
	return filtered
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

func (c *Client) openResponsesWebsocketStream(ctx context.Context, payload map[string]any, isChatGPTOAuth bool) (io.ReadCloser, error) {
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
		return websocket.Dial(ctx, endpoint, &websocket.DialOptions{
			HTTPClient:      c.httpClient,
			HTTPHeader:      headers,
			CompressionMode: websocket.CompressionContextTakeover,
		})
	}

	connect := func() (*websocket.Conn, error) {
		tokenUsed := c.auth.Token
		conn, resp, connectErr := dial()
		if connectErr != nil && isChatGPTOAuth && resp != nil && resp.StatusCode == http.StatusUnauthorized && c.oauthUnauthorizedHandler != nil {
			if resp.Body != nil {
				resp.Body.Close()
			}
			tokens, recovered, recoverErr := c.oauthUnauthorizedHandler(ctx, tokenUsed)
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
			return nil, httpretry.NewResponseError(resp, fmt.Errorf("%w: connect %q: %d %s %s", errResponsesWebsocketTransport, endpoint, resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body))))
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
		defer state.mu.Unlock()
		defer writer.Close()
		var outputItems []any
		responseID := ""
		receivedFrame := false
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
			if _, writeErr := fmt.Fprintf(writer, "data: %s\n\n", data); writeErr != nil {
				state.resetConnectionLocked()
				return
			}
			var event map[string]any
			if json.Unmarshal(data, &event) == nil {
				eventType := stringFromAny(event["type"])
				if eventType == "response.output_item.done" {
					if item, ok := event["item"].(map[string]any); ok {
						outputItems = append(outputItems, item)
					}
				}
				if isTerminalResponsesWebsocketEvent(eventType) {
					if eventType == "response.completed" {
						if response, ok := event["response"].(map[string]any); ok {
							responseID = stringFromAny(response["id"])
						}
						state.lastProperties = properties
						state.lastBaseline = append(append([]any(nil), fullInput...), outputItems...)
						state.lastResponseID = responseID
					}
					return
				}
			}
		}
	}()
	return reader, nil
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

func (c *Client) openResponsesLiteHTTPStream(ctx context.Context, websocketPayload map[string]any, isChatGPTOAuth bool) (io.ReadCloser, error) {
	payload := make(map[string]any, len(websocketPayload))
	for key, value := range websocketPayload {
		payload[key] = value
	}
	delete(payload, "type")
	delete(payload, "client_metadata")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Responses Lite HTTP request: %w", err)
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
		req.Header.Set("x-openai-internal-codex-responses-lite", "true")
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

func isTerminalResponsesWebsocketEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.failed", "response.incomplete", "response.error", "error":
		return true
	default:
		return false
	}
}
