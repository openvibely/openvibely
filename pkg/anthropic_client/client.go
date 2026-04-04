// Package anthropicclient provides an Anthropic API client that supports both
// API key auth and OAuth (Claude Max subscription) auth.
//
// Usage with OAuth (requires prior login via CLI):
//
//	client, err := anthropicclient.NewFromStoredAuth()
//	resp, err := client.Send(ctx, "Hello!", nil)
//
// Usage with API key:
//
//	client := anthropicclient.NewWithAPIKey("sk-ant-...")
//	resp, err := client.Send(ctx, "Hello!", nil)
package anthropicclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AnthropicAPIHost is the base URL for the Anthropic Messages API.
// It is a variable (not a constant) to allow overriding in tests.
var AnthropicAPIHost = "https://api.anthropic.com"

const (
	AnthropicAPIVersion = "2023-06-01"
	OAuthBetaHeader     = "oauth-2025-04-20"
	DefaultModel        = "claude-sonnet-4-20250514"

	oauthTokenURL = "https://platform.claude.com/v1/oauth/token"
	oauthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthScope    = "user:profile user:inference user:sessions:claude_code user:mcp_servers"
	tokenFileName = ".claude-max-client.json"
)

// defaultHTTPClient is a shared HTTP client with connection pooling.
// Reusing a single client avoids repeated TCP/TLS handshakes across API calls.
var defaultHTTPClient = &http.Client{
	Timeout: 5 * time.Minute,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
	},
}

// StoredAuth holds OAuth tokens or an API key.
type StoredAuth struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	APIKey       string `json:"api_key,omitempty"`
}

// Message is a single message in a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// MessagesRequest is sent to the Anthropic Messages API.
type MessagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream,omitempty"`
	System    string    `json:"system,omitempty"`
}

// multimodalMessage is a message where Content can be either a string
// or an array of content blocks (for multimodal messages with attachments).
type multimodalMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// multimodalRequest is a Messages API request that supports multimodal content.
type multimodalRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []multimodalMessage `json:"messages"`
	Stream    bool                `json:"stream,omitempty"`
	System    string              `json:"system,omitempty"`
}

// MessagesResponse is returned from the Messages API.
type MessagesResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []ContentBlock `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      Usage  `json:"usage"`
}

// ContentBlock is a content block in a response.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usageCacheCreation struct {
	InputTokens int `json:"input_tokens"`
}

type usageCacheRead struct {
	InputTokens int `json:"input_tokens"`
}

// Usage tracks token usage for a response.
type Usage struct {
	InputTokens         int                `json:"input_tokens"`
	OutputTokens        int                `json:"output_tokens"`
	CacheCreationInputTokens int           `json:"cache_creation_input_tokens"`
	CacheReadInputTokens int               `json:"cache_read_input_tokens"`
	CacheCreation       usageCacheCreation `json:"cache_creation,omitempty"`
	CacheRead           usageCacheRead     `json:"cache_read,omitempty"`
}

// StreamEvent represents a server-sent event from the streaming API.
type StreamEvent struct {
	Type  string          `json:"type"`
	Delta json.RawMessage `json:"delta,omitempty"`
}

// StreamDelta is a text delta from streaming.
type StreamDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SendOptions configures a single Send call.
type SendOptions struct {
	Model     string
	MaxTokens int
	System    string
	Stream    bool
	// OnDelta is called for each text chunk during streaming.
	// If nil with Stream=true, chunks are collected silently.
	OnDelta func(text string)
	// Attachments are files to include with the message (images, PDFs, code files).
	// They are sent as multimodal content blocks in the API request.
	Attachments []*FileAttachment
}

// Response is the result of a Send call.
type Response struct {
	Text         string
	Model        string
	InputTokens  int
	OutputTokens int
	StopReason   string
}

// Client is an Anthropic API client with conversation history.
type Client struct {
	auth       *StoredAuth
	httpClient *http.Client
	History    []Message
}

// Auth returns the current auth state (token may have been refreshed).
func (c *Client) Auth() StoredAuth {
	return *c.auth
}

// NewWithAPIKey creates a client using an API key.
func NewWithAPIKey(apiKey string) *Client {
	return &Client{
		auth:       &StoredAuth{APIKey: apiKey},
		httpClient: defaultHTTPClient,
	}
}

// NewWithOAuthToken creates a client using an existing OAuth token.
func NewWithOAuthToken(token, refreshToken string, expiresAt int64) *Client {
	return &Client{
		auth: &StoredAuth{
			Token:        token,
			RefreshToken: refreshToken,
			ExpiresAt:    expiresAt,
		},
		httpClient: defaultHTTPClient,
	}
}

// NewFromStoredAuth loads credentials from ~/.claude-max-client.json.
// Auto-refreshes the token if it expires within 1 hour.
func NewFromStoredAuth() (*Client, error) {
	auth, err := LoadAuth()
	if err != nil {
		return nil, err
	}

	c := &Client{
		auth:       auth,
		httpClient: defaultHTTPClient,
	}

	if err := c.EnsureValidToken(); err != nil {
		return nil, err
	}

	return c, nil
}

// EnsureValidToken refreshes the OAuth token if it's expiring within 1 hour.
func (c *Client) EnsureValidToken() error {
	if c.auth.APIKey != "" {
		return nil
	}
	if c.auth.Token == "" || c.auth.RefreshToken == "" {
		return nil
	}
	if c.auth.ExpiresAt >= time.Now().Add(time.Hour).UnixMilli() {
		return nil
	}
	newAuth, err := RefreshToken(c.auth.RefreshToken)
	if err != nil {
		return fmt.Errorf("token refresh: %w", err)
	}
	*c.auth = *newAuth
	if err := SaveAuth(c.auth); err != nil {
		return fmt.Errorf("save refreshed token: %w", err)
	}
	return nil
}

// ClearHistory resets the conversation context.
func (c *Client) ClearHistory() {
	c.History = nil
}

// Send sends a message and returns the response.
// It appends both the user message and assistant response to History.
// Pass nil for opts to use defaults.
// When opts.Attachments is set, files are included as multimodal content blocks.
func (c *Client) Send(ctx context.Context, prompt string, opts *SendOptions) (*Response, error) {
	if opts == nil {
		opts = &SendOptions{}
	}
	if opts.Model == "" {
		opts.Model = DefaultModel
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 4096
	}

	c.History = append(c.History, Message{Role: "user", Content: prompt})

	var body []byte
	var err error

	if len(opts.Attachments) > 0 {
		// Build multimodal request with content blocks for the current message
		contentBlocks, blockErr := buildContentBlocks(prompt, opts.Attachments)
		if blockErr != nil {
			return nil, fmt.Errorf("build content blocks: %w", blockErr)
		}

		// Convert history to multimodal messages — prior messages are text-only,
		// only the current (last) message gets content blocks
		messages := make([]multimodalMessage, 0, len(c.History))
		for i, msg := range c.History {
			if i == len(c.History)-1 {
				// Last message (current) — use content blocks
				messages = append(messages, multimodalMessage{
					Role:    msg.Role,
					Content: contentBlocks,
				})
			} else {
				messages = append(messages, multimodalMessage{
					Role:    msg.Role,
					Content: msg.Content,
				})
			}
		}

		req := multimodalRequest{
			Model:     opts.Model,
			MaxTokens: opts.MaxTokens,
			Messages:  messages,
			Stream:    opts.Stream,
			System:    opts.System,
		}
		body, err = json.Marshal(req)
	} else {
		req := MessagesRequest{
			Model:     opts.Model,
			MaxTokens: opts.MaxTokens,
			Messages:  c.History,
			Stream:    opts.Stream,
			System:    opts.System,
		}
		body, err = json.Marshal(req)
	}
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	isOAuth := c.auth.APIKey == "" && c.auth.Token != ""
	endpoint := AnthropicAPIHost + "/v1/messages"
	if isOAuth {
		endpoint += "?beta=true"
	}

	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-version", AnthropicAPIVersion)
		if c.auth.APIKey != "" {
			httpReq.Header.Set("x-api-key", c.auth.APIKey)
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+c.auth.Token)
			httpReq.Header.Set("anthropic-beta", strings.Join([]string{
				"claude-code-20250219", OAuthBetaHeader, "prompt-caching-scope-2026-01-05",
			}, ","))
		}
		return httpReq, nil
	}

	resp, err := doWithRetry(ctx, c.httpClient, buildReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// TEMP: dump rate limit headers
	for k, v := range resp.Header {
		kl := strings.ToLower(k)
		if strings.Contains(kl, "ratelimit") || strings.Contains(kl, "rate-limit") || strings.Contains(kl, "retry") || strings.Contains(kl, "x-anthropic") {
			log.Printf("[anthropic-headers] %s: %v", k, v)
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result *Response
	if opts.Stream {
		result, err = c.handleStream(resp.Body, opts.OnDelta)
	} else {
		result, err = c.handleResponse(resp.Body)
	}
	if err != nil {
		return nil, err
	}

	c.History = append(c.History, Message{Role: "assistant", Content: result.Text})
	return result, nil
}

func (c *Client) handleResponse(body io.Reader) (*Response, error) {
	var msgResp MessagesResponse
	if err := json.NewDecoder(body).Decode(&msgResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var parts []string
	for _, block := range msgResp.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}

	return &Response{
		Text:         strings.Join(parts, ""),
		Model:        msgResp.Model,
		InputTokens:  msgResp.Usage.InputTokens,
		OutputTokens: msgResp.Usage.OutputTokens,
		StopReason:   msgResp.StopReason,
	}, nil
}

func (c *Client) handleStream(body io.Reader, onDelta func(string)) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var collected strings.Builder
	var result Response

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event StreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_delta":
			var delta StreamDelta
			if err := json.Unmarshal(event.Delta, &delta); err == nil && delta.Type == "text_delta" {
				collected.WriteString(delta.Text)
				if onDelta != nil {
					onDelta(delta.Text)
				}
			}
		case "message_start":
			var msg struct {
				Message struct {
					Model string `json:"model"`
				} `json:"message"`
			}
			if err := json.Unmarshal(event.Delta, &msg); err == nil {
				result.Model = msg.Message.Model
			}
		case "message_delta":
			var delta struct {
				StopReason string `json:"stop_reason"`
				Usage      struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(event.Delta, &delta); err == nil {
				result.StopReason = delta.StopReason
				result.OutputTokens = delta.Usage.OutputTokens
			}
		}
	}
	result.Text = collected.String()
	return &result, scanner.Err()
}

// --- Token Storage ---

// TokenFilePath returns the path to the token storage file.
func TokenFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, tokenFileName), nil
}

// LoadAuth reads stored credentials from disk.
func LoadAuth() (*StoredAuth, error) {
	path, err := TokenFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (run --login first)", tokenFileName, err)
	}
	var auth StoredAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parse %s: %w", tokenFileName, err)
	}
	return &auth, nil
}

// SaveAuth persists credentials to disk.
func SaveAuth(auth *StoredAuth) error {
	path, err := TokenFilePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// RefreshToken exchanges a refresh token for new credentials.
func RefreshToken(refreshTok string) (*StoredAuth, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     oauthClientID,
		"refresh_token": refreshTok,
		"scope":         oauthScope,
	})

	req, _ := http.NewRequest("POST", oauthTokenURL, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", AnthropicAPIVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh failed %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}

	return &StoredAuth{
		Token:        result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().UnixMilli() + result.ExpiresIn*1000,
	}, nil
}
