// Package openaiclient provides an OpenAI Responses API client that supports
// both API key auth and OAuth subscription token auth.
package openaiclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
)

const (
	// DefaultModel is the fallback model when no model is provided.
	DefaultModel = "gpt-5.6-sol"

	defaultModelRequestTimeout = 10 * time.Minute

	openAIOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// ChatGPT Codex backend requires this query parameter.
	openAIOAuthClientVersion = "0.144.0"
	// Matches Codex CLI originator for ChatGPT OAuth-backed calls.
	openAIOAuthOriginator = "codex_cli_rs"
)

// OpenAIAPIBaseURL is the OpenAI API base URL.
// It is a variable so tests can override it.
var OpenAIAPIBaseURL = "https://api.openai.com/v1/"

// OpenAIChatGPTAPIBaseURL is the ChatGPT Codex backend base URL used with
// OAuth subscription tokens from auth.openai.com.
// It is a variable so tests can override it.
var OpenAIChatGPTAPIBaseURL = "https://chatgpt.com/backend-api/codex/"

// OpenAIOAuthTokenURL is the OAuth token endpoint used for refresh.
// It is a variable so tests can override it.
var OpenAIOAuthTokenURL = "https://auth.openai.com/oauth/token"

var defaultHTTPClient = &http.Client{
	Timeout: defaultModelRequestTimeout,
	Transport: &loggingRoundTripper{
		base: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
	},
}

type loggingRoundTripper struct {
	base http.RoundTripper
}

func (rt *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp == nil || resp.StatusCode < http.StatusBadRequest || resp.Body == nil {
		return resp, nil
	}

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		applog.Infof("[openai-client] non-2xx response status=%d method=%s url=%s body_read_error=%v", resp.StatusCode, req.Method, req.URL.String(), readErr)
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return resp, nil
	}

	if strings.Contains(req.URL.Host, "auth.openai.com") {
		applog.Infof("[openai-client] non-2xx response status=%d method=%s url=%s body=<redacted>", resp.StatusCode, req.Method, req.URL.String())
	} else {
		trimmed := strings.TrimSpace(string(body))
		if trimmed == "" {
			applog.Infof("[openai-client] non-2xx response status=%d method=%s url=%s body=<empty>", resp.StatusCode, req.Method, req.URL.String())
		} else {
			applog.Infof("[openai-client] non-2xx response status=%d method=%s url=%s body=%s", resp.StatusCode, req.Method, req.URL.String(), trimmed)
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

// StoredAuth holds OAuth tokens or an API key.
type StoredAuth struct {
	Token                 string `json:"token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresAt             int64  `json:"expires_at"`
	APIKey                string `json:"api_key,omitempty"`
	AccountID             string `json:"account_id,omitempty"`
	AuthHeaderName        string `json:"auth_header_name,omitempty"`
	AuthHeaderValuePrefix string `json:"auth_header_value_prefix,omitempty"`
	AllowMissingAuth      bool   `json:"allow_missing_auth,omitempty"`
}

// OAuthTokens are refreshed OAuth credentials supplied by an external authority.
type OAuthTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	AccountID    string
}

// OAuthUnauthorizedHandler recovers an OAuth client after a request receives 401.
type OAuthUnauthorizedHandler func(ctx context.Context, tokenUsed string) (OAuthTokens, bool, error)

// Message is a single conversation message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SendOptions configures a single Send call.
type SendOptions struct {
	Model               string
	MaxOutputTokens     int
	System              string
	Stream              bool
	ReasoningEffort     string
	DisableTools        bool
	SuppressToolMarkers bool
	OnDelta             func(text string)
	Attachments         []*FileAttachment
}

// Response is the result of a Send call.
type Response struct {
	Text              string
	Model             string
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	ReasoningTokens   int
	StopReason        string
}

// Client is an OpenAI API client with conversation history.
type Client struct {
	auth                          *StoredAuth
	httpClient                    *http.Client
	sessionID                     string
	apiBaseURL                    string
	oauthUnauthorizedHandler      OAuthUnauthorizedHandler
	oauthRefreshExternallyManaged bool
	responsesTransportState       *ResponsesTransportState
	History                       []Message
}

// NewWithAPIKey creates a client using an API key.
func NewWithAPIKey(apiKey string) *Client {
	return newClient(&StoredAuth{APIKey: apiKey})
}

// NewWithCompatibleAPIKey creates a client for an OpenAI-compatible endpoint.
func NewWithCompatibleAPIKey(apiKey, baseURL, authHeaderName, authHeaderValuePrefix string) *Client {
	client := newClient(&StoredAuth{
		APIKey:                apiKey,
		AuthHeaderName:        authHeaderName,
		AuthHeaderValuePrefix: authHeaderValuePrefix,
		AllowMissingAuth:      true,
	})
	client.apiBaseURL = strings.TrimSpace(baseURL)
	return client
}

// NewWithOAuthToken creates a client using an OAuth access token.
func NewWithOAuthToken(token, refreshToken string, expiresAt int64, accountID string) *Client {
	return newClient(&StoredAuth{
		Token:        token,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    accountID,
	})
}

func newClient(auth *StoredAuth) *Client {
	transportState := NewResponsesTransportState()
	return &Client{
		auth:                    auth,
		httpClient:              defaultHTTPClient,
		sessionID:               transportState.sessionID,
		responsesTransportState: transportState,
	}
}

// SetResponsesTransportState shares WebSocket connection and fallback state
// without sharing conversation history or credentials.
func (c *Client) SetResponsesTransportState(state *ResponsesTransportState) {
	if state != nil {
		c.responsesTransportState = state
		c.sessionID = state.sessionID
	}
}

// CloseResponsesTransport releases the WebSocket owned by this client.
// Callers must not use the client after closing its transport.
func (c *Client) CloseResponsesTransport() {
	if c.responsesTransportState != nil {
		c.responsesTransportState.Close()
	}
}

// CurrentAuth returns a copy of the current auth values (including refreshed tokens).
func (c *Client) CurrentAuth() StoredAuth {
	if c.auth == nil {
		return StoredAuth{}
	}
	return *c.auth
}

// SetOAuthUnauthorizedHandler installs request-level OAuth recovery used when a
// request receives 401 before streaming content starts. It also marks token
// refresh as externally managed so adapter-backed clients persist all rotations
// through their selected model config row instead of refreshing only in-memory.
func (c *Client) SetOAuthUnauthorizedHandler(handler OAuthUnauthorizedHandler) {
	c.oauthUnauthorizedHandler = handler
	c.oauthRefreshExternallyManaged = handler != nil
}

func (c *Client) ensureValidToken() error {
	if c.oauthRefreshExternallyManaged {
		return nil
	}
	return c.EnsureValidToken()
}

func (c *Client) applyOAuthTokens(tokens OAuthTokens) {
	if c.auth == nil {
		return
	}
	if tokens.AccessToken != "" {
		c.auth.Token = tokens.AccessToken
	}
	if tokens.RefreshToken != "" {
		c.auth.RefreshToken = tokens.RefreshToken
	}
	if tokens.ExpiresAt != 0 {
		c.auth.ExpiresAt = tokens.ExpiresAt
	}
	if tokens.AccountID != "" {
		c.auth.AccountID = tokens.AccountID
	}
}

func (c *Client) doWithOAuthRecovery(ctx context.Context, endpoint string, isOAuth bool, buildReq func() (*http.Request, error)) (*http.Response, error) {
	tokenUsed := ""
	if c.auth != nil {
		tokenUsed = c.auth.Token
	}
	resp, err := c.doRequestWithRetry(ctx, buildReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized || !isOAuth || c.oauthUnauthorizedHandler == nil {
		return resp, nil
	}

	resp.Body.Close()
	tokens, recovered, recoverErr := c.oauthUnauthorizedHandler(ctx, tokenUsed)
	if recoverErr != nil {
		return nil, recoverErr
	}
	if !recovered {
		return nil, fmt.Errorf("POST %q: 401 Unauthorized: OAuth unauthorized recovery did not refresh token", endpoint)
	}
	c.applyOAuthTokens(tokens)
	return c.doRequestWithRetry(ctx, buildReq)
}

func (c *Client) doRequestWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) (*http.Response, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.WrapNetworkError = wrapNetworkError
	policy.OnRetry = func(event httpretry.RetryEvent) {
		if event.Err != nil {
			applog.Infof("[openai-client] network error, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
			return
		}
		applog.Infof("[openai-client] received HTTP %d, retry attempt %d/%d in %v", event.StatusCode, event.Attempt, event.MaxRetries, event.Delay)
	}
	return httpretry.Do(ctx, c.httpClient, buildReq, policy)
}

// EnsureValidToken refreshes the OAuth token if it is expiring within 1 hour.
func (c *Client) EnsureValidToken() error {
	if c.auth == nil {
		return fmt.Errorf("missing auth")
	}
	if c.auth.APIKey != "" {
		return nil
	}
	if c.auth.Token == "" {
		if c.auth.AllowMissingAuth {
			return nil
		}
		return fmt.Errorf("missing OAuth access token")
	}
	if c.auth.ExpiresAt >= time.Now().Add(time.Hour).UnixMilli() {
		return nil
	}
	if c.auth.RefreshToken == "" {
		return fmt.Errorf("OAuth token expired and no refresh token is available")
	}

	newAuth, err := RefreshToken(c.auth.RefreshToken)
	if err != nil {
		return fmt.Errorf("token refresh: %w", err)
	}

	c.auth.Token = newAuth.Token
	if newAuth.RefreshToken != "" {
		c.auth.RefreshToken = newAuth.RefreshToken
	}
	c.auth.ExpiresAt = newAuth.ExpiresAt
	return nil
}

// RefreshToken refreshes an OAuth access token using a refresh token.
func RefreshToken(refreshToken string) (*StoredAuth, error) {
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refreshToken)
	values.Set("client_id", openAIOAuthClientID)

	req, err := http.NewRequest(http.MethodPost, OpenAIOAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", wrapNetworkError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, fmt.Errorf("%w: OAuth refresh failed with HTTP %d", ErrTokenExpired, resp.StatusCode)
		}
		return nil, fmt.Errorf("OAuth refresh failed with HTTP %d", resp.StatusCode)
	}

	var tokenResult struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResult); err != nil {
		return nil, fmt.Errorf("decode refresh token response: %w", err)
	}
	if tokenResult.AccessToken == "" {
		return nil, fmt.Errorf("refresh response did not include access_token")
	}

	return &StoredAuth{
		Token:        tokenResult.AccessToken,
		RefreshToken: firstNonEmpty(tokenResult.RefreshToken, refreshToken),
		ExpiresAt:    resolveOAuthExpiryAt(tokenResult.ExpiresIn),
	}, nil
}

// ClearHistory clears accumulated conversation history.
func (c *Client) ClearHistory() {
	c.History = nil
}

// Send sends a prompt and returns the model response.
// It appends the user prompt and assistant response to History.
func (c *Client) Send(ctx context.Context, prompt string, opts *SendOptions) (*Response, error) {
	if opts == nil {
		opts = &SendOptions{}
	}
	if opts.Model == "" {
		opts.Model = DefaultModel
	}
	if opts.MaxOutputTokens == 0 {
		opts.MaxOutputTokens = 4096
	}

	isChatGPTOAuth := strings.TrimSpace(c.auth.APIKey) == ""

	if err := c.ensureValidToken(); err != nil {
		return nil, err
	}

	c.History = append(c.History, Message{Role: "user", Content: prompt})

	inputItems, err := buildInputItems(c.History, opts.Attachments)
	if err != nil {
		return nil, fmt.Errorf("build input items: %w", err)
	}

	payload := map[string]any{
		"model": opts.Model,
		"input": inputItems,
	}

	if !isChatGPTOAuth {
		payload["max_output_tokens"] = opts.MaxOutputTokens
	}

	if isChatGPTOAuth {
		// ChatGPT Codex backend requires explicit store=false.
		payload["store"] = false
		payload["include"] = []string{"reasoning.encrypted_content"}
	}

	system := strings.TrimSpace(opts.System)
	if system == "" && isChatGPTOAuth {
		// ChatGPT Codex backend requires instructions.
		system = "You are a helpful assistant."
	}
	if system != "" {
		payload["instructions"] = system
	}

	if effort := normalizeReasoningEffort(opts.ReasoningEffort); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}

	stream := opts.Stream || isChatGPTOAuth
	if stream {
		payload["stream"] = true
	}
	if opts.DisableTools {
		payload["tool_choice"] = "none"
	}

	if isResponsesLiteWebsocketModel(opts.Model) {
		payload["stream"] = true
		wsPayload := buildResponsesLiteWebsocketPayload(payload, system, c.sessionID)
		policy := httpretry.DefaultPolicy()
		policy.AllowReplay = true
		result, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*Response, bool, error) {
			openStream := func(useWebsocket bool) (io.ReadCloser, error) {
				if useWebsocket {
					return c.openResponsesWebsocketStream(attemptCtx, wsPayload, isChatGPTOAuth)
				}
				return c.openResponsesLiteHTTPStream(attemptCtx, wsPayload, isChatGPTOAuth)
			}
			useWebsocket := !c.responsesTransportState.websocketDisabled.Load()
			body, wsErr := openStream(useWebsocket)
			if useWebsocket && shouldFallbackResponsesWebsocket(attemptCtx, wsErr) {
				c.responsesTransportState.disableWebsocket()
				useWebsocket = false
				body, wsErr = openStream(false)
			}
			if wsErr != nil {
				return nil, false, wsErr
			}
			sawOutput := false
			onDelta := func(text string) {
				sawOutput = true
				if opts.OnDelta != nil {
					opts.OnDelta(text)
				}
			}
			result, wsErr := parseStreamingResponse(body, onDelta, opts.SuppressToolMarkers)
			body.Close()
			if wsErr != nil && useWebsocket && shouldFallbackResponsesWebsocket(attemptCtx, wsErr) {
				c.responsesTransportState.disableWebsocket()
				if !sawOutput {
					body, wsErr = openStream(false)
					if wsErr == nil {
						result, wsErr = parseStreamingResponse(body, onDelta, opts.SuppressToolMarkers)
						body.Close()
					}
				}
			}
			if wsErr != nil {
				return result, sawOutput, httpretry.NewStreamError(wsErr)
			}
			return result, sawOutput, nil
		})
		if err != nil {
			return nil, err
		}
		c.History = append(c.History, Message{Role: "assistant", Content: result.Text})
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

	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	result, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (*Response, bool, error) {
		buildReq := func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			c.applyAuthHeaders(req, isChatGPTOAuth)
			req.Header.Set("Content-Type", "application/json")
			if stream {
				req.Header.Set("Accept", "text/event-stream")
			}
			return req, nil
		}
		resp, err := c.doWithOAuthRecovery(attemptCtx, endpoint, isChatGPTOAuth, buildReq)
		if err != nil {
			return nil, false, err
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
			apiErr := parseAPIError(resp.StatusCode, errBody)
			return nil, false, httpretry.NewResponseError(resp, fmt.Errorf("POST %q: %w", endpoint, apiErr))
		}
		observed := false
		onDelta := func(text string) {
			observed = true
			if opts.OnDelta != nil {
				opts.OnDelta(text)
			}
		}
		var result *Response
		if stream {
			result, err = parseStreamingResponse(resp.Body, onDelta, opts.SuppressToolMarkers)
		} else {
			result, err = parseResponse(resp.Body)
		}
		if err != nil {
			return result, observed, httpretry.NewStreamError(err)
		}
		return result, observed, nil
	})
	if err != nil {
		return nil, err
	}

	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})
	return result, nil
}

func (c *Client) completionsEndpoint() (string, error) {
	base := strings.TrimSpace(c.apiBaseURL)
	if base == "" {
		base = strings.TrimSpace(OpenAIAPIBaseURL)
	}
	if base == "" {
		return "", fmt.Errorf("missing base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base URL must use http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("base URL must include host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return u.String(), nil
}

func (c *Client) responsesEndpoint(isChatGPTOAuth bool) (string, error) {
	base := strings.TrimSpace(OpenAIAPIBaseURL)
	if isChatGPTOAuth {
		base = strings.TrimSpace(OpenAIChatGPTAPIBaseURL)
	}
	if base == "" {
		return "", fmt.Errorf("missing base URL")
	}

	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL %q: %w", base, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/responses"
	if isChatGPTOAuth {
		q := u.Query()
		q.Set("client_version", openAIOAuthClientVersion)
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func (c *Client) responsesCompactEndpoint(isChatGPTOAuth bool) (string, error) {
	endpoint, err := c.responsesEndpoint(isChatGPTOAuth)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse responses endpoint %q: %w", endpoint, err)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/compact"
	return u.String(), nil
}

func (c *Client) applyAuthHeaders(req *http.Request, isChatGPTOAuth bool) {
	token := strings.TrimSpace(c.auth.APIKey)
	if token == "" {
		token = strings.TrimSpace(c.auth.Token)
	}
	if token != "" {
		headerName := strings.TrimSpace(c.auth.AuthHeaderName)
		if headerName == "" {
			headerName = "Authorization"
		}
		prefix := c.auth.AuthHeaderValuePrefix
		if prefix == "" {
			prefix = "Bearer "
		}
		req.Header.Set(headerName, prefix+token)
	}

	if !isChatGPTOAuth {
		return
	}

	req.Header.Set("originator", openAIOAuthOriginator)
	req.Header.Set("session_id", c.sessionID)
	req.Header.Set("x-client-request-id", c.sessionID)

	accountID := strings.TrimSpace(c.auth.AccountID)
	if accountID == "" {
		accountID = ExtractChatGPTAccountID(c.auth.Token)
	}
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
}

func parseResponse(body io.Reader) (*Response, error) {
	var payload map[string]any
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	resp := &Response{
		Text:       extractOutputText(payload),
		Model:      stringFromAny(payload["model"]),
		StopReason: responseStopReasonMap(payload),
	}
	resp.InputTokens, resp.OutputTokens = extractUsage(payload)
	resp.CachedInputTokens = extractCachedInputTokens(payload)
	resp.ReasoningTokens = extractReasoningTokens(payload)
	if resp.StopReason == "" {
		resp.StopReason = "completed"
	}
	return resp, nil
}

func parseStreamingResponse(body io.Reader, onDelta func(string), suppressToolMarkers bool) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var (
		outputText strings.Builder
		completed  map[string]any
		callNames  = make(map[string]string)
		lastMarker string
	)

	emit := func(text string) {
		if text == "" {
			return
		}
		if openAIIsMarkerChunk(text) {
			marker := strings.TrimSpace(text)
			if marker != "" && marker == lastMarker {
				return
			}
			lastMarker = marker
		} else if strings.TrimSpace(text) != "" {
			lastMarker = ""
		}
		outputText.WriteString(text)
		if onDelta != nil {
			onDelta(text)
		}
	}

	var emitter *openAITextStreamEmitter
	if !suppressToolMarkers {
		emitter = newOpenAITextStreamEmitter(emit)
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") {
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

		typ := stringFromAny(ev["type"])
		switch typ {
		case "response.output_text.delta":
			delta := stringFromAny(ev["delta"])
			if delta != "" {
				if emitter != nil {
					emitter.Write(delta)
				} else {
					emit(delta)
				}
			}
		case "response.function_call_arguments.delta", "response.output_item.added":
			if emitter != nil {
				emitter.FlushBoundary()
			}
		case "response.output_item.done":
			if suppressToolMarkers {
				continue
			}
			if emitter != nil {
				emitter.FlushBoundary()
			}
			if item, ok := ev["item"].(map[string]any); ok {
				for _, marker := range openAIOutputItemMarkers(item, callNames) {
					emit(marker)
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

	if emitter != nil {
		emitter.Flush()
	}

	resp := &Response{Text: outputText.String()}
	if completed != nil {
		if resp.Text == "" {
			resp.Text = extractOutputText(completed)
		}
		resp.Model = stringFromAny(completed["model"])
		resp.InputTokens, resp.OutputTokens = extractUsage(completed)
		resp.CachedInputTokens = extractCachedInputTokens(completed)
		resp.ReasoningTokens = extractReasoningTokens(completed)
		resp.StopReason = responseStopReasonMap(completed)
	}
	if resp.StopReason == "" {
		resp.StopReason = "completed"
	}
	return resp, nil
}

func openAIOutputItemMarkers(item map[string]any, callNames map[string]string) []string {
	itemType := strings.TrimSpace(stringFromAny(item["type"]))
	switch itemType {
	case "function_call":
		name := strings.TrimSpace(stringFromAny(item["name"]))
		if name == "" {
			name = "tool"
		}
		if callID := strings.TrimSpace(stringFromAny(item["call_id"])); callID != "" {
			callNames[callID] = name
		}
		detail := openAIToolDetailFromArguments(name, item["arguments"])
		return []string{openAIFormatUsingToolMarker(name, detail)}
	case "custom_tool_call":
		name := strings.TrimSpace(stringFromAny(item["name"]))
		if name == "" {
			name = "tool"
		}
		if callID := strings.TrimSpace(stringFromAny(item["call_id"])); callID != "" {
			callNames[callID] = name
		}
		detail := strings.TrimSpace(stringFromAny(item["input"]))
		if detail != "" && len(detail) > 120 {
			detail = detail[:117] + "..."
		}
		return []string{openAIFormatUsingToolMarker(name, detail)}
	case "local_shell_call":
		if callID := strings.TrimSpace(stringFromAny(item["call_id"])); callID != "" {
			callNames[callID] = "bash"
		}
		return []string{openAIFormatUsingToolMarker("bash", openAILocalShellCommand(item))}
	case "function_call_output", "custom_tool_call_output":
		callID := strings.TrimSpace(stringFromAny(item["call_id"]))
		name := strings.TrimSpace(callNames[callID])
		if name == "" {
			name = "tool"
		}
		status := "done"
		if s := strings.ToLower(strings.TrimSpace(stringFromAny(item["status"]))); strings.Contains(s, "error") || strings.Contains(s, "fail") {
			status = "error"
		}
		output := openAIExtractToolOutputText(item["output"])
		if output == "" {
			return nil
		}
		return []string{openAIFormatToolResultMarker(name, status, output)}
	case "web_search_call":
		if query := openAIExtractWebSearchQuery(item); query != "" {
			return []string{openAIFormatUsingToolMarker("web_search", query)}
		}
		if url := openAIExtractWebSearchURL(item); url != "" {
			return []string{openAIFormatUsingToolMarker("web_search", url)}
		}
		return []string{openAIFormatUsingToolMarker("web_search", "")}
	default:
		return nil
	}
}

func openAIExtractWebSearchQuery(item map[string]any) string {
	if q := strings.TrimSpace(stringFromAny(item["query"])); q != "" {
		return q
	}
	action, ok := item["action"].(map[string]any)
	if !ok {
		return ""
	}
	if q := strings.TrimSpace(stringFromAny(action["query"])); q != "" {
		return q
	}
	if q := strings.TrimSpace(stringFromAny(action["search_query"])); q != "" {
		return q
	}
	return ""
}

func openAIExtractWebSearchURL(item map[string]any) string {
	if u := strings.TrimSpace(stringFromAny(item["url"])); u != "" {
		return u
	}
	if action, ok := item["action"].(map[string]any); ok {
		if u := strings.TrimSpace(stringFromAny(action["url"])); u != "" {
			return u
		}
		if sources, ok := action["sources"].([]any); ok {
			for _, src := range sources {
				if m, ok := src.(map[string]any); ok {
					if u := strings.TrimSpace(stringFromAny(m["url"])); u != "" {
						return u
					}
				}
			}
		}
	}
	if results, ok := item["results"].([]any); ok {
		for _, res := range results {
			if m, ok := res.(map[string]any); ok {
				if u := strings.TrimSpace(stringFromAny(m["url"])); u != "" {
					return u
				}
			}
		}
	}
	return ""
}

func openAILocalShellCommand(item map[string]any) string {
	action, ok := item["action"].(map[string]any)
	if !ok {
		return ""
	}
	switch command := action["command"].(type) {
	case string:
		return strings.TrimSpace(command)
	case []any:
		parts := make([]string, 0, len(command))
		for _, part := range command {
			if token := strings.TrimSpace(stringFromAny(part)); token != "" {
				parts = append(parts, token)
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	default:
		return ""
	}
}

func openAIExtractToolOutputText(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			switch block := item.(type) {
			case string:
				text := strings.TrimSpace(block)
				if text != "" {
					parts = append(parts, text)
				}
			case map[string]any:
				text := firstNonEmpty(
					strings.TrimSpace(stringFromAny(block["text"])),
					strings.TrimSpace(stringFromAny(block["output_text"])),
					strings.TrimSpace(stringFromAny(block["content"])),
				)
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		if body, ok := v["body"]; ok {
			if text := openAIExtractToolOutputText(body); text != "" {
				return text
			}
		}
		if content, ok := v["content"]; ok {
			if text := openAIExtractToolOutputText(content); text != "" {
				return text
			}
		}
		return strings.TrimSpace(firstNonEmpty(stringFromAny(v["text"]), stringFromAny(v["output"])))
	default:
		return ""
	}
}

const openAIToolDetailMaxLen = 320

func openAIToolDetailFromArguments(toolName string, raw any) string {
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return ""
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(v), &args); err != nil {
			// Some models emit plain command text instead of JSON.
			if strings.EqualFold(openAICanonicalToolName(toolName), "Bash") {
				return openAITrimDetail(v, openAIToolDetailMaxLen)
			}
			return ""
		}
		return openAIToolDetailFromMap(toolName, args)
	case map[string]any:
		return openAIToolDetailFromMap(toolName, v)
	default:
		return ""
	}
}

func openAIToolDetailFromMap(toolName string, args map[string]any) string {
	lower := strings.ToLower(strings.TrimSpace(toolName))
	getString := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(stringFromAny(args[key])); value != "" {
				return value
			}
		}
		return ""
	}

	var detail string
	switch lower {
	case "read_file", "write_file", "edit_file":
		if path := getString("file_path", "path"); path != "" {
			detail = filepath.Base(path)
		}
	case "list_files", "glob":
		detail = getString("path", "pattern")
	case "grep_search", "grep":
		detail = getString("pattern")
	case "bash", "shell", "shell_command", "exec_command":
		detail = getString("command", "cmd")
	default:
		// Best-effort fallback for ad-hoc tool payloads.
		if cmd := getString("command", "cmd"); cmd != "" {
			detail = cmd
		} else if path := getString("file_path", "path"); path != "" {
			detail = filepath.Base(path)
		}
	}

	return openAITrimDetail(detail, openAIToolDetailMaxLen)
}

func openAITrimDetail(detail string, max int) string {
	detail = strings.ReplaceAll(strings.TrimSpace(detail), "]", ")")
	if detail == "" {
		return ""
	}
	if max <= 0 || len(detail) <= max {
		return detail
	}
	return detail[:max-3] + "..."
}

func openAICanonicalToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "shell", "shell_command", "exec_command", "local_shell_call":
		return "Bash"
	case "read_file":
		return "Read"
	case "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "list_files", "glob":
		return "Glob"
	case "grep_search", "grep":
		return "Grep"
	case "web_search", "web_search_preview", "web_search_call":
		return "WebSearch"
	default:
		if strings.TrimSpace(name) == "" {
			return "tool"
		}
		return strings.TrimSpace(name)
	}
}

func openAIFormatUsingToolMarker(name, detail string) string {
	toolName := openAICanonicalToolName(name)
	if detail = strings.TrimSpace(detail); detail != "" {
		return fmt.Sprintf("\n[Using tool: %s | %s]\n", toolName, detail)
	}
	return fmt.Sprintf("\n[Using tool: %s]\n", toolName)
}

func openAIFormatToolResultMarker(name, status, output string) string {
	preview := strings.TrimSpace(output)
	if preview == "" {
		preview = "(no output)"
	}
	if len(preview) > 300 {
		preview = preview[:300] + "..."
	}
	if strings.TrimSpace(status) == "" {
		status = "done"
	}
	return fmt.Sprintf("[Tool %s %s]\n%s\n[/Tool]\n", openAICanonicalToolName(name), status, preview)
}

func openAIIsMarkerChunk(text string) bool {
	return strings.Contains(text, "[Using tool:") || strings.Contains(text, "[Tool ") || strings.Contains(text, "[/Tool]")
}

type openAITextStreamEmitter struct {
	emit func(string)
}

func newOpenAITextStreamEmitter(emit func(string)) *openAITextStreamEmitter {
	return &openAITextStreamEmitter{emit: emit}
}

func (s *openAITextStreamEmitter) Write(delta string) {
	if delta == "" {
		return
	}
	s.emit(delta)
}

func (s *openAITextStreamEmitter) Flush() {
}

func (s *openAITextStreamEmitter) FlushBoundary() {
}

func buildInputItems(history []Message, attachments []*FileAttachment) ([]any, error) {
	items := make([]any, 0, len(history))
	lastIdx := len(history) - 1

	for i, msg := range history {
		role := roleForMessage(msg.Role)
		if i == lastIdx && role == "user" && len(attachments) > 0 {
			content := make([]any, 0, 1+len(attachments))
			if msg.Content != "" {
				content = append(content, map[string]any{
					"type": "input_text",
					"text": msg.Content,
				})
			}
			for _, att := range attachments {
				block, err := att.toInputContent()
				if err != nil {
					return nil, err
				}
				content = append(content, block)
			}
			items = append(items, map[string]any{
				"type":    "message",
				"role":    role,
				"content": content,
			})
			continue
		}

		items = append(items, map[string]any{
			"type":    "message",
			"role":    role,
			"content": msg.Content,
		})
	}

	return items, nil
}

func roleForMessage(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	case "developer":
		return "developer"
	default:
		return "user"
	}
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeReasoningSummary(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "concise", "detailed", "none":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func responseStopReasonMap(payload map[string]any) string {
	status := stringFromAny(payload["status"])
	if status == "incomplete" {
		if details, ok := payload["incomplete_details"].(map[string]any); ok {
			if reason := stringFromAny(details["reason"]); reason != "" {
				return reason
			}
		}
	}
	return status
}

func extractUsage(payload map[string]any) (int, int) {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		return 0, 0
	}
	return intFromAny(usage["input_tokens"]), intFromAny(usage["output_tokens"])
}

func extractCachedInputTokens(payload map[string]any) int {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		return 0
	}
	details, ok := usage["input_tokens_details"].(map[string]any)
	if !ok {
		return 0
	}
	return intFromAny(details["cached_tokens"])
}

func extractReasoningTokens(payload map[string]any) int {
	usage, ok := payload["usage"].(map[string]any)
	if !ok {
		return 0
	}
	details, ok := usage["output_tokens_details"].(map[string]any)
	if !ok {
		return 0
	}
	return intFromAny(details["reasoning_tokens"])
}

func extractOutputText(payload map[string]any) string {
	output, ok := payload["output"].([]any)
	if !ok {
		return ""
	}

	var parts []string
	for _, item := range output {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := itemMap["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if stringFromAny(blockMap["type"]) == "output_text" {
				if text := stringFromAny(blockMap["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

func extractReasoningText(payload map[string]any) string {
	output, ok := payload["output"].([]any)
	if !ok {
		return ""
	}

	parts := make([]string, 0, len(output))
	for _, item := range output {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringFromAny(itemMap["type"])) != "reasoning" {
			continue
		}
		if text := openAIReasoningTextFromItem(itemMap); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func openAIReasoningTextFromItem(item map[string]any) string {
	parts := make([]string, 0, 2)

	if summaryRaw, ok := item["summary"].([]any); ok {
		summaryParts := make([]string, 0, len(summaryRaw))
		for _, raw := range summaryRaw {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			text := strings.TrimSpace(firstNonEmpty(
				stringFromAny(entry["text"]),
				stringFromAny(entry["summary_text"]),
			))
			if text != "" {
				summaryParts = append(summaryParts, text)
			}
		}
		if len(summaryParts) > 0 {
			parts = append(parts, strings.Join(summaryParts, "\n\n"))
		}
	}

	if contentRaw, ok := item["content"].([]any); ok {
		contentParts := make([]string, 0, len(contentRaw))
		for _, raw := range contentRaw {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			entryType := strings.TrimSpace(stringFromAny(entry["type"]))
			switch entryType {
			case "", "reasoning_text", "text", "summary_text":
				text := strings.TrimSpace(firstNonEmpty(
					stringFromAny(entry["text"]),
					stringFromAny(entry["content"]),
				))
				if text != "" {
					contentParts = append(contentParts, text)
				}
			}
		}
		if len(contentParts) > 0 {
			parts = append(parts, strings.Join(contentParts, "\n\n"))
		}
	}

	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func extractErrorMessage(ev map[string]any) string {
	if msg := stringFromAny(ev["message"]); msg != "" {
		return msg
	}
	if errObj, ok := ev["error"].(map[string]any); ok {
		if msg := stringFromAny(errObj["message"]); msg != "" {
			return msg
		}
		if code := stringFromAny(errObj["code"]); code != "" {
			return code
		}
	}
	if code := stringFromAny(ev["code"]); code != "" {
		return code
	}
	return ""
}

func responsesStreamTerminalError(eventType string, ev map[string]any) error {
	if response, ok := ev["response"].(map[string]any); ok {
		if errObj, ok := response["error"].(map[string]any); ok {
			code := stringFromAny(errObj["code"])
			message := stringFromAny(errObj["message"])
			switch {
			case code != "" && message != "":
				return fmt.Errorf("stream error: %s: %s", code, message)
			case code != "":
				return fmt.Errorf("stream error: %s", code)
			case message != "":
				return fmt.Errorf("stream error: %s", message)
			}
		}
		if msg := extractErrorMessage(response); msg != "" {
			return fmt.Errorf("stream error: %s", msg)
		}
		if details, ok := response["incomplete_details"].(map[string]any); ok {
			if reason := stringFromAny(details["reason"]); reason != "" {
				return fmt.Errorf("stream error: incomplete response: %s", reason)
			}
		}
	}
	if msg := extractErrorMessage(ev); msg != "" {
		return fmt.Errorf("stream error: %s", msg)
	}
	return fmt.Errorf("stream error: %s", strings.TrimSpace(eventType))
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resolveOAuthExpiryAt(expiresInSeconds int64) int64 {
	if expiresInSeconds > 0 {
		return time.Now().UnixMilli() + expiresInSeconds*1000
	}
	return time.Now().Add(24 * time.Hour).UnixMilli()
}

func newSessionID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func ExtractChatGPTAccountID(token string) string {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	if id := strings.TrimSpace(claims.Auth.ChatGPTAccountID); id != "" {
		return id
	}
	return strings.TrimSpace(claims.ChatGPTAccountID)
}
