package openaiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

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
		ReasoningEffort: "xhigh", // should be ignored (unsupported for API)
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
		if got := r.URL.Query().Get("client_version"); got != "0.0.0" {
			t.Fatalf("client_version query = %q, want 0.0.0", got)
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
		if got, ok := body["instructions"].(string); !ok || strings.TrimSpace(got) == "" {
			t.Fatalf("instructions must be present and non-empty, got %#v", body["instructions"])
		}
		if got, ok := body["stream"].(bool); !ok || !got {
			t.Fatalf("stream = %#v, want true", body["stream"])
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
		Model: "gpt-5.3-codex",
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
	got := extractChatGPTAccountID(token)
	if got != "org_abc" {
		t.Fatalf("extractChatGPTAccountID = %q, want org_abc", got)
	}
	if got := extractChatGPTAccountID("not-a-jwt"); got != "" {
		t.Fatalf("extractChatGPTAccountID invalid token = %q, want empty", got)
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

func TestSend_StreamingSanitizesPseudoToolText(t *testing.T) {
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
	if strings.Contains(got, "{\"tool\"") || strings.Contains(got, "<tool name=") || strings.Contains(got, "bash -lc 'ls -la'") {
		t.Fatalf("streamed output still contains raw pseudo-tool syntax: %q", got)
	}
	if !strings.Contains(got, "[Using tool: Glob | .]") {
		t.Fatalf("streamed output missing list_files marker: %q", got)
	}
	if !strings.Contains(got, "[Using tool: Bash | ls -la]") {
		t.Fatalf("streamed output missing bash marker: %q", got)
	}
	if strings.Count(got, "[Using tool: Bash | ls -la]") != 1 {
		t.Fatalf("expected exactly one bash marker, got output: %q", got)
	}
	if !strings.Contains(resp.Text, "[Using tool: Glob | .]") {
		t.Fatalf("response text missing tool marker: %q", resp.Text)
	}
}

func TestOpenAIStreamSanitizer_DoesNotSplitUTF8Runes(t *testing.T) {
	var emitted []string
	sanitizer := newOpenAIStreamSanitizer(func(text string) {
		emitted = append(emitted, text)
	})

	delta := strings.Repeat("a", 7) + "I\u2019" + "ve" + strings.Repeat("x", 28)
	sanitizer.Write(delta)
	sanitizer.Flush()

	if len(emitted) < 2 {
		t.Fatalf("expected sanitizer to emit multiple chunks, got %d", len(emitted))
	}
	for i, chunk := range emitted {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is invalid UTF-8: %q", i, chunk)
		}
	}

	got := strings.Join(emitted, "")
	if got != delta {
		t.Fatalf("joined output mismatch: got %q, want %q", got, delta)
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

func testOAuthJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	return header + "." + payload + "." + signature
}
