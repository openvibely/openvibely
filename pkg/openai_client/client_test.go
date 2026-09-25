package openaiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpretry"
)

func TestParseStreamingResponseRejectsMissingTerminalEvent(t *testing.T) {
	_, err := parseStreamingResponse(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), nil, false)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
}

func TestResponsesWebSocketHandshakePreservesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	_, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{"input": []any{}}, false, responsesWebsocketStreamOptions{})
	var responseErr *httpretry.ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("error = %v, want ResponseError", err)
	}
	if responseErr.StatusCode != http.StatusTooManyRequests || responseErr.Header.Get("Retry-After") != "7" {
		t.Fatalf("status/Retry-After = %d/%q, want 429/7", responseErr.StatusCode, responseErr.Header.Get("Retry-After"))
	}
}

func TestSendRetriesResponseBodyTimeoutBeforeOutput(t *testing.T) {
	attempts := 0
	client := NewWithAPIKey("sk-test")
	client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		body := io.ReadCloser(failingCompletionsBody{})
		if attempts == 2 {
			body = io.NopCloser(strings.NewReader(`{"model":"test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || resp.Text != "ok" {
		t.Fatalf("attempts/text = %d/%q, want 2/ok", attempts, resp.Text)
	}
}

func TestRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/x-www-form-urlencoded") {
			t.Fatalf("unexpected content-type: %s", got)
		}
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse form body: %v", err)
		}
		if values.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", values.Get("grant_type"))
		}
		if values.Get("refresh_token") != "old-refresh" {
			t.Fatalf("refresh_token = %q, want old-refresh", values.Get("refresh_token"))
		}
		if values.Get("client_id") != openAIOAuthClientID {
			t.Fatalf("client_id = %q, want %q", values.Get("client_id"), openAIOAuthClientID)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`))
	}))
	defer srv.Close()

	oldURL := OpenAIOAuthTokenURL
	OpenAIOAuthTokenURL = srv.URL
	defer func() { OpenAIOAuthTokenURL = oldURL }()

	auth, err := RefreshToken("old-refresh")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if auth.Token != "new-access" {
		t.Fatalf("Token = %q, want new-access", auth.Token)
	}
	if auth.RefreshToken != "new-refresh" {
		t.Fatalf("RefreshToken = %q, want new-refresh", auth.RefreshToken)
	}
	if auth.ExpiresAt <= 0 {
		t.Fatalf("ExpiresAt should be > 0, got %d", auth.ExpiresAt)
	}
}

func TestRefreshTokenFailureRedactsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","refresh_token":"secret-refresh","access_token":"secret-access"}`))
	}))
	defer srv.Close()

	oldURL := OpenAIOAuthTokenURL
	OpenAIOAuthTokenURL = srv.URL
	defer func() { OpenAIOAuthTokenURL = oldURL }()

	_, err := RefreshToken("secret-refresh")
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	if !errors.Is(err, ErrOAuthReauthenticationRequired) {
		t.Fatalf("invalid_grant error = %v, want reauthentication classification", err)
	}
	message := err.Error()
	for _, forbidden := range []string{"secret-refresh", "secret-access", "invalid_grant", "refresh_token", "access_token"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("refresh failure leaked %q in error: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "HTTP 401") {
		t.Fatalf("refresh failure should include status code, got %s", message)
	}
}

func TestSend_NonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("expected /responses, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("authorization header = %q", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["model"] != "gpt-5.3-codex" {
			t.Fatalf("model = %v, want gpt-5.3-codex", body["model"])
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "xhigh" {
			t.Fatalf("reasoning.effort = %v, want xhigh", reasoning["effort"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_test",
			"object":"response",
			"created_at":1731000000,
			"status":"completed",
			"model":"gpt-5.3-codex",
			"output":[
				{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[
						{"type":"output_text","text":"Hello from OpenAI"}
					]
				}
			],
			"usage":{
				"input_tokens":10,
				"input_tokens_details":{"cached_tokens":0},
				"output_tokens":5,
				"output_tokens_details":{"reasoning_tokens":0}
			}
		}`))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{
		Model:           "gpt-5.3-codex",
		MaxOutputTokens: 128,
		ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "Hello from OpenAI" {
		t.Fatalf("Text = %q, want %q", resp.Text, "Hello from OpenAI")
	}
	if resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Fatalf("tokens = (%d, %d), want (10, 5)", resp.InputTokens, resp.OutputTokens)
	}
	if len(client.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(client.History))
	}
}

func TestBuildInputItems_WithImageAttachment(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "img.png")
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4E, 0x47}, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	att, err := NewFileAttachment(path)
	if err != nil {
		t.Fatalf("NewFileAttachment: %v", err)
	}

	items, err := buildInputItems([]Message{{Role: "user", Content: "check image"}}, []*FileAttachment{att})
	if err != nil {
		t.Fatalf("buildInputItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}

	data, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"type":"input_image"`) {
		t.Fatalf("expected input_image block in payload, got: %s", body)
	}
	if !strings.Contains(body, `"image_url":"data:image/png;base64,`) {
		t.Fatalf("expected image data URL in payload, got: %s", body)
	}
}

func TestSend_OAuthUsesChatGPTBackendAndAccountHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/responses" {
			t.Fatalf("expected /responses, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testOAuthJWT("org_test_123") {
			t.Fatalf("authorization header = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_test_123" {
			t.Fatalf("ChatGPT-Account-ID header = %q, want org_test_123", got)
		}
		if got := r.Header.Get("originator"); got != "codex_cli_rs" {
			t.Fatalf("originator header = %q, want codex_cli_rs", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "0.144.0" {
			t.Fatalf("client_version query = %q, want 0.144.0", got)
		}
		if got := r.Header.Get("session_id"); got == "" {
			t.Fatalf("session_id header should be set")
		}
		if got := r.Header.Get("x-client-request-id"); got == "" {
			t.Fatalf("x-client-request-id header should be set")
		}
		if r.Header.Get("x-client-request-id") != r.Header.Get("session_id") {
			t.Fatalf("x-client-request-id and session_id should match")
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept header = %q, want text/event-stream", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Fatalf("max_output_tokens should be omitted for ChatGPT OAuth requests")
		}
		if got, ok := body["store"].(bool); !ok || got {
			t.Fatalf("store = %#v, want false", body["store"])
		}
		include, _ := body["include"].([]any)
		if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("include = %#v, want reasoning.encrypted_content", body["include"])
		}
		if got, ok := body["instructions"].(string); !ok || strings.TrimSpace(got) == "" {
			t.Fatalf("instructions must be present and non-empty, got %#v", body["instructions"])
		}
		if got, ok := body["stream"].(bool); !ok || !got {
			t.Fatalf("stream = %#v, want true", body["stream"])
		}
		if got := body["model"]; got != "gpt-5.5" {
			t.Fatalf("model = %#v, want gpt-5.5", got)
		}
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "" {
			t.Fatalf("responses-lite header = %q, want omitted for HTTP SSE", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"content_index\":0,\"item_id\":\"msg_1\",\"output_index\":0}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":1,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	// Ensure OAuth token is treated as valid and no refresh request is made.
	client := NewWithOAuthToken(testOAuthJWT("org_test_123"), "refresh-token", time.Now().Add(2*time.Hour).UnixMilli(), "org_test_123")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{
		Model: "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestExtractChatGPTAccountID(t *testing.T) {
	token := testOAuthJWT("org_abc")
	got := ExtractChatGPTAccountID(token)
	if got != "org_abc" {
		t.Fatalf("extractChatGPTAccountID = %q, want org_abc", got)
	}
	if got := ExtractChatGPTAccountID("not-a-jwt"); got != "" {
		t.Fatalf("extractChatGPTAccountID invalid token = %q, want empty", got)
	}
}

func TestSend_OAuthLunaUsesResponsesLiteWebSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != openAIResponsesWebsocketBeta {
			t.Fatalf("OpenAI-Beta = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_test" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		if r.Header.Get("session-id") == "" || r.Header.Get("thread-id") == "" {
			t.Fatalf("missing Codex session headers")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var request map[string]any
		if err := json.Unmarshal(data, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request["type"] != "response.create" || request["model"] != "gpt-5.6-luna" {
			t.Errorf("request type/model = %v/%v", request["type"], request["model"])
		}
		if _, ok := request["tools"]; ok {
			t.Error("top-level tools must be omitted for Responses Lite")
		}
		input, _ := request["input"].([]any)
		if len(input) < 3 || input[0].(map[string]any)["type"] != "additional_tools" || input[1].(map[string]any)["role"] != "developer" {
			t.Errorf("unexpected Lite input prefix: %#v", input)
		}
		metadata, _ := request["client_metadata"].(map[string]any)
		if metadata[responsesLiteMetadataKey] != "true" {
			t.Errorf("Responses Lite metadata = %#v", metadata)
		}
		userMessage, _ := input[len(input)-1].(map[string]any)
		content, _ := userMessage["content"].([]any)
		image, _ := content[1].(map[string]any)
		if got := image["detail"]; got != "auto" {
			t.Errorf("OAuth Lite image detail = %#v, want auto", got)
		}
		events := []string{
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"model":"gpt-5.6-luna","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`,
		}
		for _, event := range events {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Errorf("write event: %v", err)
				return
			}
		}
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{
		Model:       "gpt-5.6-luna",
		Attachments: []*FileAttachment{{FileName: "test.png", MediaType: "image/png", Data: []byte("png")}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" || resp.Model != "gpt-5.6-luna" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestResponsesLiteWebSocketModels(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", " GPT-6-SOL "} {
		if !isResponsesLiteWebsocketModel(model) {
			t.Errorf("isResponsesLiteWebsocketModel(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-5.5", "gpt-5.4", ""} {
		if isResponsesLiteWebsocketModel(model) {
			t.Errorf("isResponsesLiteWebsocketModel(%q) = true, want false", model)
		}
	}
}

func TestGPT6WorkflowModels(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", " GPT-6-SOL "} {
		if !isGPT6WorkflowModel(model) {
			t.Errorf("isGPT6WorkflowModel(%q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.5", "gpt-6", ""} {
		if isGPT6WorkflowModel(model) {
			t.Errorf("isGPT6WorkflowModel(%q) = true, want false", model)
		}
	}
}

func TestSend_GPT6SolLunaAPIKeyUsesResponsesLiteWebSocket(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("OpenAI-Beta"); got != openAIResponsesWebsocketBeta {
					t.Errorf("OpenAI-Beta = %q", got)
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept websocket: %v", err)
					return
				}
				defer conn.Close(websocket.StatusNormalClosure, "")
				_, data, err := conn.Read(r.Context())
				if err != nil {
					t.Errorf("read request: %v", err)
					return
				}
				var request map[string]any
				if err := json.Unmarshal(data, &request); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				if request["model"] != model || request["type"] != "response.create" {
					t.Errorf("request type/model = %v/%v", request["type"], request["model"])
				}
				metadata, _ := request["client_metadata"].(map[string]any)
				if metadata[responsesLiteMetadataKey] != "true" {
					t.Errorf("Responses Lite metadata = %#v", metadata)
				}
				if _, ok := request["max_output_tokens"]; ok {
					t.Errorf("Responses Lite request retained max_output_tokens")
				}
				completed := fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`, model)
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer srv.Close()

			original := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL + "/v1/"
			defer func() { OpenAIAPIBaseURL = original }()

			client := NewWithAPIKey("sk-test")
			resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: model, ReasoningEffort: "medium"})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if resp.Text != "ok" || resp.Model != model {
				t.Fatalf("response = %#v", resp)
			}
		})
	}
}

func TestSendAgentic_GPT6SolLunaAPIKeyWebSearchUsesResponsesLiteNamespace(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6-luna"} {
		t.Run(model, func(t *testing.T) {
			var request map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/responses" {
					t.Fatalf("request = %s %s, want GET /v1/responses", r.Method, r.URL.Path)
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("accept websocket: %v", err)
					return
				}
				defer conn.Close(websocket.StatusNormalClosure, "")
				_, data, err := conn.Read(r.Context())
				if err != nil {
					t.Errorf("read request: %v", err)
					return
				}
				if err := json.Unmarshal(data, &request); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				completed := fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`, model)
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
					t.Errorf("write completed event: %v", err)
				}
			}))
			defer srv.Close()

			original := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL + "/v1/"
			defer func() { OpenAIAPIBaseURL = original }()

			client := NewWithAPIKey("sk-test")
			resp, err := client.SendAgentic(context.Background(), "Search", &AgenticOptions{
				Model: model, ReasoningEffort: "medium", DisableTools: true,
				WebSearchEnabled: true, MaxTurns: 1,
			})
			if err != nil {
				t.Fatalf("SendAgentic: %v", err)
			}
			if resp.Text != "ok" {
				t.Fatalf("response text = %q, want ok", resp.Text)
			}
			if _, ok := request["tools"]; ok {
				t.Fatalf("Responses Lite request unexpectedly retained top-level tools: %#v", request["tools"])
			}
			input, _ := request["input"].([]any)
			additional, _ := input[0].(map[string]any)
			tools, _ := additional["tools"].([]any)
			foundWebRun := false
			for _, raw := range tools {
				tool, _ := raw.(map[string]any)
				if tool["type"] == "namespace" && tool["name"] == "web" {
					foundWebRun = true
				}
			}
			if !foundWebRun {
				t.Fatalf("Responses Lite tools omitted web.run: %#v", tools)
			}
		})
	}
}

func TestSetResponsesTransportStateSharesSessionID(t *testing.T) {
	state := NewResponsesTransportState()
	first := NewWithAPIKey("first")
	second := NewWithAPIKey("second")
	first.SetResponsesTransportState(state)
	second.SetResponsesTransportState(state)
	if first.sessionID == "" || first.sessionID != second.sessionID || first.sessionID != state.sessionID {
		t.Fatalf("shared session IDs first=%q second=%q state=%q", first.sessionID, second.sessionID, state.sessionID)
	}
}

func TestBuildResponsesLiteWebsocketPayload_ModelDefaultsAndHostedTools(t *testing.T) {
	for _, tc := range []struct {
		model  string
		effort string
	}{
		{model: "gpt-5.6-sol", effort: "medium"},
		{model: "gpt-5.6-terra", effort: "medium"},
		{model: "gpt-5.6-luna", effort: "medium"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			request := buildResponsesLiteWebsocketPayload(map[string]any{
				"model": tc.model,
				"input": []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_image", "image_url": "data:image/png;base64,AA==", "detail": "auto",
					}},
				}},
				"tools": []any{
					map[string]any{"type": "function", "name": "read_file"},
					map[string]any{"type": "web_search"},
					map[string]any{"type": "image_generation"},
				},
			}, "system", "session")
			reasoning, _ := request["reasoning"].(map[string]any)
			if reasoning["effort"] != tc.effort {
				t.Fatalf("reasoning.effort = %#v, want %q", reasoning["effort"], tc.effort)
			}
			input, _ := request["input"].([]any)
			additionalTools, _ := input[0].(map[string]any)
			tools, _ := additionalTools["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("additional tools = %#v, want only function tool", tools)
			}
			userMessage, _ := input[len(input)-1].(map[string]any)
			content, _ := userMessage["content"].([]any)
			image, _ := content[0].(map[string]any)
			if got := image["detail"]; got != "auto" {
				t.Fatalf("Lite image detail = %#v, want auto: %#v", got, image)
			}
		})
	}
}

func TestBuildResponsesLiteWebsocketPayload_OmitsUnsupportedImageDetails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		detail string
	}{
		{name: "unsupported model", model: "gpt-5.5", detail: "auto"},
		{name: "unselected original detail", model: "gpt-5.6-sol", detail: "original"},
		{name: "unsupported detail value", model: "gpt-5.6-sol", detail: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := buildResponsesLiteWebsocketPayload(map[string]any{
				"model": tc.model,
				"input": []any{map[string]any{
					"type": "message",
					"role": "user",
					"content": []any{map[string]any{
						"type": "input_image", "image_url": "data:image/png;base64,AA==", "detail": tc.detail,
					}},
				}},
			}, "", "session")
			input, _ := request["input"].([]any)
			userMessage, _ := input[1].(map[string]any)
			content, _ := userMessage["content"].([]any)
			image, _ := content[0].(map[string]any)
			if _, ok := image["detail"]; ok {
				t.Fatalf("unsupported Lite image retained detail: %#v", image)
			}
		})
	}
}

func TestSend_APIKeyTerraUsesResponsesLiteWebSocket(t *testing.T) {
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, data, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := json.Unmarshal(data, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		input, _ := request["input"].([]any)
		userMessage, _ := input[len(input)-1].(map[string]any)
		content, _ := userMessage["content"].([]any)
		image, _ := content[1].(map[string]any)
		if got := image["detail"]; got != "auto" {
			t.Errorf("API-key standard image detail = %#v, want auto", got)
		}
		completed := `{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-terra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
			t.Errorf("write completed event: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{
		Model:           "gpt-5.6-terra",
		MaxOutputTokens: 123,
		Attachments:     []*FileAttachment{{FileName: "test.png", MediaType: "image/png", Data: []byte("png")}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
	if _, ok := request["max_output_tokens"]; ok {
		t.Errorf("Responses Lite websocket retained max_output_tokens = %#v", request["max_output_tokens"])
	}
	metadata, _ := request["client_metadata"].(map[string]any)
	if metadata[responsesLiteMetadataKey] != "true" {
		t.Errorf("Responses Lite metadata = %#v", metadata)
	}
}

func TestSend_ResponsesLiteWebSocketAcceptsLargeEvent(t *testing.T) {
	largeText := strings.Repeat("x", 40<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		completed, err := json.Marshal(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"model":  "gpt-5.6-terra",
				"output": []any{map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{map[string]any{
						"type": "output_text",
						"text": largeText,
					}},
				}},
			},
		})
		if err != nil {
			t.Errorf("marshal completed event: %v", err)
			return
		}
		if len(completed) <= 32<<10 {
			t.Fatalf("completed event size = %d, want greater than 32 KiB", len(completed))
		}
		if err := conn.Write(r.Context(), websocket.MessageText, completed); err != nil {
			t.Errorf("write completed event: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: "gpt-5.6-terra"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != largeText {
		t.Fatalf("Text length = %d, want %d", len(resp.Text), len(largeText))
	}
}

func TestSend_ResponsesLiteWebSocketHandshakeFallsBackToHTTPForSession(t *testing.T) {
	var websocketAttempts atomic.Int32
	var httpRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			websocketAttempts.Add(1)
			http.Error(w, "websocket unavailable", http.StatusUpgradeRequired)
			return
		}
		httpRequests.Add(1)
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "true" {
			t.Fatalf("Responses Lite header = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode HTTP fallback request: %v", err)
		}
		if _, ok := request["type"]; ok {
			t.Fatalf("HTTP fallback retained websocket type: %#v", request["type"])
		}
		if _, ok := request["client_metadata"]; ok {
			t.Fatalf("HTTP fallback retained websocket client_metadata")
		}
		input, _ := request["input"].([]any)
		userMessage, _ := input[len(input)-1].(map[string]any)
		content, _ := userMessage["content"].([]any)
		image, _ := content[1].(map[string]any)
		if got := image["detail"]; got != "auto" {
			t.Fatalf("HTTP Lite image detail = %#v, want auto", got)
		}
		_, _ = w.Write([]byte(buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-terra"}}`,
		})))
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	for i := 0; i < 2; i++ {
		resp, err := client.Send(context.Background(), "Hello", &SendOptions{
			Model:       "gpt-5.6-terra",
			Attachments: []*FileAttachment{{FileName: "test.png", MediaType: "image/png", Data: []byte("png")}},
		})
		if err != nil {
			t.Fatalf("Send %d: %v", i+1, err)
		}
		if resp.Text != "ok" {
			t.Fatalf("Send %d text = %q", i+1, resp.Text)
		}
	}
	if got := websocketAttempts.Load(); got != 1 {
		t.Fatalf("websocket attempts = %d, want 1", got)
	}
	if got := httpRequests.Load(); got != 2 {
		t.Fatalf("HTTP fallback requests = %d, want 2", got)
	}
}

func TestOpenResponsesLiteHTTPStream_OAuthPreservesGPT56AutoImageDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_test" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "true" {
			t.Fatalf("Responses Lite header = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode OAuth HTTP fallback request: %v", err)
		}
		input, _ := request["input"].([]any)
		userMessage, _ := input[len(input)-1].(map[string]any)
		content, _ := userMessage["content"].([]any)
		image, _ := content[0].(map[string]any)
		if got := image["detail"]; got != "auto" {
			t.Fatalf("OAuth HTTP Lite image detail = %#v, want auto", got)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken("oauth-token", "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	payload := buildResponsesLiteWebsocketPayload(map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_image", "image_url": "data:image/png;base64,AA==", "detail": "auto",
			}},
		}},
	}, "", client.sessionID)
	body, err := client.openResponsesLiteHTTPStream(context.Background(), payload, true)
	if err != nil {
		t.Fatalf("open OAuth HTTP fallback stream: %v", err)
	}
	body.Close()
}

func TestResponsesLiteOAuthRecoveryErrorUnlocksTransportState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken("expired", "refresh", time.Now().Add(time.Hour).UnixMilli(), "org_test")
	client.SetOAuthUnauthorizedHandler(func(context.Context, string) (OAuthTokens, bool, error) {
		return OAuthTokens{}, false, errors.New("refresh failed")
	})
	payload := buildResponsesLiteWebsocketPayload(map[string]any{"model": "gpt-5.6-luna", "input": []any{}}, "", client.sessionID)
	if _, err := client.openResponsesWebsocketStream(context.Background(), payload, true, responsesWebsocketStreamOptions{Model: "gpt-5.6-luna"}); err == nil {
		t.Fatal("expected OAuth recovery error")
	}
	if !client.responsesTransportState.mu.TryLock() {
		t.Fatal("transport mutex remained locked after OAuth recovery error")
	}
	client.responsesTransportState.mu.Unlock()
}

func TestResponsesLitePayloadRemovesAsyncToolMetadata(t *testing.T) {
	payload := buildResponsesLiteWebsocketPayload(map[string]any{
		"model": "gpt-6-astra",
		"input": []any{},
		"tools": []ToolDefinition{{
			Type:       "function",
			Name:       "memory_view",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Async:      true,
		}},
	}, "", "session-test")

	input, _ := payload["input"].([]any)
	additionalTools, _ := input[0].(map[string]any)
	tools, _ := additionalTools["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	if _, exists := tool["async"]; exists {
		t.Fatalf("Responses Lite tool contains unsupported async metadata: %#v", tool)
	}
}

func TestResponsesLiteClosingStreamBeforeTerminalResetsConnection(t *testing.T) {
	continueServer := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created"}`)); err != nil {
			t.Errorf("write first event: %v", err)
			return
		}
		<-continueServer
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.in_progress"}`))
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	payload := buildResponsesLiteWebsocketPayload(map[string]any{"model": "gpt-5.6-sol", "input": []any{}}, "", client.sessionID)
	body, err := client.openResponsesWebsocketStream(context.Background(), payload, false, responsesWebsocketStreamOptions{Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	buffer := make([]byte, 256)
	if _, err := body.Read(buffer); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	_ = body.Close()
	close(continueServer)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if client.responsesTransportState.mu.TryLock() {
			conn := client.responsesTransportState.conn
			client.responsesTransportState.mu.Unlock()
			if conn == nil {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("connection remained reusable after stream consumer closed early")
}

func TestSend_ResponsesLiteReconnectsStaleReusedWebSocket(t *testing.T) {
	var websocketAttempts atomic.Int32
	var httpRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpRequests.Add(1)
			http.Error(w, "unexpected HTTP fallback", http.StatusInternalServerError)
			return
		}
		attempt := websocketAttempts.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read websocket request: %v", err)
			return
		}
		text := fmt.Sprintf("ok-%d", attempt)
		completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","status":"completed","model":"gpt-5.6-terra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}}`, attempt, text)
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
			t.Errorf("write completed event: %v", err)
			return
		}
		if attempt == 1 {
			_ = conn.Close(websocket.StatusNormalClosure, "idle timeout")
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	for i := 1; i <= 2; i++ {
		resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: "gpt-5.6-terra"})
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if resp.Text != fmt.Sprintf("ok-%d", i) {
			t.Fatalf("Send %d text = %q", i, resp.Text)
		}
	}
	if got := websocketAttempts.Load(); got != 2 {
		t.Fatalf("websocket attempts = %d, want 2", got)
	}
	if got := httpRequests.Load(); got != 0 {
		t.Fatalf("HTTP fallback requests = %d, want 0", got)
	}
}

func TestSend_ResponsesLiteCancellationDoesNotDisableWebSocket(t *testing.T) {
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = "http://127.0.0.1:1/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := NewWithAPIKey("sk-test")
	_, err := client.Send(ctx, "Hello", &SendOptions{Model: "gpt-5.6-sol"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error = %v, want context.Canceled", err)
	}
	if client.responsesTransportState.websocketDisabled.Load() {
		t.Fatal("cancellation disabled Responses WebSocket transport")
	}
}

func TestSend_ResponsesLitePartialOutputFallsBackFromTurnState(t *testing.T) {
	var websocketAttempts atomic.Int32
	var httpRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			websocketAttempts.Add(1)
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept websocket: %v", err)
				return
			}
			if _, _, err := conn.Read(r.Context()); err != nil {
				t.Errorf("read websocket request: %v", err)
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"partial"}`))
			_ = conn.Close(websocket.StatusInternalError, "upstream reset")
			return
		}
		httpRequests.Add(1)
		_, _ = w.Write([]byte(buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"recovered"}`,
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
		})))
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	var output strings.Builder
	resp, err := client.Send(context.Background(), "first", &SendOptions{
		Model:   "gpt-5.6-sol",
		OnDelta: func(text string) { output.WriteString(text) },
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "recovered" {
		t.Fatalf("text = %q, want recovered", resp.Text)
	}
	// The provider response is rebuilt from retry state, while the live stream
	// intentionally preserves text already emitted by the failed attempt.
	if output.String() != strings.Repeat("partial", httpretry.StreamTurnMaxRetries+1)+"recovered" {
		t.Fatalf("unexpected streamed output = %q", output.String())
	}
	if websocketAttempts.Load() != int32(httpretry.StreamTurnMaxRetries+1) || httpRequests.Load() != 1 {
		t.Fatalf("attempts websocket=%d HTTP=%d", websocketAttempts.Load(), httpRequests.Load())
	}
}

func TestSend_OAuthLunaWebSocketFailedEventReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		failed := `{"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"input is too large"}}}`
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(failed)); err != nil {
			t.Errorf("write failed event: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	_, err := client.Send(ctx, "Hello", &SendOptions{Model: "gpt-5.6-luna"})
	if err == nil || !strings.Contains(err.Error(), "input is too large") {
		t.Fatalf("Send error = %v, want backend failure", err)
	}
	if !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Fatalf("Send error = %v, want backend error code", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send waited for context deadline: %v", err)
	}
}

func TestSend_OAuthGPT56Live(t *testing.T) {
	if os.Getenv("OPENVIBELY_LIVE_OPENAI") != "1" {
		t.Skip("set OPENVIBELY_LIVE_OPENAI=1 to run")
	}
	token := os.Getenv("OPENVIBELY_LIVE_OPENAI_TOKEN")
	if token == "" {
		t.Fatal("OPENVIBELY_LIVE_OPENAI_TOKEN is required")
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			client := NewWithOAuthToken(
				token,
				os.Getenv("OPENVIBELY_LIVE_OPENAI_REFRESH_TOKEN"),
				time.Now().Add(2*time.Hour).UnixMilli(),
				os.Getenv("OPENVIBELY_LIVE_OPENAI_ACCOUNT_ID"),
			)
			resp, err := client.Send(context.Background(), "Reply with only the number: 4+4", &SendOptions{
				Model: model,
			})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if strings.TrimSpace(resp.Text) != "8" {
				t.Fatalf("Text = %q, want 8", resp.Text)
			}
		})
	}

	agenticClient := NewWithOAuthToken(
		token,
		os.Getenv("OPENVIBELY_LIVE_OPENAI_REFRESH_TOKEN"),
		time.Now().Add(2*time.Hour).UnixMilli(),
		os.Getenv("OPENVIBELY_LIVE_OPENAI_ACCOUNT_ID"),
	)
	agenticResp, err := agenticClient.SendAgentic(context.Background(), "Reply with only the number: 4+4", &AgenticOptions{
		Model:            "gpt-5.6-luna",
		ReasoningEffort:  "medium",
		DisableTools:     true,
		WebSearchEnabled: true,
		MaxTurns:         1,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if strings.TrimSpace(agenticResp.Text) != "8" {
		t.Fatalf("agentic Text = %q, want 8", agenticResp.Text)
	}
}

func TestNormalizeReasoningEffort_PreservesNone(t *testing.T) {
	if got := normalizeReasoningEffort(" NONE "); got != "none" {
		t.Fatalf("normalizeReasoningEffort = %q, want none", got)
	}
}

func TestClearHistoryClearsCompletionsReasoningState(t *testing.T) {
	client := NewWithAPIKey("test-key")
	client.SetCompletionsHistory([]CompletionsHistoryMessage{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", ReasoningContent: "private thought"},
	})
	client.lastCompletionsReasoning = "latest private thought"
	client.lastCompletionsTranscript = []CompletionsHistoryMessage{{Role: "assistant", Content: "answer"}}

	client.ClearHistory()

	if client.History != nil {
		t.Fatalf("History = %#v, want nil", client.History)
	}
	if client.completionsHistory != nil {
		t.Fatalf("completionsHistory = %#v, want nil", client.completionsHistory)
	}
	if got := client.LastCompletionsReasoningContent(); got != "" {
		t.Fatalf("LastCompletionsReasoningContent() = %q, want empty", got)
	}
	if got := client.LastCompletionsTranscript(); got != nil {
		t.Fatalf("LastCompletionsTranscript() = %#v, want nil", got)
	}
}

func TestSend_StreamingPreservesSpacesInDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\",\"content_index\":0,\"item_id\":\"msg_1\",\"output_index\":0}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\" world\",\"content_index\":0,\"item_id\":\"msg_1\",\"output_index\":0}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello world\"}]}],\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":2,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var streamed strings.Builder
	resp, err := client.Send(context.Background(), "Hi", &SendOptions{
		Model:  "gpt-5.3-codex",
		Stream: true,
		OnDelta: func(text string) {
			streamed.WriteString(text)
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := streamed.String(); got != "Hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "Hello world")
	}
	if got := resp.Text; got != "Hello world" {
		t.Fatalf("response text = %q, want %q", got, "Hello world")
	}
}

func TestParseStreamingResponse_StreamsSmallDeltaBeforeCompletion(t *testing.T) {
	pr, pw := io.Pipe()
	textCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		_, err := parseStreamingResponse(pr, func(text string) {
			select {
			case textCh <- text:
			default:
			}
		}, false)
		errCh <- err
	}()

	_, _ = pw.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))

	select {
	case got := <-textCh:
		if got != "ok" {
			t.Fatalf("unexpected delta: %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("small delta did not stream before completion")
	}

	_, _ = pw.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	_ = pw.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("parseStreamingResponse: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parser did not finish")
	}
}

func TestParseStreamingResponse_StreamsTextBeforeToolDone(t *testing.T) {
	pr, pw := io.Pipe()
	textCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		_, err := parseStreamingResponse(pr, func(text string) {
			select {
			case textCh <- text:
			default:
			}
		}, false)
		errCh <- err
	}()

	_, _ = pw.Write([]byte(strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"I'll inspect that now."}`,
		``,
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		``,
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"file_path\":\"main.go\"}"}`,
		``,
	}, "\n")))

	select {
	case got := <-textCh:
		if got != "I'll inspect that now." {
			t.Fatalf("unexpected delta: %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("text did not stream before tool done")
	}

	_, _ = pw.Write([]byte(strings.Join([]string{
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"file_path\":\"main.go\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
	}, "\n")))
	_ = pw.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("parseStreamingResponse: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parser did not finish")
	}
}

func TestSend_StreamingFormatsToolItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_item.done\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"file_path\\\":\\\"internal/service/llm_service.go\\\"}\"}}\n\n" +
				"event: response.output_item.done\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call_output\",\"call_id\":\"call_1\",\"output\":\"read success\"}}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":2,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var streamed strings.Builder
	resp, err := client.Send(context.Background(), "Hi", &SendOptions{
		Model:  "gpt-5.3-codex",
		Stream: true,
		OnDelta: func(text string) {
			streamed.WriteString(text)
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := streamed.String()
	if !strings.Contains(got, "[Using tool: Read | llm_service.go]") {
		t.Fatalf("streamed output missing tool marker: %q", got)
	}
	if !strings.Contains(got, "[Tool Read done]") {
		t.Fatalf("streamed output missing tool result marker: %q", got)
	}
	if !strings.Contains(resp.Text, "[Using tool: Read | llm_service.go]") {
		t.Fatalf("response text missing tool marker: %q", resp.Text)
	}
}

func TestSend_StreamingKeepsPseudoToolTextPlain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"I\\u2019ll inspect the repo.\"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"tool\\\":\\\"list_files\\\",\\\"path\\\":\\\".\\\"}\"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"bash -lc 'ls -la'\"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"<tool name=\\\"bash\\\">ls -la</tool>\"}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":2,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var streamed strings.Builder
	resp, err := client.Send(context.Background(), "Hi", &SendOptions{
		Model:  "gpt-5.3-codex",
		Stream: true,
		OnDelta: func(text string) {
			streamed.WriteString(text)
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := streamed.String()
	if !strings.Contains(got, "{\"tool\":\"list_files\",\"path\":\".\"}") {
		t.Fatalf("streamed output missing plain JSON-like text: %q", got)
	}
	if !strings.Contains(got, "bash -lc 'ls -la'") {
		t.Fatalf("streamed output missing plain bash-like text: %q", got)
	}
	if !strings.Contains(got, "<tool name=\"bash\">ls -la</tool>") {
		t.Fatalf("streamed output missing plain XML-like text: %q", got)
	}
	if strings.Contains(got, "[Using tool:") {
		t.Fatalf("pseudo-tool text should not create tool markers: %q", got)
	}
	if resp.Text != got {
		t.Fatalf("response text mismatch: got %q, streamed %q", resp.Text, got)
	}
}

func TestOpenAITextStreamEmitter_EmitsImmediately(t *testing.T) {
	var emitted []string
	emitter := newOpenAITextStreamEmitter(func(text string) {
		emitted = append(emitted, text)
	})

	delta := "ok"
	emitter.Write(delta)
	if strings.Join(emitted, "") != delta {
		t.Fatalf("delta should emit immediately, got %q", emitted)
	}
}

func TestOpenAITextStreamEmitter_FlushBoundaryIsNoopAfterImmediateEmit(t *testing.T) {
	var emitted []string
	emitter := newOpenAITextStreamEmitter(func(text string) {
		emitted = append(emitted, text)
	})

	emitter.Write("I'll inspect that now.")
	emitter.FlushBoundary()
	if strings.Join(emitted, "") != "I'll inspect that now." {
		t.Fatalf("plain text should already be emitted once, got %q", emitted)
	}
}

func TestOpenAITextStreamEmitter_PseudoToolTextEmitsImmediately(t *testing.T) {
	var emitted []string
	emitter := newOpenAITextStreamEmitter(func(text string) {
		emitted = append(emitted, text)
	})

	emitter.Write(`{"tool":"bash","command":"`)
	emitter.FlushBoundary()
	if strings.Join(emitted, "") != `{"tool":"bash","command":"` {
		t.Fatalf("pseudo-tool text should emit as plain text, got %q", emitted)
	}
}

func TestParseStreamingResponse_OrdersTextBeforeToolMarker(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"I'll inspect that now."}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"read_file","arguments":"{\"file_path\":\"main.go\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
	}, "\n")

	resp, err := parseStreamingResponse(strings.NewReader(stream), nil, false)
	if err != nil {
		t.Fatalf("parseStreamingResponse: %v", err)
	}
	textIdx := strings.Index(resp.Text, "I'll inspect that now.")
	toolIdx := strings.Index(resp.Text, "[Using tool: Read | main.go]")
	if textIdx == -1 || toolIdx == -1 || textIdx > toolIdx {
		t.Fatalf("expected text before tool marker, got %q", resp.Text)
	}
}

func TestOpenAIToolDetailFromArguments_LongBashPreservesLaterContext(t *testing.T) {
	raw := map[string]any{
		"command": "cd /Users/dubee/go/src/github.com/openvibely/openvibely/.worktrees/task_6a40e9f8fefa53ac8d203aa3fd3a70be && rg -n \"openAIToolDetailFromArguments|task thread|chat_shared.templ\" internal pkg web/templates/components/chat_shared.templ",
	}
	got := openAIToolDetailFromArguments("bash", raw)
	if !strings.Contains(got, "chat_shared.templ") {
		t.Fatalf("expected later command context to survive truncation, got %q", got)
	}
}

func TestSend_OAuthDisableToolsSetsToolChoiceNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got := body["tool_choice"]; got != "none" {
			t.Fatalf("tool_choice = %#v, want \"none\"", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\",\"content_index\":0,\"item_id\":\"msg_1\",\"output_index\":0}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":1,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken(testOAuthJWT("org_test_123"), "refresh-token", time.Now().Add(2*time.Hour).UnixMilli(), "org_test_123")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: "gpt-5.3-codex", DisableTools: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestSend_OAuthSuppressToolMarkersStripsFunctionCallMarkers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_item.done\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"name\":\"playwright-ui-ux-reviewer-agent\",\"arguments\":\"{}\"}}\n\n" +
				"event: response.output_text.delta\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"{\\\"name\\\":\\\"A\\\",\\\"description\\\":\\\"B\\\",\\\"system_prompt\\\":\\\"C\\\",\\\"model\\\":\\\"inherit\\\",\\\"tools\\\":[],\\\"skills\\\":[],\\\"mcp_servers\\\":[]}\",\"content_index\":0,\"item_id\":\"msg_1\",\"output_index\":0}\n\n" +
				"event: response.completed\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":1731000000,\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":0},\"output_tokens\":1,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken(testOAuthJWT("org_test_123"), "refresh-token", time.Now().Add(2*time.Hour).UnixMilli(), "org_test_123")
	resp, err := client.Send(context.Background(), "Hello", &SendOptions{Model: "gpt-5.3-codex", DisableTools: true, SuppressToolMarkers: true})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if strings.Contains(resp.Text, "[Using tool:") {
		t.Fatalf("expected no tool markers, got %q", resp.Text)
	}
	if !strings.Contains(resp.Text, "\"system_prompt\":\"C\"") {
		t.Fatalf("expected JSON content, got %q", resp.Text)
	}
}

func TestApplyAuthHeadersSupportsRawCustomAPIKey(t *testing.T) {
	client := NewWithCompatibleAPIKey("secret-key", "https://example.test/v1", "X-API-Key", "")
	req, err := http.NewRequest(http.MethodGet, "https://example.test/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	client.applyAuthHeaders(req, false)
	if got := req.Header.Get("X-API-Key"); got != "secret-key" {
		t.Fatalf("X-API-Key = %q, want raw API key", got)
	}
}

func testOAuthJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return header + "." + payload + "." + signature
}

func TestOpenAIToolMarkersAndTextEmitterHelpers(t *testing.T) {
	longCommand := strings.Repeat("x", openAIToolDetailMaxLen+20) + "]"
	inputs := []struct {
		name string
		args any
		want string
	}{
		{name: "read_file", args: `{"file_path":"/tmp/project/main.go"}`, want: "main.go"},
		{name: "grep_search", args: map[string]any{"pattern": "func Test"}, want: "func Test"},
		{name: "bash", args: map[string]any{"command": longCommand}, want: strings.Repeat("x", openAIToolDetailMaxLen-3) + "..."},
		{name: "custom", args: `{"path":"/tmp/project/custom.txt"}`, want: "custom.txt"},
		{name: "custom", args: `{"command":"run tests"}`, want: "run tests"},
	}
	for _, tt := range inputs {
		if got := openAIToolDetailFromArguments(tt.name, tt.args); got != tt.want {
			t.Fatalf("openAIToolDetailFromArguments(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := openAIFormatUsingToolMarker("shell_command", "go test ./..."); got != "\n[Using tool: Bash | go test ./...]\n" {
		t.Fatalf("using marker = %q", got)
	}
	if got := openAIFormatUsingToolMarker("", ""); got != "\n[Using tool: tool]\n" {
		t.Fatalf("empty marker = %q", got)
	}
	if !strings.Contains(openAIFormatToolResultMarker("web_search", "", ""), "[Tool WebSearch done]\n(no output)") {
		t.Fatalf("empty tool result marker mismatch")
	}
	if !openAIIsMarkerChunk("prefix [Using tool: Bash]") || !openAIIsMarkerChunk("[Tool Bash done]") || openAIIsMarkerChunk("plain text") {
		t.Fatalf("marker chunk detection mismatch")
	}

	var emitted []string
	emitter := newOpenAITextStreamEmitter(func(delta string) { emitted = append(emitted, delta) })
	emitter.Write("")
	emitter.Write("one")
	emitter.Flush()
	emitter.FlushBoundary()
	emitter.Write("two")
	if strings.Join(emitted, ",") != "one,two" {
		t.Fatalf("emitted = %#v", emitted)
	}
}

func TestOpenResponsesWebsocketStream_AstraMidTurnSteeringPendingAfterToolStopCompletesPrimaryResponse(t *testing.T) {
	steerSeen := make(chan map[string]any, 1)
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_original"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		_, steerBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		var steer map[string]any
		if err := json.Unmarshal(steerBytes, &steer); err != nil {
			t.Errorf("decode steer: %v", err)
			return
		}
		steerSeen <- steer
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.accepted","sequence_number":4,"steer":{"id":"steer_pending","previous_response_id":"resp_original"}}`)); err != nil {
			t.Errorf("write steer accepted: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_item.done","item":{"id":"call_1","type":"function_call","name":"read_file","call_id":"call_1","arguments":"{}"}}`)); err != nil {
			t.Errorf("write tool item: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_original","status":"completed","model":"gpt-6-astra"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.pending","sequence_number":12,"steer":{"id":"steer_pending","previous_response_id":"resp_original"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call_1","name":"read_file"}]}`)); err != nil {
			t.Errorf("write steer pending: %v", err)
			return
		}
		_, continuationBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read continuation: %v", err)
			return
		}
		var continuation map[string]any
		if err := json.Unmarshal(continuationBytes, &continuation); err != nil {
			t.Errorf("decode continuation: %v", err)
			return
		}
		if continuation["previous_response_id"] != "resp_original" {
			t.Errorf("continuation previous_response_id = %#v, want resp_original", continuation["previous_response_id"])
			return
		}
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_continued"}}`,
			`{"type":"response.output_text.delta","delta":"continued"}`,
			`{"type":"response.completed","response":{"id":"resp_continued","status":"completed","model":"gpt-6-astra"}}`,
		} {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Errorf("write continuation event: %v", err)
				return
			}
		}
		close(serverDone)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var callbackCalls atomic.Int32
	pending := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(ctx, map[string]any{
		"type":  "response.create",
		"model": "gpt-6-astra",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			if !callbackCalls.CompareAndSwap(0, 1) {
				return nil
			}
			delivery, err := deliver(ctx, "steer now")
			if err == nil {
				pending <- delivery
			}
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	stream := string(raw)
	if strings.Contains(stream, "response.steer.accepted") || strings.Contains(stream, "response.steer.pending") {
		t.Fatalf("control events leaked to SSE parser stream: %s", raw)
	}
	if !strings.Contains(stream, "response.completed") || !strings.Contains(stream, "resp_original") || !strings.Contains(stream, "function_call") {
		t.Fatalf("stream = %s, want original completed response with tool call", raw)
	}
	select {
	case steer := <-steerSeen:
		if steer["type"] != "response.steer" || steer["previous_response_id"] != "resp_original" || steer["input"] != "steer now" {
			t.Fatalf("steer event = %#v", steer)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive response.steer")
	}
	select {
	case delivery := <-pending:
		if delivery.Status != AstraSteeringPending || delivery.SteeringID != "steer_pending" || delivery.PreviousResponseID != "resp_original" || delivery.ResponseID != "" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe pending state")
	}
	toolCall := map[string]any{"id": "call_1", "type": "function_call", "name": "read_file", "call_id": "call_1", "arguments": "{}"}
	continuation, err := client.openResponsesWebsocketStream(ctx, map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{
			toolCall,
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
		},
	}, false, responsesWebsocketStreamOptions{Model: "gpt-6-astra"})
	if err != nil {
		t.Fatalf("open continuation stream: %v", err)
	}
	continuedRaw, err := io.ReadAll(continuation)
	if err != nil {
		t.Fatalf("read continuation stream: %v", err)
	}
	_ = continuation.Close()
	if !strings.Contains(string(continuedRaw), "continued") {
		t.Fatalf("continuation stream = %s, want continued output", continuedRaw)
	}
	records := client.responsesTransportState.SteeringDeliveries()
	if len(records) == 0 || records[len(records)-1].Status != AstraSteeringAccepted || records[len(records)-1].ResponseID != "resp_continued" {
		t.Fatalf("steering records = %#v, want successor commit", records)
	}
	if unresolved := client.responsesTransportState.takeUnresolvedAstraSteering(); unresolved != nil {
		t.Fatalf("pending steering was not committed: %v", unresolved)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not send pending event")
	}
}

func TestOpenResponsesWebsocketStream_AstraPendingSteeringCanFailBeforeSuccessor(t *testing.T) {
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		defer close(serverDone)
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_original"}}`,
		} {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Errorf("write initial event: %v", err)
				return
			}
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		for _, event := range []string{
			`{"type":"response.steer.accepted","steer":{"id":"steer_pending_failure","previous_response_id":"resp_original"}}`,
			`{"type":"response.output_item.done","item":{"id":"call_1","type":"function_call","name":"read_file","call_id":"call_1","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"id":"resp_original","status":"completed","model":"gpt-6-astra"}}`,
			`{"type":"response.steer.pending","steer":{"id":"steer_pending_failure","previous_response_id":"resp_original"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call_1"}]}`,
		} {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Errorf("write pending sequence: %v", err)
				return
			}
		}

		_, continuationBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read continuation: %v", err)
			return
		}
		var continuation map[string]any
		if err := json.Unmarshal(continuationBytes, &continuation); err != nil {
			t.Errorf("decode continuation: %v", err)
			return
		}
		if continuation["previous_response_id"] != "resp_original" {
			t.Errorf("continuation previous_response_id = %#v, want resp_original", continuation["previous_response_id"])
			return
		}
		for _, event := range []string{
			`{"type":"response.steer.failed","steer":{"id":"steer_pending_failure","previous_response_id":"resp_original"},"error":{"code":"server_error","message":"could not apply queued steering"}}`,
			`{"type":"response.created","response":{"id":"resp_tool_continuation"}}`,
			`{"type":"response.output_text.delta","delta":"continued"}`,
			`{"type":"response.completed","response":{"id":"resp_tool_continuation","status":"completed","model":"gpt-6-astra"}}`,
		} {
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Errorf("write failure sequence: %v", err)
				return
			}
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	pending := make(chan AstraSteeringDelivery, 1)
	first, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			delivery, err := deliver(ctx, "change course")
			pending <- delivery
			return err
		},
	})
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	if _, err := io.ReadAll(first); err != nil {
		t.Fatalf("read first stream: %v", err)
	}
	_ = first.Close()
	select {
	case delivery := <-pending:
		if delivery.Status != AstraSteeringPending {
			t.Fatalf("delivery = %#v, want pending", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe pending state")
	}

	toolCall := map[string]any{"id": "call_1", "type": "function_call", "name": "read_file", "call_id": "call_1", "arguments": "{}"}
	second, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{
			toolCall,
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
		},
	}, false, responsesWebsocketStreamOptions{Model: "gpt-6-astra"})
	if err != nil {
		t.Fatalf("open continuation stream: %v", err)
	}
	if _, err := io.ReadAll(second); err != nil {
		t.Fatalf("read continuation stream: %v", err)
	}
	_ = second.Close()
	if err := client.responsesTransportState.takeAstraSteeringFailure(); err == nil || !strings.Contains(err.Error(), "could not apply queued steering") {
		t.Fatalf("queued steering failure = %v", err)
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("server did not finish pending failure sequence")
	}
}

func TestResponsesTransportState_ResetPreservesPendingAstraSteeringAsAmbiguous(t *testing.T) {
	state := NewResponsesTransportState()
	state.pendingAstraSteering = map[string]ResponsesSteeringDelivery{
		"steer_reset": {
			Status:             AstraSteeringPending,
			SteeringID:         "steer_reset",
			PreviousResponseID: "resp_original",
		},
	}
	state.mu.Lock()
	state.resetConnectionLocked()
	state.mu.Unlock()
	if len(state.pendingAstraSteering) != 0 {
		t.Fatalf("pending steering survived reset: %#v", state.pendingAstraSteering)
	}
	if err := state.takeAstraSteeringFailure(); err != nil {
		t.Fatalf("reset treated server-owned steering as failed: %v", err)
	}
	ambiguous := state.takeAstraSteeringAmbiguous()
	if len(ambiguous) != 1 || ambiguous[0].SteeringID != "steer_reset" || ambiguous[0].Status != AstraSteeringAmbiguous {
		t.Fatalf("reset ambiguous steering = %#v", ambiguous)
	}
	if err := state.takeUnresolvedAstraSteering(); err != nil {
		t.Fatalf("reset left unresolved steering: %v", err)
	}
}

func TestOpenResponsesWebsocketStream_AstraMidTurnSteeringRejectedDoesNotClaimDelivery(t *testing.T) {
	steerSeen := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_rejected"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		_, steerBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		var steer map[string]any
		if err := json.Unmarshal(steerBytes, &steer); err != nil {
			t.Errorf("decode steer: %v", err)
			return
		}
		steerSeen <- steer
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.failed","sequence_number":5,"steer":{"id":"steer_rejected","previous_response_id":"resp_rejected","input":"try rejected steering"},"error":{"code":"steering_not_supported","message":"not steerable"}}`)); err != nil {
			t.Errorf("write steer failed: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"primary"}`)); err != nil {
			t.Errorf("write delta: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_rejected","status":"completed","model":"gpt-6-astra"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
			return
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	var callbackCalls atomic.Int32
	failed := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type":  "response.create",
		"model": "gpt-6-astra",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			if !callbackCalls.CompareAndSwap(0, 1) {
				return nil
			}
			delivery, err := deliver(ctx, "try rejected steering")
			failed <- delivery
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	stream := string(raw)
	if strings.Contains(stream, "response.steer.failed") || strings.Contains(stream, "not steerable") {
		t.Fatalf("failed steering control event leaked to SSE parser stream: %s", raw)
	}
	if !strings.Contains(stream, "primary") || !strings.Contains(stream, "response.completed") {
		t.Fatalf("stream = %s, want primary response completion", raw)
	}
	select {
	case steer := <-steerSeen:
		if steer["type"] != "response.steer" || steer["previous_response_id"] != "resp_rejected" || steer["input"] != "try rejected steering" {
			t.Fatalf("steer event = %#v", steer)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive response.steer")
	}
	select {
	case delivery := <-failed:
		if delivery.Status != AstraSteeringFailed || delivery.SteeringID != "steer_rejected" || delivery.PreviousResponseID != "resp_rejected" || delivery.Error != "not steerable" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe rejection")
	}
	records := client.responsesTransportState.SteeringDeliveries()
	if len(records) < 2 || records[len(records)-1].Status != AstraSteeringFailed {
		t.Fatalf("steering records = %#v", records)
	}
}

func TestOpenResponsesWebsocketStream_AstraAcceptedSteeringCanFailBeforeCommit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_late_failure"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.accepted","sequence_number":4,"steer":{"id":"steer_late_failure","previous_response_id":"resp_late_failure"}}`)); err != nil {
			t.Errorf("write steer accepted: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.failed","sequence_number":5,"steer":{"id":"steer_late_failure","previous_response_id":"resp_late_failure","input":"change course"},"error":{"code":"server_error","message":"could not commit"}}`)); err != nil {
			t.Errorf("write steer failed: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_late_failure","status":"completed","model":"gpt-6-astra"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	deliveries := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			delivery, err := deliver(ctx, "change course")
			deliveries <- delivery
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	select {
	case delivery := <-deliveries:
		if delivery.Status != AstraSteeringFailed || delivery.SteeringID != "steer_late_failure" || delivery.PreviousResponseID != "resp_late_failure" || delivery.Error != "could not commit" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe the post-acceptance failure")
	}
}

func TestOpenResponsesWebsocketStream_AstraMidTurnSteeringDisconnectReturnsAmbiguous(t *testing.T) {
	steerSeen := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_disconnect"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		_, steerBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		var steer map[string]any
		if err := json.Unmarshal(steerBytes, &steer); err != nil {
			t.Errorf("decode steer: %v", err)
			return
		}
		steerSeen <- steer
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var callbackCalls atomic.Int32
	ambiguous := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(ctx, map[string]any{
		"type":  "response.create",
		"model": "gpt-6-astra",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			if !callbackCalls.CompareAndSwap(0, 1) {
				return nil
			}
			delivery, err := deliver(ctx, "steer before disconnect")
			ambiguous <- delivery
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	_, readErr := io.ReadAll(body)
	if readErr == nil {
		t.Fatal("expected stream read error after disconnected websocket")
	}
	select {
	case steer := <-steerSeen:
		if steer["type"] != "response.steer" || steer["previous_response_id"] != "resp_disconnect" {
			t.Fatalf("steer event = %#v", steer)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive response.steer before disconnect")
	}
	select {
	case delivery := <-ambiguous:
		if delivery.Status != AstraSteeringAmbiguous || delivery.PreviousResponseID != "resp_disconnect" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe ambiguous delivery")
	}
	records := client.responsesTransportState.SteeringDeliveries()
	for _, record := range records {
		if record.Status == AstraSteeringAccepted {
			t.Fatalf("disconnected steering must not be recorded as accepted: %#v", records)
		}
	}
}

func TestOpenResponsesWebsocketStream_AstraAcceptedSteeringDisconnectPreservesServerOwnership(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusInternalError, "disconnect after acceptance")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_disconnect_accepted"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.accepted","steer":{"id":"steer_disconnect_accepted","previous_response_id":"resp_disconnect_accepted"}}`)); err != nil {
			t.Errorf("write steer accepted: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	var callbackCalls atomic.Int32
	deliveries := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			if !callbackCalls.CompareAndSwap(0, 1) {
				return nil
			}
			delivery, err := deliver(ctx, "do not replay after acceptance")
			deliveries <- delivery
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err == nil {
		t.Fatal("expected stream read error after accepted steering disconnect")
	}
	select {
	case delivery := <-deliveries:
		if delivery.Status != AstraSteeringAmbiguous || delivery.SteeringID != "steer_disconnect_accepted" || delivery.PreviousResponseID != "resp_disconnect_accepted" || delivery.ResponseID != "" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not preserve accepted ownership")
	}
	ambiguous := client.responsesTransportState.takeAstraSteeringAmbiguous()
	if len(ambiguous) != 1 || ambiguous[0].SteeringID != "steer_disconnect_accepted" {
		t.Fatalf("accepted disconnect ambiguous deliveries = %#v", ambiguous)
	}
}

func TestOpenResponsesWebsocketStream_AstraMidTurnSteeringCompletedResponseCancelsAndJoinsCallback(t *testing.T) {
	callbackStarted := make(chan struct{})
	callbackFinished := make(chan struct{})
	deliveryAllowed := make(chan struct{})
	serverReadDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_done"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		select {
		case <-callbackStarted:
		case <-time.After(time.Second):
			t.Error("steering callback did not start while response was active")
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"done"}`)); err != nil {
			t.Errorf("write delta: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_done","status":"completed","model":"gpt-6-astra"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
			return
		}
		readCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		if _, data, err := conn.Read(readCtx); err == nil {
			t.Errorf("unexpected frame after completed response: %s", data)
		}
		close(serverReadDone)
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := client.openResponsesWebsocketStream(ctx, map[string]any{
		"type":  "response.create",
		"model": "gpt-6-astra",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			defer close(callbackFinished)
			select {
			case <-callbackStarted:
			default:
				close(callbackStarted)
			}
			select {
			case <-deliveryAllowed:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := deliver(ctx, "late steer")
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(raw), "done") || !strings.Contains(string(raw), "response.completed") {
		t.Fatalf("stream = %s, want completed Astra response", raw)
	}
	close(deliveryAllowed)
	select {
	case <-callbackFinished:
	case <-time.After(time.Second):
		t.Fatal("steering callback was not joined before the completed response returned")
	}
	select {
	case <-serverReadDone:
	case <-time.After(time.Second):
		t.Fatal("server did not finish checking for late steer frame")
	}
	records := client.responsesTransportState.SteeringDeliveries()
	for _, record := range records {
		if record.Status == AstraSteeringAccepted || record.Status == AstraSteeringDelivered {
			t.Fatalf("completed response must not record delivered or accepted steering: %#v", records)
		}
	}
}

func TestOpenResponsesWebsocketStream_NonGPT6SuppressesMidTurnSteeringCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_sol"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"sol done"}`)); err != nil {
			t.Errorf("write delta: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_sol","status":"completed","model":"gpt-5.6-sol"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
			return
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	var callbackCalls atomic.Int32
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type":  "response.create",
		"model": "gpt-5.6-sol",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-5.6-sol",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			callbackCalls.Add(1)
			_, _ = deliver(ctx, "must not send")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(raw), "sol done") {
		t.Fatalf("stream = %s, want non-Astra response", raw)
	}
	if callbackCalls.Load() != 0 {
		t.Fatalf("non-Astra model invoked mid-turn steering callback %d times", callbackCalls.Load())
	}
	if records := client.responsesTransportState.SteeringDeliveries(); len(records) != 0 {
		t.Fatalf("non-Astra model recorded steering deliveries: %#v", records)
	}
}

func TestOpenResponsesWebsocketStream_AstraMidTurnSteeringCallbackErrorFailsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.CloseNow()
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_callback_error"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		_, _, _ = conn.Read(r.Context())
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(context.Context, AstraSteeringDeliverer) error {
			return errors.New("store steering receipt")
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	_, err = io.ReadAll(body)
	if err == nil || !strings.Contains(err.Error(), "persisting Astra steering delivery: store steering receipt") {
		t.Fatalf("read error = %v, want callback persistence failure", err)
	}
}

func TestOpenResponsesWebsocketStream_AstraMissingAcknowledgementIsAmbiguous(t *testing.T) {
	originalTimeout := astraSteeringAckTimeout
	astraSteeringAckTimeout = 25 * time.Millisecond
	t.Cleanup(func() { astraSteeringAckTimeout = originalTimeout })

	deliveryObserved := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_no_ack"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		select {
		case <-deliveryObserved:
		case <-time.After(time.Second):
			t.Error("steering callback did not observe acknowledgement timeout")
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_no_ack","status":"completed","model":"gpt-6-astra"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
		}
	}))
	defer srv.Close()

	originalURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	t.Cleanup(func() { OpenAIAPIBaseURL = originalURL })

	client := NewWithAPIKey("sk-test")
	deliveryResult := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type": "response.create", "model": "gpt-6-astra", "input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model: "gpt-6-astra",
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			delivery, err := deliver(ctx, "steer without an acknowledgement")
			deliveryResult <- delivery
			close(deliveryObserved)
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read stream: %v", err)
	}

	select {
	case delivery := <-deliveryResult:
		if delivery.Status != AstraSteeringAmbiguous || delivery.PreviousResponseID != "resp_no_ack" || !strings.Contains(delivery.Error, "timed out") {
			t.Fatalf("delivery = %#v, want ambiguous timeout", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not return delivery state")
	}
}

func TestOpenResponsesWebsocketStream_GPT6SolMidTurnSteeringAccepted(t *testing.T) {
	steerSeen := make(chan map[string]any, 1)
	steeringWakeup := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read initial request: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_original"}}`)); err != nil {
			t.Errorf("write created: %v", err)
			return
		}
		steeringWakeup <- struct{}{}
		_, steerBytes, err := conn.Read(r.Context())
		if err != nil {
			t.Errorf("read steer: %v", err)
			return
		}
		var steer map[string]any
		if err := json.Unmarshal(steerBytes, &steer); err != nil {
			t.Errorf("decode steer: %v", err)
			return
		}
		steerSeen <- steer
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.steer.accepted","sequence_number":4,"steer":{"id":"steer_accepted","previous_response_id":"resp_original"}}`)); err != nil {
			t.Errorf("write steer accepted: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.incomplete","response":{"id":"resp_original","incomplete_details":{"reason":"steered"}}}`)); err != nil {
			t.Errorf("write steered incomplete: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp_continued"}}`)); err != nil {
			t.Errorf("write continuation created: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.output_text.delta","delta":"continued"}`)); err != nil {
			t.Errorf("write delta: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp_continued","status":"completed","model":"gpt-6-sol"}}`)); err != nil {
			t.Errorf("write completed: %v", err)
			return
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	var callbackCalls atomic.Int32
	accepted := make(chan AstraSteeringDelivery, 1)
	body, err := client.openResponsesWebsocketStream(context.Background(), map[string]any{
		"type":  "response.create",
		"model": "gpt-6-sol",
		"input": []any{},
	}, false, responsesWebsocketStreamOptions{
		Model:                 "gpt-6-sol",
		MidTurnSteeringWakeup: steeringWakeup,
		OnMidTurnSteering: func(ctx context.Context, deliver AstraSteeringDeliverer) error {
			if !callbackCalls.CompareAndSwap(0, 1) {
				return nil
			}
			delivery, err := deliver(ctx, "steer now")
			if err == nil {
				accepted <- delivery
			}
			return err
		},
	})
	if err != nil {
		t.Fatalf("openResponsesWebsocketStream: %v", err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if strings.Contains(string(raw), "response.steer.accepted") || strings.Contains(string(raw), "\"reason\":\"steered\"") {
		t.Fatalf("control events leaked to SSE parser stream: %s", raw)
	}
	if !strings.Contains(string(raw), "continued") {
		t.Fatalf("stream = %s, want continuation delta", raw)
	}
	select {
	case steer := <-steerSeen:
		if steer["type"] != "response.steer" || steer["previous_response_id"] != "resp_original" || steer["input"] != "steer now" {
			t.Fatalf("steer event = %#v", steer)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive response.steer")
	}
	select {
	case delivery := <-accepted:
		if delivery.Status != AstraSteeringAccepted || delivery.SteeringID != "steer_accepted" || delivery.PreviousResponseID != "resp_original" || delivery.ResponseID != "resp_continued" {
			t.Fatalf("delivery = %#v", delivery)
		}
	case <-time.After(time.Second):
		t.Fatal("steering callback did not observe acceptance")
	}
	records := client.responsesTransportState.SteeringDeliveries()
	if len(records) < 2 || records[len(records)-1].Status != AstraSteeringAccepted {
		t.Fatalf("steering records = %#v", records)
	}
}
