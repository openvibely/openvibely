package openaiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStandaloneWebSearchRecentInputKeepsOnlyRecentVisibleText(t *testing.T) {
	longAssistant := strings.Repeat("assistant ", 2500)
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "old user"},
		map[string]any{"type": "message", "role": "assistant", "content": "old assistant"},
		map[string]any{"type": "message", "role": "user", "content": "previous user"},
		map[string]any{"type": "function_call", "name": "read_file", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "output": "secret tool output"},
		map[string]any{"type": "message", "role": "assistant", "content": longAssistant},
		map[string]any{"type": "message", "role": "user", "content": "<environment_context>ignored</environment_context>"},
		map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "# Context from my IDE setup:\n\n## Open tabs:\n- private.go\n\n## My request:\ncurrent user"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
		}},
		map[string]any{"type": "message", "role": "assistant", "content": "commentary after current user"},
	}

	got := standaloneWebSearchRecentInput(input)
	if len(got) != 3 {
		t.Fatalf("recent input length = %d, want 3: %#v", len(got), got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{"previous user", "assistant", "current user"} {
		if !strings.Contains(text, want) {
			t.Errorf("recent input omitted %q: %s", want, text)
		}
	}
	for _, omitted := range []string{"old user", "secret tool output", "environment_context", "input_image", "commentary after current user", "private.go", "Context from my IDE"} {
		if strings.Contains(text, omitted) {
			t.Errorf("recent input retained %q: %s", omitted, text)
		}
	}
	assistant := got[1].(map[string]any)
	if tokens := approxOpenAITokenCount(visibleSearchMessageText(assistant["content"], "assistant")); tokens > standaloneWebSearchAssistantTokenLimit {
		t.Fatalf("assistant tokens = %d, want <= %d", tokens, standaloneWebSearchAssistantTokenLimit)
	}
}

func TestVisibleSearchUserTextRetainsRequestAfterContextBlocks(t *testing.T) {
	input := "<environment_context>private</environment_context>\n<permissions instructions>secret</permissions instructions>\nfind the latest release"
	if got := visibleSearchUserText(input); got != "find the latest release" {
		t.Fatalf("visibleSearchUserText() = %q", got)
	}
	if got := visibleSearchUserText("# Context from my IDE setup:\n\n## Open tabs:\n- secret.go"); got != "" {
		t.Fatalf("context-only message = %q, want empty", got)
	}
}

func TestRunStandaloneWebSearchAPIKeyPreservesStructuredResults(t *testing.T) {
	var gotAuth string
	var gotInput []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/alpha/search" {
			t.Fatalf("path = %q, want /v1/alpha/search", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotInput, _ = body["input"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":"search output","results":[{"title":"Result","url":"https://example.com"}]}`))
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	result, isError, err := client.runStandaloneWebSearch(context.Background(), "gpt-5.6-sol", []any{
		map[string]any{"type": "message", "role": "user", "content": "search this"},
		map[string]any{"type": "function_call_output", "output": "do not send"},
	}, json.RawMessage(`{"search_query":[{"q":"example"}]}`), 10000, false)
	if err != nil || isError {
		t.Fatalf("runStandaloneWebSearch error=%v isError=%v", err, isError)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if result.Output != "search output" || !strings.Contains(string(result.Results), "https://example.com") {
		t.Fatalf("result = %#v", result)
	}
	if display := result.DisplayOutput(); !strings.Contains(display, "search output") || !strings.Contains(display, "https://example.com") {
		t.Fatalf("DisplayOutput() = %q", display)
	}
	if len(gotInput) != 1 || strings.Contains(string(mustJSON(t, gotInput)), "do not send") {
		t.Fatalf("search input = %#v", gotInput)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
