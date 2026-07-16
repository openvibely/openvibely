package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type completionsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f completionsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type failingCompletionsBody struct{}

func (failingCompletionsBody) Read([]byte) (int, error) {
	return 0, errors.New("read: operation timed out")
}
func (failingCompletionsBody) Close() error { return nil }

func TestSendCompletionsRetriesStreamTimeoutBeforeOutput(t *testing.T) {
	attempts := 0
	client := NewWithCompatibleAPIKey("test-key", "https://compatible.test/v1", "", "")
	client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		body := io.ReadCloser(failingCompletionsBody{})
		if attempts == 2 {
			body = io.NopCloser(strings.NewReader(
				"data: {\"choices\":[{\"delta\":{\"content\":\"retry succeeded\"}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}

	resp, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{DisableTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || resp.Text != "retry succeeded" {
		t.Fatalf("attempts/text = %d/%q, want 2/retry succeeded", attempts, resp.Text)
	}
}

func TestSendCompletionsDoesNotReplayStreamAfterOutput(t *testing.T) {
	attempts := 0
	client := NewWithCompatibleAPIKey("test-key", "https://compatible.test/v1", "", "")
	client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		body := io.NopCloser(io.MultiReader(
			strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"),
			failingCompletionsBody{},
		))
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}

	_, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{DisableTools: true})
	if err == nil {
		t.Fatal("expected stream read error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 after output was observed", attempts)
	}
}

func TestCompletionsOptions_DefaultMaxTurnsNoLimit(t *testing.T) {
	client := NewWithAPIKey("test-key")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts := &CompletionsOptions{}
	_, err := client.SendCompletions(ctx, "test", opts)
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}

	if opts.MaxTurns != math.MaxInt32 {
		t.Fatalf("expected MaxTurns default to no limit (%d), got %d", math.MaxInt32, opts.MaxTurns)
	}
}

func TestSendCompletions_CompatibleBaseURLAuthAndUsage(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"model\":\"provider/model\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15,\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = "https://api.openai.example/v1/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithCompatibleAPIKey("sk-compatible", srv.URL+"/v1/", "", "")
	resp, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{
		Model:           "provider/model",
		MaxOutputTokens: 99,
		DisableTools:    true,
	})
	if err != nil {
		t.Fatalf("SendCompletions: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-compatible" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["max_tokens"] != float64(99) {
		t.Fatalf("max_tokens = %#v, want 99", gotBody["max_tokens"])
	}
	if _, ok := gotBody["input"]; ok {
		t.Fatalf("chat completions request must not include Responses input field: %#v", gotBody)
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("chat completions request must not include Responses max_output_tokens field: %#v", gotBody)
	}
	if resp.Text != "Hello" || resp.InputTokens != 12 || resp.OutputTokens != 3 || resp.TotalTokens != 15 || resp.CachedInputTokens != 4 || resp.ReasoningTokens != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSendCompletions_CompatibleBaseURLAllowsMissingAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Local\"}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	client := NewWithCompatibleAPIKey("", srv.URL+"/v1/", "", "")
	resp, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{DisableTools: true})
	if err != nil {
		t.Fatalf("SendCompletions: %v", err)
	}
	if resp.Text != "Local" {
		t.Fatalf("Text = %q", resp.Text)
	}
}

func TestSendCompletions_ExtraHeadersAndBodyProtectOwnedFields(t *testing.T) {
	var gotBody map[string]any
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Provider-Route")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Done\"}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	client := NewWithCompatibleAPIKey("sk-compatible", srv.URL+"/v1/", "", "")
	_, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{
		Model:        "real-model",
		DisableTools: true,
		ExtraHeaders: map[string]string{"X-Provider-Route": "openrouter"},
		ExtraBody: map[string]any{
			"model":            "evil-model",
			"stream":           false,
			"reasoning_effort": "high",
		},
	})
	if err != nil {
		t.Fatalf("SendCompletions: %v", err)
	}
	if gotHeader != "openrouter" {
		t.Fatalf("X-Provider-Route = %q", gotHeader)
	}
	if gotBody["model"] != "real-model" || gotBody["stream"] != true {
		t.Fatalf("protected fields were overridden: %#v", gotBody)
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("allowed extra body missing: %#v", gotBody)
	}
}

func TestSendCompletions_DisableToolsSuppressesExtraTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		if _, ok := reqBody["tools"]; ok {
			t.Fatalf("expected no tools when DisableTools=true, got %#v", reqBody["tools"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Done.\"}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
		ExtraTools: []ToolDefinition{
			{Name: "memory_view", Description: "Load an authorized selected memory"},
		},
	})
	if err != nil {
		t.Fatalf("SendCompletions: %v", err)
	}
}

func TestSendCompletions_ToolFilterRemovesDeniedToolsFromRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)

		tools, ok := reqBody["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("expected memory_view runtime tool only, got %#v", reqBody["tools"])
		}
		seen := map[string]bool{}
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			name, _ := function["name"].(string)
			seen[name] = true
		}
		if !seen["memory_view"] || seen["list_files"] || seen["Bash"] {
			t.Fatalf("expected memory_view only, got %#v", seen)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Done.\"}}]}\n\n" +
				"data: {\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendCompletions(context.Background(), "test", &CompletionsOptions{
		Model: "gpt-5.3-codex",
		ExtraTools: []ToolDefinition{
			{
				Type:        "function",
				Name:        "memory_view",
				Description: "Load an authorized selected memory",
			},
		},
		ToolFilter: func(name string) bool { return name == "memory_view" },
	})
	if err != nil {
		t.Fatalf("SendCompletions: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestCompletionsTurnResult_Structure(t *testing.T) {
	// Just a compilation test
	result := &completionsTurnResult{
		text:        "test",
		stopReason:  "stop",
		model:       "gpt-4",
		inputTokens: 10,
	}

	if result.text != "test" {
		t.Errorf("expected text=test, got %s", result.text)
	}
}
