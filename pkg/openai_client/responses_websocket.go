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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpretry"
)

var errResponsesWebsocketTransport = errors.New("Responses websocket transport error")
var errResponsesWebsocketStale = errors.New("Responses websocket stale connection")

var astraSteeringAckTimeout = 5 * time.Second

func isRetryableResponsesTransportError(err error) bool {
	return errors.Is(err, errResponsesWebsocketTransport) || errors.Is(err, errResponsesWebsocketStale)
}

// ResponsesTransportState holds connection/fallback state that may be shared
// by short-lived clients for the same configured model.
type ResponsesTransportState struct {
	websocketDisabled      atomic.Bool
	sessionID              string
	mu                     sync.Mutex
	conn                   *websocket.Conn
	lastProperties         string
	lastBaseline           []any
	lastResponseID         string
	astraRequestEffort     string
	astraConfiguredEffort  string
	astraConfigUpdates     []astraConfigurationUpdate
	pendingAstraSteering   map[string]ResponsesSteeringDelivery
	astraSteeringFailures  []ResponsesSteeringDelivery
	astraSteeringCommits   []ResponsesSteeringDelivery
	astraSteeringAmbiguous []ResponsesSteeringDelivery
	steeringRecordsMu      sync.Mutex
	steeringRecords        []ResponsesSteeringDelivery
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

type ResponsesSteeringDelivery struct {
	Status             AstraSteeringDeliveryStatus
	SteeringID         string
	ResponseID         string
	PreviousResponseID string
	Error              string
}

type pendingSteeringAck struct {
	previousResponseID string
	steeringID         string
	accepted           bool
	acceptedCh         chan struct{}
	ch                 chan AstraSteeringDelivery
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
	s.markAllPendingAstraSteeringAmbiguousLocked()
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
func doResponsesStreamTurn[T any](ctx context.Context, c *Client, model string, policy httpretry.StreamTurnPolicy, fn func(context.Context) (T, error)) (T, error) {
	result, err := httpretry.DoStreamTurn(ctx, policy, fn)
	state := c.responsesTransportState
	if err == nil || ctx.Err() != nil || !isResponsesLiteWebsocketModel(model) || state.websocketDisabled.Load() || state.hasAstraSteeringAmbiguous() ||
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
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	default:
		return false
	}
}

func isGPT6WorkflowModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna":
		return true
	default:
		return false
	}
}

func responsesLiteDefaultReasoningEffort(model string) string {
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

func (s *ResponsesTransportState) takeAstraSteeringFailure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.astraSteeringFailures) == 0 {
		return nil
	}
	delivery := s.astraSteeringFailures[0]
	s.astraSteeringFailures = s.astraSteeringFailures[1:]
	err := fmt.Errorf("astra steering %s failed after being queued: %s", delivery.SteeringID, firstNonEmpty(delivery.Error, "provider rejected queued steering"))
	return &astraSteeringFailedError{err: err, ids: steeringDeliveryIDs([]ResponsesSteeringDelivery{delivery})}
}

func (s *ResponsesTransportState) takeUnresolvedAstraSteering() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pendingAstraSteering) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.pendingAstraSteering))
	for id := range s.pendingAstraSteering {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// response.steer.accepted transfers ownership to the server, but only a
	// successor response confirms application. Preserve an unresolved delivery
	// as ambiguous so callers neither replay it nor mark it applied.
	for id, pending := range s.pendingAstraSteering {
		pending.Status = AstraSteeringAmbiguous
		s.appendAstraSteeringAmbiguousLocked(pending)
		delete(s.pendingAstraSteering, id)
	}
	s.pendingAstraSteering = nil
	return fmt.Errorf("astra steering remained pending without a successor response: %s", strings.Join(ids, ", "))
}

func (s *ResponsesTransportState) takeAstraSteeringCommits() []ResponsesSteeringDelivery {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	commits := append([]ResponsesSteeringDelivery(nil), s.astraSteeringCommits...)
	s.astraSteeringCommits = nil
	return commits
}

func (s *ResponsesTransportState) takeAstraSteeringAmbiguous() []ResponsesSteeringDelivery {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ambiguous := append([]ResponsesSteeringDelivery(nil), s.astraSteeringAmbiguous...)
	s.astraSteeringAmbiguous = nil
	return ambiguous
}

func (s *ResponsesTransportState) hasAstraSteeringAmbiguous() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.astraSteeringAmbiguous) > 0
}

func (s *ResponsesTransportState) clearAstraSteeringCommits() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.astraSteeringCommits = nil
}

func (s *ResponsesTransportState) recordSteeringDelivery(record ResponsesSteeringDelivery) {
	if s == nil {
		return
	}
	s.steeringRecordsMu.Lock()
	defer s.steeringRecordsMu.Unlock()
	s.steeringRecords = append(s.steeringRecords, record)
}

func (s *ResponsesTransportState) SteeringDeliveries() []ResponsesSteeringDelivery {
	if s == nil {
		return nil
	}
	s.steeringRecordsMu.Lock()
	defer s.steeringRecordsMu.Unlock()
	out := make([]ResponsesSteeringDelivery, len(s.steeringRecords))
	copy(out, s.steeringRecords)
	return out
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
	Model                 string
	OnMidTurnSteering     AstraMidTurnSteeringCallback
	MidTurnSteeringWakeup <-chan struct{}
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
	requestPreviousResponseID := strings.TrimSpace(stringFromAny(wirePayload["previous_response_id"]))

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
	callbackErrors := make(chan error, 1)
	var steeringMu sync.Mutex
	activeResponseID := ""
	primaryResponseID := ""
	acceptedSteering := false
	pendingAcks := []*pendingSteeringAck{}
	streamDone := make(chan struct{})
	responseStarted := make(chan struct{})
	var responseStartedOnce sync.Once
	var steeringCallbackDone <-chan struct{}
	var cancelSteeringCallback context.CancelFunc
	writeFrame := func(writeCtx context.Context, payload map[string]any) error {
		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return conn.Write(writeCtx, websocket.MessageText, body)
	}
	deliverSteering := func(deliverCtx context.Context, text string) (AstraSteeringDelivery, error) {
		if !isGPT6WorkflowModel(opts.Model) || opts.OnMidTurnSteering == nil {
			return AstraSteeringDelivery{Status: AstraSteeringUnavailable}, nil
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return AstraSteeringDelivery{Status: AstraSteeringUnavailable}, nil
		}
		select {
		case <-streamDone:
			return AstraSteeringDelivery{Status: AstraSteeringUnavailable}, nil
		default:
		}
		steeringMu.Lock()
		select {
		case <-streamDone:
			steeringMu.Unlock()
			return AstraSteeringDelivery{Status: AstraSteeringUnavailable}, nil
		default:
		}
		previousID := activeResponseID
		if previousID == "" {
			steeringMu.Unlock()
			return AstraSteeringDelivery{Status: AstraSteeringUnavailable}, nil
		}
		ack := &pendingSteeringAck{
			previousResponseID: previousID,
			acceptedCh:         make(chan struct{}),
			ch:                 make(chan AstraSteeringDelivery, 1),
		}
		pendingAcks = append(pendingAcks, ack)

		event := map[string]any{
			"type":                 "response.steer",
			"previous_response_id": previousID,
			"input":                text,
		}
		if err := writeFrame(deliverCtx, event); err != nil {
			removePendingSteeringAckLocked(&pendingAcks, ack)
			steeringMu.Unlock()
			delivery := AstraSteeringDelivery{Status: AstraSteeringAmbiguous, PreviousResponseID: previousID, Error: err.Error()}
			state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
			return delivery, nil
		}
		state.recordSteeringDelivery(ResponsesSteeringDelivery{Status: AstraSteeringDelivered, PreviousResponseID: previousID})
		steeringMu.Unlock()
		timer := time.NewTimer(astraSteeringAckTimeout)
		defer timer.Stop()
		select {
		case delivery := <-ack.ch:
			return delivery, nil
		case <-ack.acceptedCh:
			// Acceptance transfers ownership to the server, but the input is not
			// committed until a successor response is created. A pending event is an
			// intermediate state that lets the local tool loop continue.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			select {
			case delivery := <-ack.ch:
				return delivery, nil
			case <-streamDone:
				select {
				case delivery := <-ack.ch:
					return delivery, nil
				default:
				}
				removePendingSteeringAck(&steeringMu, &pendingAcks, ack)
				delivery := ResponsesSteeringDelivery{
					Status:             AstraSteeringAmbiguous,
					SteeringID:         ack.steeringID,
					PreviousResponseID: previousID,
				}
				state.appendAstraSteeringAmbiguousLocked(delivery)
				state.recordSteeringDelivery(delivery)
				return AstraSteeringDelivery(delivery), nil
			}
		case <-deliverCtx.Done():
			removePendingSteeringAck(&steeringMu, &pendingAcks, ack)
			delivery := AstraSteeringDelivery{Status: AstraSteeringAmbiguous, PreviousResponseID: previousID, Error: deliverCtx.Err().Error()}
			state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
			return delivery, nil
		case <-streamDone:
			select {
			case delivery := <-ack.ch:
				return delivery, nil
			default:
			}
			removePendingSteeringAck(&steeringMu, &pendingAcks, ack)
			delivery := AstraSteeringDelivery{Status: AstraSteeringAmbiguous, PreviousResponseID: previousID, Error: "response.steer acknowledgement unavailable after stream closed"}
			state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
			return delivery, nil
		case <-timer.C:
			removePendingSteeringAck(&steeringMu, &pendingAcks, ack)
			delivery := AstraSteeringDelivery{Status: AstraSteeringAmbiguous, PreviousResponseID: previousID, Error: "response.steer acknowledgement timed out"}
			state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
			return delivery, nil
		}
	}
	if isGPT6WorkflowModel(opts.Model) && opts.OnMidTurnSteering != nil {
		steeringCallbackCtx, cancelCallback := context.WithCancel(ctx)
		cancelSteeringCallback = cancelCallback
		done := make(chan struct{})
		steeringCallbackDone = done
		go func() {
			defer close(done)
			fallbackInterval := 100 * time.Millisecond
			if opts.MidTurnSteeringWakeup != nil {
				fallbackInterval = 2 * time.Second
			}
			ticker := time.NewTicker(fallbackInterval)
			defer ticker.Stop()
			runCallback := func() bool {
				if err := opts.OnMidTurnSteering(steeringCallbackCtx, deliverSteering); err != nil {
					select {
					case <-streamDone:
						return false
					default:
					}
					if steeringCallbackCtx.Err() != nil {
						return false
					}
					state.recordSteeringDelivery(ResponsesSteeringDelivery{Status: AstraSteeringFailed, Error: err.Error()})
					callbackErrors <- fmt.Errorf("persisting Astra steering delivery: %w", err)
					_ = conn.CloseNow()
					return false
				}
				return true
			}
			runCallbackWhenReady := func() bool {
				select {
				case <-responseStarted:
				case <-steeringCallbackCtx.Done():
					return false
				case <-streamDone:
					return false
				}
				return runCallback()
			}
			for {
				select {
				case <-steeringCallbackCtx.Done():
					return
				case <-streamDone:
					return
				case <-opts.MidTurnSteeringWakeup:
					if !runCallbackWhenReady() {
						return
					}
				case <-ticker.C:
					if !runCallbackWhenReady() {
						return
					}
				}
			}
		}()
	}
	go func() {
		defer func() {
			close(streamDone)
			if cancelSteeringCallback != nil {
				cancelSteeringCallback()
			}
			if steeringCallbackDone != nil {
				<-steeringCallbackDone
			}
			state.mu.Unlock()
			_ = writer.Close()
		}()
		var outputItems []any
		responseID := ""
		var deferredPrimaryCompleted []byte
		deferredPrimaryCompletedID := ""
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
				select {
				case callbackErr := <-callbackErrors:
					state.resetConnectionLocked()
					writer.CloseWithError(callbackErr)
					return
				default:
				}
				if len(deferredPrimaryCompleted) > 0 {
					if forwardEventData(deferredPrimaryCompleted) {
						recordCompletedResponse(deferredPrimaryCompletedID)
					}
					return
				}
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
				if eventType == "response.created" {
					if response, ok := event["response"].(map[string]any); ok {
						id := stringFromAny(response["id"])
						successorResponse := false
						steeringMu.Lock()
						previousActiveResponseID := activeResponseID
						activeResponseID = id
						if id != "" {
							responseStartedOnce.Do(func() { close(responseStarted) })
						}
						if primaryResponseID == "" {
							primaryResponseID = id
						} else if acceptedSteering && id != "" && id != primaryResponseID {
							successorResponse = true
						}
						committed := commitAcceptedSteeringLocked(&pendingAcks, previousActiveResponseID, id, "")
						persistedCommitted := state.commitPendingAstraSteeringLocked(requestPreviousResponseID, id)
						steeringMu.Unlock()
						for _, delivery := range committed {
							state.appendAstraSteeringCommitLocked(ResponsesSteeringDelivery(delivery))
							state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
						}
						for _, delivery := range persistedCommitted {
							state.appendAstraSteeringCommitLocked(delivery)
							state.recordSteeringDelivery(delivery)
						}
						if successorResponse {
							deferredPrimaryCompleted = nil
							deferredPrimaryCompletedID = ""
						}
					}
				}
				if eventType == "response.steer.accepted" || eventType == "response.steer.failed" {
					delivery := steeringDeliveryFromEvent(eventType, event)
					if delivery.Status == AstraSteeringFailed {
						state.failPendingAstraSteeringLocked(delivery)
					}
					recordAndHandlePendingSteeringAck(state, &steeringMu, &pendingAcks, delivery)
					if delivery.Status == AstraSteeringAccepted {
						acceptedSteering = true
					} else {
						steeringMu.Lock()
						acceptedSteering = hasAcceptedSteeringLocked(pendingAcks)
						steeringMu.Unlock()
					}
				}
				if eventType == "response.output_item.done" {
					if item, ok := event["item"].(map[string]any); ok {
						outputItems = append(outputItems, item)
					}
				}
				if eventType == "response.steer.pending" {
					delivery := steeringDeliveryFromEvent(eventType, event)
					steeringMu.Lock()
					pendingDeliveries := pendAcceptedSteeringLocked(&pendingAcks, delivery)
					for _, pendingDelivery := range pendingDeliveries {
						state.addPendingAstraSteeringLocked(pendingDelivery)
					}
					steeringMu.Unlock()
					for _, pendingDelivery := range pendingDeliveries {
						state.recordSteeringDelivery(ResponsesSteeringDelivery(pendingDelivery))
					}
					if len(deferredPrimaryCompleted) > 0 {
						if forwardEventData(deferredPrimaryCompleted) {
							recordCompletedResponse(deferredPrimaryCompletedID)
						}
						return
					}
					continue
				}
				if eventType == "response.completed" && acceptedSteering {
					completedID := responseIDFromEvent(event)
					if completedID != "" && completedID == primaryResponseID {
						deferredPrimaryCompleted = append(deferredPrimaryCompleted[:0], data...)
						deferredPrimaryCompletedID = completedID
						continue
					}
				}
				if !shouldForwardResponsesWebsocketEvent(eventType, event) {
					continue
				}
				if !forwardEventData(data) {
					return
				}
				if isTerminalResponsesWebsocketEvent(eventType) {
					if eventType == "response.incomplete" && responseIncompleteReason(event) == "steered" {
						continue
					}
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

func removePendingSteeringAck(mu *sync.Mutex, pending *[]*pendingSteeringAck, target *pendingSteeringAck) {
	if mu == nil || pending == nil || target == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	removePendingSteeringAckLocked(pending, target)
}

func removePendingSteeringAckLocked(pending *[]*pendingSteeringAck, target *pendingSteeringAck) {
	if pending == nil || target == nil {
		return
	}
	items := *pending
	for i, ack := range items {
		if ack == target {
			copy(items[i:], items[i+1:])
			items[len(items)-1] = nil
			*pending = items[:len(items)-1]
			return
		}
	}
}

func recordAndHandlePendingSteeringAck(state *ResponsesTransportState, mu *sync.Mutex, pending *[]*pendingSteeringAck, delivery AstraSteeringDelivery) bool {
	if state == nil || mu == nil || pending == nil {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	// The sender holds mu until it has recorded a successful frame write. This
	// keeps a fast acknowledgement from appearing before the delivered record.
	state.recordSteeringDelivery(ResponsesSteeringDelivery(delivery))
	items := *pending
	for i, ack := range items {
		if ack == nil {
			continue
		}
		if delivery.SteeringID != "" && ack.steeringID != "" && delivery.SteeringID != ack.steeringID {
			continue
		}
		if delivery.PreviousResponseID != "" && ack.previousResponseID != "" && delivery.PreviousResponseID != ack.previousResponseID {
			continue
		}
		if delivery.Status == AstraSteeringAccepted {
			ack.steeringID = delivery.SteeringID
			if !ack.accepted {
				ack.accepted = true
				close(ack.acceptedCh)
			}
			return false
		}
		copy(items[i:], items[i+1:])
		items[len(items)-1] = nil
		*pending = items[:len(items)-1]
		select {
		case ack.ch <- delivery:
		default:
		}
		return true
	}
	return false
}

func commitAcceptedSteeringLocked(pending *[]*pendingSteeringAck, previousResponseID, responseID, steeringID string) []AstraSteeringDelivery {
	if pending == nil {
		return nil
	}
	items := *pending
	committed := make([]AstraSteeringDelivery, 0, len(items))
	retained := items[:0]
	for _, ack := range items {
		if ack == nil || !ack.accepted ||
			(steeringID != "" && ack.steeringID != steeringID) ||
			(previousResponseID != "" && ack.previousResponseID != previousResponseID) {
			retained = append(retained, ack)
			continue
		}
		delivery := AstraSteeringDelivery{
			Status:             AstraSteeringAccepted,
			SteeringID:         ack.steeringID,
			ResponseID:         responseID,
			PreviousResponseID: ack.previousResponseID,
		}
		committed = append(committed, delivery)
		select {
		case ack.ch <- delivery:
		default:
		}
	}
	for i := len(retained); i < len(items); i++ {
		items[i] = nil
	}
	*pending = retained
	return committed
}

func pendAcceptedSteeringLocked(pending *[]*pendingSteeringAck, delivery AstraSteeringDelivery) []AstraSteeringDelivery {
	if pending == nil {
		return nil
	}
	items := *pending
	pendingDeliveries := make([]AstraSteeringDelivery, 0, len(items))
	retained := items[:0]
	for _, ack := range items {
		if ack == nil || !ack.accepted ||
			(delivery.SteeringID != "" && ack.steeringID != delivery.SteeringID) ||
			(delivery.PreviousResponseID != "" && ack.previousResponseID != delivery.PreviousResponseID) {
			retained = append(retained, ack)
			continue
		}
		pendingDelivery := AstraSteeringDelivery{
			Status:             AstraSteeringPending,
			SteeringID:         firstNonEmpty(delivery.SteeringID, ack.steeringID),
			PreviousResponseID: firstNonEmpty(delivery.PreviousResponseID, ack.previousResponseID),
		}
		pendingDeliveries = append(pendingDeliveries, pendingDelivery)
		select {
		case ack.ch <- pendingDelivery:
		default:
		}
	}
	for i := len(retained); i < len(items); i++ {
		items[i] = nil
	}
	*pending = retained
	return pendingDeliveries
}

// The caller holds s.mu for all three helpers below.
func (s *ResponsesTransportState) addPendingAstraSteeringLocked(delivery AstraSteeringDelivery) {
	if s == nil || delivery.SteeringID == "" {
		return
	}
	if s.pendingAstraSteering == nil {
		s.pendingAstraSteering = make(map[string]ResponsesSteeringDelivery)
	}
	s.pendingAstraSteering[delivery.SteeringID] = ResponsesSteeringDelivery(delivery)
}

func (s *ResponsesTransportState) commitPendingAstraSteeringLocked(previousResponseID, responseID string) []ResponsesSteeringDelivery {
	if s == nil || previousResponseID == "" || responseID == "" {
		return nil
	}
	committed := make([]ResponsesSteeringDelivery, 0, len(s.pendingAstraSteering))
	for id, pending := range s.pendingAstraSteering {
		if pending.PreviousResponseID != previousResponseID {
			continue
		}
		pending.Status = AstraSteeringAccepted
		pending.ResponseID = responseID
		committed = append(committed, pending)
		delete(s.pendingAstraSteering, id)
	}
	return committed
}

func (s *ResponsesTransportState) failPendingAstraSteeringLocked(delivery AstraSteeringDelivery) bool {
	if s == nil || delivery.SteeringID == "" {
		return false
	}
	if _, ok := s.pendingAstraSteering[delivery.SteeringID]; !ok {
		return false
	}
	delete(s.pendingAstraSteering, delivery.SteeringID)
	s.astraSteeringFailures = append(s.astraSteeringFailures, ResponsesSteeringDelivery(delivery))
	return true
}

func (s *ResponsesTransportState) appendAstraSteeringCommitLocked(delivery ResponsesSteeringDelivery) {
	if s == nil || delivery.SteeringID == "" {
		return
	}
	for _, existing := range s.astraSteeringCommits {
		if existing.SteeringID == delivery.SteeringID {
			return
		}
	}
	delivery.Status = AstraSteeringAccepted
	s.astraSteeringCommits = append(s.astraSteeringCommits, delivery)
}

func (s *ResponsesTransportState) appendAstraSteeringAmbiguousLocked(delivery ResponsesSteeringDelivery) {
	if s == nil || delivery.SteeringID == "" {
		return
	}
	for _, existing := range s.astraSteeringAmbiguous {
		if existing.SteeringID == delivery.SteeringID {
			return
		}
	}
	delivery.Status = AstraSteeringAmbiguous
	s.astraSteeringAmbiguous = append(s.astraSteeringAmbiguous, delivery)
}

func (s *ResponsesTransportState) markAllPendingAstraSteeringAmbiguousLocked() {
	if s == nil || len(s.pendingAstraSteering) == 0 {
		return
	}
	for id, pending := range s.pendingAstraSteering {
		pending.Status = AstraSteeringAmbiguous
		s.appendAstraSteeringAmbiguousLocked(pending)
		s.recordSteeringDelivery(pending)
		delete(s.pendingAstraSteering, id)
	}
}

func hasAcceptedSteeringLocked(pending []*pendingSteeringAck) bool {
	for _, ack := range pending {
		if ack != nil && ack.accepted {
			return true
		}
	}
	return false
}

func steeringDeliveryFromEvent(eventType string, event map[string]any) AstraSteeringDelivery {
	status := AstraSteeringAccepted
	if eventType == "response.steer.pending" {
		status = AstraSteeringPending
	} else if eventType == "response.steer.failed" {
		status = AstraSteeringFailed
	}
	steer, _ := event["steer"].(map[string]any)
	delivery := AstraSteeringDelivery{
		Status:             status,
		SteeringID:         strings.TrimSpace(stringFromAny(steer["id"])),
		ResponseID:         strings.TrimSpace(firstNonEmpty(stringFromAny(event["response_id"]), stringFromAny(event["response"]), stringFromAny(event["id"]))),
		PreviousResponseID: strings.TrimSpace(firstNonEmpty(stringFromAny(steer["previous_response_id"]), stringFromAny(event["previous_response_id"]))),
		Error:              strings.TrimSpace(firstNonEmpty(stringFromAny(event["error"]), stringFromAny(event["message"]))),
	}
	if nested, ok := event["response"].(map[string]any); ok && delivery.ResponseID == "" {
		delivery.ResponseID = stringFromAny(nested["id"])
	}
	if nested, ok := event["error"].(map[string]any); ok && delivery.Error == "" {
		delivery.Error = firstNonEmpty(stringFromAny(nested["message"]), stringFromAny(nested["code"]), stringFromAny(nested["type"]))
	}
	return delivery
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

func responseIncompleteReason(event map[string]any) string {
	if event == nil {
		return ""
	}
	if response, ok := event["response"].(map[string]any); ok {
		if details, ok := response["incomplete_details"].(map[string]any); ok {
			return strings.ToLower(strings.TrimSpace(stringFromAny(details["reason"])))
		}
		if reason := strings.TrimSpace(stringFromAny(response["reason"])); reason != "" {
			return strings.ToLower(reason)
		}
	}
	return strings.ToLower(strings.TrimSpace(stringFromAny(event["reason"])))
}

func shouldForwardResponsesWebsocketEvent(eventType string, event map[string]any) bool {
	switch eventType {
	case "response.steer.accepted", "response.steer.failed", "response.steer.pending":
		return false
	case "response.incomplete":
		return responseIncompleteReason(event) != "steered"
	default:
		return true
	}
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
