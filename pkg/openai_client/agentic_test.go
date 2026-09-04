package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// buildSSE constructs a server-sent events stream from JSON data lines.
func buildSSE(events []string) string {
	var sb strings.Builder
	for _, evt := range events {
		fmt.Fprintf(&sb, "data: %s\n\n", evt)
	}
	return sb.String()
}

func TestParseAgenticStream_TextOnly(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_text.delta","delta":"Hello"}`,
		`{"type":"response.output_text.delta","delta":" world"}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	})

	var collected strings.Builder
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.model != "gpt-5.3-codex" {
		t.Errorf("model = %q, want gpt-5.3-codex", result.model)
	}
	if result.inputTokens != 10 {
		t.Errorf("inputTokens = %d, want 10", result.inputTokens)
	}
	if result.outputTokens != 5 {
		t.Errorf("outputTokens = %d, want 5", result.outputTokens)
	}
	if collected.String() != "Hello world" {
		t.Errorf("collected text = %q, want %q", collected.String(), "Hello world")
	}
	if result.text != "Hello world" {
		t.Errorf("result.text = %q, want %q", result.text, "Hello world")
	}
	if len(result.toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(result.toolCalls))
	}
}

func TestParseAgenticStream_PreservesEncryptedReasoningForNextTurn(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"encrypted-state","summary":[]}}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"call_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-luna"}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("parseAgenticStream: %v", err)
	}
	if len(result.outputItems) != 2 {
		t.Fatalf("outputItems = %#v, want reasoning and function call", result.outputItems)
	}
	reasoning, _ := result.outputItems[0].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "encrypted-state" {
		t.Fatalf("reasoning output item = %#v", reasoning)
	}
}

func TestStatelessOAuthOutputItemsDropsUnencryptedReasoning(t *testing.T) {
	items := []any{
		map[string]any{"id": "rs_missing", "type": "reasoning", "summary": []any{}},
		map[string]any{"id": "rs_encrypted", "type": "reasoning", "encrypted_content": "opaque-state"},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "echo", "arguments": "{}"},
	}
	filtered := statelessOAuthOutputItems(items)
	if len(filtered) != 2 {
		t.Fatalf("filtered items = %#v, want encrypted reasoning and function call", filtered)
	}
	reasoning, _ := filtered[0].(map[string]any)
	if reasoning["id"] != "rs_encrypted" {
		t.Fatalf("reasoning item = %#v", reasoning)
	}
}

func TestSendAgentic_APIKeyResponsesLiteDoesNotReplayUnencryptedReasoning(t *testing.T) {
	requests := make(chan map[string]any, 2)
	var turns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		if turns.Add(1) == 1 {
			_, _ = w.Write([]byte(buildSSE([]string{
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_temporary","type":"reasoning","summary":[]}}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{}"}}`,
				`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
			})))
			return
		}
		_, _ = w.Write([]byte(buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"done"}`,
			`{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol"}}`,
		})))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("test-key")
	client.responsesTransportState.websocketDisabled.Store(true)
	resp, err := client.SendAgentic(context.Background(), "use echo", &AgenticOptions{
		Model:            "gpt-5.6-sol",
		SkipDefaultTools: true,
		ExtraTools:       []ToolDefinition{{Type: "function", Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolExecutor: func(_ context.Context, _ string, _ json.RawMessage) (string, bool, error) {
			return "echoed", false, nil
		},
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
	<-requests
	second := <-requests
	input, _ := second["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "reasoning" {
			t.Fatalf("second request replayed temporary reasoning item: %#v", item)
		}
	}
}

func TestCompactAgenticInputItems_OAuthLunaUsesResponsesLiteContract(t *testing.T) {
	var gotHeader string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		gotHeader = r.Header.Get("x-openai-internal-codex-responses-lite")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode compaction body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"summary\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compact\",\"status\":\"completed\",\"model\":\"gpt-5.6-luna\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n",
		))
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	items := []any{map[string]any{"type": "message", "role": "user", "content": "hello"}}
	compactedItems, summary, err := client.compactAgenticInputItems(context.Background(), items, DefaultTools(), &AgenticOptions{
		Model:           "gpt-5.6-luna",
		ReasoningEffort: "max",
	}, true)
	if err != nil {
		t.Fatalf("compactAgenticInputItems: %v", err)
	}
	if gotHeader != "true" {
		t.Fatalf("Responses Lite header = %q, want true", gotHeader)
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["context"] != "all_turns" || reasoning["effort"] != "max" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	if got, ok := gotBody["store"].(bool); !ok || got {
		t.Fatalf("store = %#v, want false", gotBody["store"])
	}
	if _, ok := gotBody["max_output_tokens"]; ok {
		t.Fatalf("max_output_tokens should be omitted for OAuth requests")
	}
	if parallel, ok := gotBody["parallel_tool_calls"].(bool); !ok || parallel {
		t.Fatalf("parallel_tool_calls = %#v, want false", gotBody["parallel_tool_calls"])
	}
	input := gotBody["input"].([]any)
	trigger, ok := input[len(input)-1].(map[string]any)
	if !ok || trigger["type"] != "compaction_trigger" {
		t.Fatalf("last compaction input = %#v, want compaction_trigger", input[len(input)-1])
	}
	if len(compactedItems) < 1 {
		t.Fatalf("compacted item count = %d, want at least 1", len(compactedItems))
	}
	compaction, ok := compactedItems[len(compactedItems)-1].(map[string]any)
	if !ok || compaction["type"] != "compaction" {
		t.Fatalf("last compacted item = %#v, want compaction", compactedItems[len(compactedItems)-1])
	}
	if summary != "summary" {
		t.Fatalf("summary = %q, want summary", summary)
	}
}

func TestCompactAgenticInputItems_APIKeySolUsesResponsesLiteContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/compact" {
			t.Fatalf("path = %q, want /v1/responses/compact", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "true" {
			t.Fatalf("Responses Lite header = %q, want true", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		if reasoning["context"] != "all_turns" || reasoning["effort"] != "medium" {
			t.Fatalf("reasoning = %#v", reasoning)
		}
		if parallel, ok := body["parallel_tool_calls"].(bool); !ok || parallel {
			t.Fatalf("parallel_tool_calls = %#v, want false", body["parallel_tool_calls"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"type":"compaction","encrypted_content":"summary"}]}`))
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	items := []any{map[string]any{"type": "message", "role": "user", "content": "hello"}}
	_, _, err := client.compactAgenticInputItems(context.Background(), items, DefaultTools(), &AgenticOptions{
		Model: "gpt-5.6-sol",
	}, false)
	if err != nil {
		t.Fatalf("compactAgenticInputItems: %v", err)
	}
}

func TestBuildAgenticRemoteCompactionV2History_RetainsAssistantAndTruncatesBoundary(t *testing.T) {
	oversized := "prefix-" + strings.Repeat("A", (openAIRemoteCompactionV2RetainedMessageTokenBudget+100)*4) + "-suffix"
	inputItems := []any{
		map[string]any{"type": "message", "role": "user", "content": oversized},
		map[string]any{"type": "message", "role": "assistant", "content": "intermediate result"},
		map[string]any{"type": "message", "role": "assistant", "content": "Message Type: FINAL_ANSWER\nfinished"},
		map[string]any{"type": "function_call", "name": "read_file", "arguments": "{}"},
	}
	compaction := map[string]any{"type": "compaction", "encrypted_content": "summary"}

	history := buildAgenticRemoteCompactionV2History(inputItems, compaction)
	if len(history) != 3 {
		t.Fatalf("history len = %d, want truncated user, assistant, compaction", len(history))
	}
	first := history[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("first retained item = %#v, want user message", first)
	}
	if got := len([]rune(first["content"].(string))); got >= len([]rune(oversized)) {
		t.Fatalf("user message was not truncated; len=%d original=%d", got, len([]rune(oversized)))
	}
	truncated := first["content"].(string)
	if !strings.HasPrefix(truncated, "prefix-") || !strings.HasSuffix(truncated, "-suffix") {
		t.Fatalf("truncated message should preserve prefix and suffix, got %.80q", truncated)
	}
	if !strings.Contains(truncated, "tokens truncated") {
		t.Fatalf("truncated message missing token marker: %.80q", truncated)
	}
	second := history[1].(map[string]any)
	if second["role"] != "assistant" || second["content"] != "intermediate result" {
		t.Fatalf("second retained item = %#v, want intermediate assistant message", second)
	}
	last := history[2].(map[string]any)
	if last["type"] != "compaction" {
		t.Fatalf("last item = %#v, want compaction", last)
	}
}

func TestEstimateInputItemsTokens_UsesModelVisibleBytes(t *testing.T) {
	item := map[string]any{"type": "message", "role": "user", "content": "hello"}
	serialized, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	want := approxOpenAITokensFromByteCount(len(serialized))
	if got := estimateInputItemsTokens([]any{item}); got != want {
		t.Fatalf("estimateInputItemsTokens = %d, want serialized byte estimate %d", got, want)
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_EncryptedCompaction(t *testing.T) {
	encryptedContent := strings.Repeat("x", 4000)
	item := map[string]any{"type": "compaction", "encrypted_content": encryptedContent}

	wantBytes := estimateOpenAIReasoningLength(len(encryptedContent))
	wantTokens := approxOpenAITokensFromByteCount(wantBytes)
	if got := estimateInputItemsTokens([]any{item}); got != wantTokens {
		t.Fatalf("estimateInputItemsTokens = %d, want encrypted compaction estimate %d", got, wantTokens)
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_ReplacesInlineImagePayload(t *testing.T) {
	payload := strings.Repeat("a", 12000)
	imageURL := "data:image/png;base64," + payload
	item := map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": "inspect"},
			map[string]any{"type": "input_image", "image_url": imageURL, "detail": "high"},
		},
	}
	serialized, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}

	wantBytes := len(serialized) - len(payload) + openAIResizedImageBytesEstimate
	if got := estimateAgenticResponseItemModelVisibleBytes(item); got != wantBytes {
		t.Fatalf("visible bytes = %d, want image payload replacement %d", got, wantBytes)
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_ReplacesInlineAudioPayload(t *testing.T) {
	payload := strings.Repeat("b", 12000)
	audioURL := "data:audio/wav;base64," + payload
	item := map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_audio", "audio_url": audioURL},
		},
	}
	serialized, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}

	wantBytes := len(serialized) - len(payload) + approxOpenAIBytesForTokens(approxOpenAITokenCount(audioURL))
	if got := estimateAgenticResponseItemModelVisibleBytes(item); got != wantBytes {
		t.Fatalf("visible bytes = %d, want audio payload replacement %d", got, wantBytes)
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_ReplacesEncryptedOutputPayload(t *testing.T) {
	encryptedContent := strings.Repeat("z", 4096)
	item := map[string]any{
		"type": "function_call_output",
		"output": []any{
			map[string]any{"type": "encrypted_content", "encrypted_content": encryptedContent},
		},
	}
	serialized, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}

	wantBytes := len(serialized) - len(encryptedContent) + estimateOpenAIEncryptedFunctionOutputLength(len(encryptedContent))
	if got := estimateAgenticResponseItemModelVisibleBytes(item); got != wantBytes {
		t.Fatalf("visible bytes = %d, want encrypted output replacement %d", got, wantBytes)
	}
}

func TestParseAgenticStream_WithToolUse(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_text.delta","delta":"Let me read that file."}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"file_"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"path\": \"main.go\"}"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"file_path\": \"main.go\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":15}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.toolCalls))
	}
	tc := result.toolCalls[0]
	if tc.Name != "read_file" {
		t.Errorf("tool name = %q, want read_file", tc.Name)
	}
	if tc.CallID != "call_1" {
		t.Errorf("call_id = %q, want call_1", tc.CallID)
	}

	var args struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
		t.Fatalf("parse tool args: %v", err)
	}
	if args.FilePath != "main.go" {
		t.Errorf("file_path = %q, want main.go", args.FilePath)
	}
}

func TestParseAgenticStream_StreamsTextAtFunctionCallBoundary(t *testing.T) {
	pr, pw := io.Pipe()
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	textCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		_, err := client.parseAgenticStreamWithToolCallbacks(pr, func(text string) {
			select {
			case textCh <- text:
			default:
			}
		}, nil, nil, nil)
		errCh <- err
	}()

	_, _ = pw.Write([]byte(buildSSE([]string{
		`{"type":"response.output_text.delta","delta":"I'll inspect that now."}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
	})))

	select {
	case got := <-textCh:
		if got != "I'll inspect that now." {
			t.Fatalf("unexpected text callback: %q", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("text did not stream at function-call boundary")
	}

	_, _ = pw.Write([]byte(buildSSE([]string{
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"file_path\":\"main.go\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":5}}}`,
	})))
	_ = pw.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("parseAgenticStreamWithToolCallbacks: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parser did not finish")
	}
}

func TestParseAgenticStream_FunctionCallMissingDoneArgumentsUsesDeltas(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"file_"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"path\": \"main.go\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":5}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.toolCalls))
	}
	if result.toolCalls[0].Arguments != `{"file_path": "main.go"}` {
		t.Fatalf("tool call arguments = %q, want streamed delta args", result.toolCalls[0].Arguments)
	}
	if len(result.outputItems) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(result.outputItems))
	}
	item, ok := result.outputItems[0].(map[string]any)
	if !ok {
		t.Fatalf("output item type = %T, want map[string]any", result.outputItems[0])
	}
	if got := item["arguments"]; got != `{"file_path": "main.go"}` {
		t.Fatalf("output item arguments = %#v, want streamed delta args", got)
	}
}

func TestParseAgenticStream_FunctionCallMissingArgumentsDefaultsToEmptyObject(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":5}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.toolCalls))
	}
	if result.toolCalls[0].Arguments != "{}" {
		t.Fatalf("tool call arguments = %q, want {}", result.toolCalls[0].Arguments)
	}
	if len(result.outputItems) != 1 {
		t.Fatalf("expected 1 output item, got %d", len(result.outputItems))
	}
	item, ok := result.outputItems[0].(map[string]any)
	if !ok {
		t.Fatalf("output item type = %T, want map[string]any", result.outputItems[0])
	}
	if got := item["arguments"]; got != "{}" {
		t.Fatalf("output item arguments = %#v, want {}", got)
	}
}

func TestParseAgenticStream_MultipleToolCalls(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"file_path\": \"a.go\"}"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_2","name":"read_file"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"read_file","arguments":"{\"file_path\": \"b.go\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":30,"output_tokens":10}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.toolCalls))
	}
	if result.toolCalls[0].CallID != "call_1" || result.toolCalls[1].CallID != "call_2" {
		t.Error("tool call IDs mismatch")
	}
}

func TestParseAgenticStream_EmptyStream(t *testing.T) {
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStream(strings.NewReader(""), nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want unexpected EOF", err)
	}
}

func TestParseAgenticStream_StreamError(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_text.delta","delta":"Hello"}`,
		`{"type":"response.error","message":"context_length_exceeded"}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err == nil {
		t.Fatal("expected error from stream error event")
	}
	if !strings.Contains(err.Error(), "context_length_exceeded") {
		t.Errorf("error = %q, want to contain context_length_exceeded", err.Error())
	}
}

func TestParseAgenticStream_WithReasoningDeltas(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"Plan: inspect handlers."}`,
		`{"type":"response.reasoning_summary_part.added","summary_index":1}`,
		`{"type":"response.reasoning_text.delta","content_index":0,"delta":"I should check task_detail handlers first."}`,
		`{"type":"response.output_text.delta","delta":"Done."}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":4}}}`,
	})

	var (
		textBuf     strings.Builder
		thinkingBuf strings.Builder
	)
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		textBuf.WriteString(text)
	}, func(text string) {
		thinkingBuf.WriteString(text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := textBuf.String(); got != "Done." {
		t.Fatalf("text callback = %q, want %q", got, "Done.")
	}
	if got := result.text; got != "Done." {
		t.Fatalf("result.text = %q, want %q", got, "Done.")
	}
	if got := thinkingBuf.String(); got != "Plan: inspect handlers.\n\nI should check task_detail handlers first." {
		t.Fatalf("thinking callback = %q", got)
	}
}

func TestParseAgenticStream_CompletedOnlyTextStillCallsTextCallback(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Recovered final text."}]}],"usage":{"input_tokens":8,"output_tokens":3}}}`,
	})

	var collected strings.Builder
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := collected.String(); got != "Recovered final text." {
		t.Fatalf("text callback = %q, want %q", got, "Recovered final text.")
	}
	if got := result.text; got != "Recovered final text." {
		t.Fatalf("result.text = %q, want %q", got, "Recovered final text.")
	}
}

func TestParseAgenticStream_ReasoningFromOutputItemDone(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"Plan first."}],"content":[{"type":"reasoning_text","text":"Then inspect files."}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":9,"output_tokens":4}}}`,
	})

	var thinking strings.Builder
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStream(strings.NewReader(stream), nil, func(text string) {
		thinking.WriteString(text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := thinking.String(); got != "Plan first.\n\nThen inspect files." {
		t.Fatalf("thinking callback = %q", got)
	}
}

func TestParseAgenticStream_ReasoningFallbackFromCompletedOutput(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"Summarized reasoning."}],"content":[{"type":"reasoning_text","text":"Detailed fallback reasoning."}]}],"usage":{"input_tokens":7,"output_tokens":2}}}`,
	})

	var thinking strings.Builder
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStream(strings.NewReader(stream), nil, func(text string) {
		thinking.WriteString(text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := thinking.String(); got != "Summarized reasoning.\n\nDetailed fallback reasoning." {
		t.Fatalf("thinking callback = %q", got)
	}
}

func TestParseAgenticStream_UsesSSEEventTypeWhenDataTypeMissing(t *testing.T) {
	stream := strings.Join([]string{
		"event: response.reasoning_summary_text.delta",
		`data: {"summary_index":0,"delta":"Plan via event header."}`,
		"",
		"event: response.output_text.delta",
		`data: {"delta":"Done via event header."}`,
		"",
		"event: response.completed",
		`data: {"response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":11,"output_tokens":6}}}`,
		"",
	}, "\n")

	var (
		textBuf     strings.Builder
		thinkingBuf strings.Builder
	)
	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		textBuf.WriteString(text)
	}, func(text string) {
		thinkingBuf.WriteString(text)
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := textBuf.String(); got != "Done via event header." {
		t.Fatalf("text callback = %q", got)
	}
	if got := thinkingBuf.String(); got != "Plan via event header." {
		t.Fatalf("thinking callback = %q", got)
	}
	if got := result.text; got != "Done via event header." {
		t.Fatalf("result.text = %q", got)
	}
}

func TestSendAgentic_ReasoningSummaryPayload(t *testing.T) {
	var gotReasoning map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if v, ok := reqBody["reasoning"].(map[string]any); ok {
			gotReasoning = v
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:            "gpt-5.5",
		DisableTools:     true,
		ReasoningEffort:  "xhigh",
		ReasoningSummary: "auto",
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if gotReasoning == nil {
		t.Fatal("expected reasoning payload")
	}
	if got := strings.TrimSpace(stringFromAny(gotReasoning["effort"])); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "xhigh")
	}
	if got := strings.TrimSpace(stringFromAny(gotReasoning["summary"])); got != "auto" {
		t.Fatalf("reasoning.summary = %q, want %q", got, "auto")
	}
}

func TestSendAgentic_APIKeyGPT56UsesResponsesLiteWebSocket(t *testing.T) {
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "" {
			t.Fatalf("ChatGPT-Account-ID = %q, want empty", got)
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
		completed := `{"type":"response.completed","response":{"status":"completed","model":"gpt-5.6-sol","usage":{"input_tokens":2,"output_tokens":1}}}`
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
			t.Errorf("write completed event: %v", err)
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:            "gpt-5.6-sol",
		DisableTools:     true,
		ReasoningEffort:  "max",
		ReasoningSummary: "auto",
		WebSearchEnabled: true,
		MaxTurns:         1,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if request["type"] != "response.create" || request["model"] != "gpt-5.6-sol" {
		t.Fatalf("request type/model = %v/%v", request["type"], request["model"])
	}
	for _, field := range []string{"tools", "instructions", "max_output_tokens", "truncation"} {
		if _, ok := request[field]; ok {
			t.Errorf("Lite websocket request unexpectedly contains %q", field)
		}
	}
	reasoning, _ := request["reasoning"].(map[string]any)
	if reasoning["effort"] != "max" || reasoning["summary"] != "auto" || reasoning["context"] != "all_turns" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	input, _ := request["input"].([]any)
	additionalTools, _ := input[0].(map[string]any)
	liteTools, _ := additionalTools["tools"].([]any)
	for _, raw := range liteTools {
		tool, _ := raw.(map[string]any)
		if tool["type"] == "web_search" || tool["type"] == "web_search_preview" || tool["type"] == "image_generation" {
			t.Fatalf("hosted tool leaked into Responses Lite additional_tools: %#v", tool)
		}
	}
}

func TestSendAgentic_ResponsesLiteStreamFailureFallsBackBeforeOutput(t *testing.T) {
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
			_ = conn.Close(websocket.StatusInternalError, "upstream reset")
			return
		}
		httpRequests.Add(1)
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "true" {
			t.Fatalf("Responses Lite header = %q", got)
		}
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
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.6-sol",
		DisableTools: true,
		MaxTurns:     1,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "recovered" {
		t.Fatalf("Text = %q, want recovered", resp.Text)
	}
	if websocketAttempts.Load() != 1 || httpRequests.Load() != 1 {
		t.Fatalf("attempts websocket=%d HTTP=%d", websocketAttempts.Load(), httpRequests.Load())
	}
}

func TestSendAgentic_ResponsesLiteReusesConnectionAndSendsIncrementalTurn(t *testing.T) {
	var handshakes atomic.Int32
	requests := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		for turn := 0; turn < 2; turn++ {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				t.Errorf("read turn %d: %v", turn+1, err)
				return
			}
			var request map[string]any
			if err := json.Unmarshal(data, &request); err != nil {
				t.Errorf("decode turn %d: %v", turn+1, err)
				return
			}
			requests <- request
			if turn == 0 {
				events := []string{
					`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{\"value\":\"hello\"}"}}`,
					`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6-sol"}}`,
				}
				for _, event := range events {
					if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
						t.Errorf("write first-turn event: %v", err)
						return
					}
				}
				continue
			}
			events := []string{
				`{"type":"response.output_text.delta","delta":"done"}`,
				`{"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.6-sol"}}`,
			}
			for _, event := range events {
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
					t.Errorf("write second-turn event: %v", err)
					return
				}
			}
		}
	}))
	defer srv.Close()

	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { OpenAIAPIBaseURL = original }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "use echo", &AgenticOptions{
		Model:            "gpt-5.6-sol",
		SkipDefaultTools: true,
		ExtraTools:       []ToolDefinition{{Type: "function", Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolExecutor: func(_ context.Context, _ string, _ json.RawMessage) (string, bool, error) {
			return "hello", false, nil
		},
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
	if handshakes.Load() != 1 {
		t.Fatalf("websocket handshakes = %d, want 1", handshakes.Load())
	}
	first := <-requests
	second := <-requests
	if _, ok := first["previous_response_id"]; ok {
		t.Fatalf("first request unexpectedly has previous_response_id")
	}
	if second["previous_response_id"] != "resp_1" {
		t.Fatalf("second previous_response_id = %#v", second["previous_response_id"])
	}
	input, _ := second["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("incremental input = %#v, want one tool result", input)
	}
	item, _ := input[0].(map[string]any)
	if item["type"] != "function_call_output" || item["call_id"] != "call_1" {
		t.Fatalf("incremental item = %#v", item)
	}
}

func TestSendAgentic_OAuthModelsReplayEncryptedReasoningWithStoreFalse(t *testing.T) {
	models := []string{
		"gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.4-mini",
		"gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.2-codex",
		"gpt-5.1-codex-max", "gpt-5.1-codex", "gpt-5.1-codex-mini",
		"gpt-5-codex", "gpt-5-codex-mini",
	}
	requests := make(chan map[string]any, len(models)*2)
	var turns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "text/event-stream")
		model := stringFromAny(request["model"])
		if turns.Add(1)%2 == 1 {
			_, _ = w.Write([]byte(buildSSE([]string{
				`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","encrypted_content":"opaque-state","summary":[]}}`,
				`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"echo","arguments":"{}"}}`,
				fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","model":%q}}`, model),
			})))
			return
		}
		_, _ = w.Write([]byte(buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"done"}`,
			fmt.Sprintf(`{"type":"response.completed","response":{"status":"completed","model":%q}}`, model),
		})))
	}))
	defer srv.Close()

	original := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL
	defer func() { OpenAIChatGPTAPIBaseURL = original }()

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
			resp, err := client.SendAgentic(context.Background(), "use echo", &AgenticOptions{
				Model:            model,
				SkipDefaultTools: true,
				ExtraTools:       []ToolDefinition{{Type: "function", Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}},
				ToolExecutor: func(_ context.Context, _ string, _ json.RawMessage) (string, bool, error) {
					return "echoed", false, nil
				},
				MaxTurns: 2,
			})
			if err != nil {
				t.Fatalf("SendAgentic: %v", err)
			}
			if resp.Text != "done" {
				t.Fatalf("Text = %q, want done", resp.Text)
			}

			first := <-requests
			second := <-requests
			for turn, request := range []map[string]any{first, second} {
				if request["model"] != model {
					t.Fatalf("turn %d model = %#v, want %q", turn+1, request["model"], model)
				}
				if stored, ok := request["store"].(bool); !ok || stored {
					t.Fatalf("turn %d store = %#v, want false", turn+1, request["store"])
				}
				include, _ := request["include"].([]any)
				if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
					t.Fatalf("turn %d include = %#v", turn+1, request["include"])
				}
			}
			input, _ := second["input"].([]any)
			var replayedReasoning map[string]any
			for _, raw := range input {
				item, _ := raw.(map[string]any)
				if item["type"] == "reasoning" {
					replayedReasoning = item
				}
			}
			if replayedReasoning == nil || replayedReasoning["encrypted_content"] != "opaque-state" {
				t.Fatalf("second-turn reasoning = %#v", replayedReasoning)
			}
		})
	}
}

func TestSendAgentic_TextOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello from agentic\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello from agentic\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Hello", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Hello from agentic" {
		t.Errorf("Text = %q, want %q", resp.Text, "Hello from agentic")
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(resp.ToolCalls))
	}
}

func TestSendAgentic_InjectsToolBoundarySteeringAfterFunctionOutput(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var turnCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := int(atomic.AddInt32(&turnCount, 1))
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if turn == 2 {
			input, ok := reqBody["input"].([]any)
			if !ok || len(input) < 2 {
				t.Fatalf("turn 2 input = %#v, want function output plus steering message", reqBody["input"])
			}
			var foundOutput bool
			for _, item := range input {
				m, _ := item.(map[string]any)
				if m["type"] == "function_call_output" && m["call_id"] == "call_1" {
					foundOutput = true
					if output, _ := m["output"].(string); !strings.Contains(output, "hello world") {
						t.Fatalf("tool output = %q, want file content", output)
					}
				}
			}
			if !foundOutput {
				t.Fatalf("turn 2 missing function_call_output in %#v", input)
			}
			last, _ := input[len(input)-1].(map[string]any)
			if last["type"] != "message" || last["role"] != "user" || last["content"] != "actually only review it" {
				t.Fatalf("turn 2 last input item = %#v, want steering user message", last)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}` + "\n\n" +
					fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr) + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":15}}}` + "\n\n",
			))
			return
		}
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"Reviewed only."}` + "\n\n" +
				`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":30,"output_tokens":10}}}` + "\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var callbackCalls int
	resp, err := client.SendAgentic(context.Background(), "fix the bug", &AgenticOptions{
		Model:   "gpt-5.3-codex",
		WorkDir: tmpDir,
		OnToolBoundarySteering: func(ctx context.Context) (string, error) {
			callbackCalls++
			return "actually only review it", nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if callbackCalls != 1 {
		t.Fatalf("callbackCalls = %d, want 1", callbackCalls)
	}
	if resp.Text != "Reviewed only." {
		t.Fatalf("resp.Text = %q, want Reviewed only.", resp.Text)
	}
	if got := atomic.LoadInt32(&turnCount); got != 2 {
		t.Fatalf("turnCount = %d, want 2", got)
	}
}

func TestSendAgentic_ToolCalling(t *testing.T) {
	// Create a temp file for the tool to read
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	turnCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++

		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		json.Unmarshal(body, &reqBody)

		// Verify tools are present in request
		if turnCount == 1 {
			tools, ok := reqBody["tools"].([]any)
			if !ok || len(tools) == 0 {
				t.Error("expected tools in first request")
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")

		if turnCount == 1 {
			// Turn 1: Return a function_call for read_file
			// Build the arguments JSON, then marshal it as a string for embedding
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON)) // double-encode for embedding in JSON

			addedEvt := fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`)
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr)
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":15}}}`

			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		} else {
			// Turn 2: Verify tool result is in input and return final text
			input, ok := reqBody["input"].([]any)
			if ok {
				// Should contain function_call_output
				found := false
				for _, item := range input {
					if m, ok := item.(map[string]any); ok {
						if m["type"] == "function_call_output" && m["call_id"] == "call_1" {
							found = true
							output := m["output"].(string)
							if !strings.HasPrefix(output, "1\thello world\n") || strings.Contains(output, "     1\t") {
								t.Errorf("model-facing read_file output must use compact line prefix, got: %q", output)
							}
							if !strings.Contains(output, "hello world") {
								t.Errorf("tool output should contain file content, got: %s", output)
							}
						}
					}
				}
				if !found {
					t.Error("expected function_call_output in turn 2 input")
				}
			}

			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"File contents read successfully.\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":30,\"output_tokens\":10}}}\n\n",
			))
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var toolUseCalled, toolResultCalled bool
	resp, err := client.SendAgentic(context.Background(), "Read test.txt", &AgenticOptions{
		Model:   "gpt-5.3-codex",
		WorkDir: tmpDir,
		OnToolUse: func(name string, input json.RawMessage) {
			toolUseCalled = true
			if name != "read_file" {
				t.Errorf("OnToolUse name = %q, want read_file", name)
			}
		},
		OnToolResult: func(name string, output string, isError bool) {
			toolResultCalled = true
			if isError {
				t.Error("tool result should not be an error")
			}
			if !strings.Contains(output, "hello world") {
				t.Errorf("tool output should contain 'hello world', got: %s", output)
			}
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if !toolUseCalled {
		t.Error("OnToolUse callback not called")
	}
	if !toolResultCalled {
		t.Error("OnToolResult callback not called")
	}
	if turnCount != 2 {
		t.Errorf("expected 2 turns, got %d", turnCount)
	}
	if len(resp.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool call name = %q, want read_file", resp.ToolCalls[0].Name)
	}
	if resp.Text != "File contents read successfully." {
		t.Errorf("Text = %q, want %q", resp.Text, "File contents read successfully.")
	}
	if resp.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50 (20+30)", resp.InputTokens)
	}
}

func TestSendAgentic_ReadOnlyToolCallsExecuteInParallel(t *testing.T) {
	turnCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++
		w.Header().Set("Content-Type", "text/event-stream")
		if turnCount == 1 {
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\n" +
					"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"grep_search\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"grep_search\",\"arguments\":\"{}\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":12,\"output_tokens\":8}}}\n\n",
			))
			return
		}
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	var inFlight int32
	var maxInFlight int32

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "parallel tools", &AgenticOptions{
		Model: "gpt-5.3-codex",
		ToolExecutor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			curr := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxInFlight)
				if curr <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, curr) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return "ok", false, nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if maxInFlight < 2 {
		t.Fatalf("expected parallel execution for read-only tools, max in-flight=%d", maxInFlight)
	}
}

func TestSendAgentic_MutatingToolMixStaysSerial(t *testing.T) {
	turnCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++
		w.Header().Set("Content-Type", "text/event-stream")
		if turnCount == 1 {
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{}\"}}\n\n" +
					"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"bash\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"bash\",\"arguments\":\"{}\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":12,\"output_tokens\":8}}}\n\n",
			))
			return
		}
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	var inFlight int32
	var maxInFlight int32

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "serial tools", &AgenticOptions{
		Model: "gpt-5.3-codex",
		ToolExecutor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			curr := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxInFlight)
				if curr <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, curr) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return "ok", false, nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if maxInFlight != 1 {
		t.Fatalf("expected serial execution when mutating tool is present, max in-flight=%d", maxInFlight)
	}
}

func TestSendAgentic_MaxTurns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Always return a tool call to test max turns limit
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\"}}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\": \\\"echo hi\\\"}\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "echo", &AgenticOptions{
		Model:    "gpt-5.3-codex",
		MaxTurns: 3,
		WorkDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	// Should have 3 tool calls (one per turn)
	if len(resp.ToolCalls) != 3 {
		t.Errorf("expected 3 tool calls (max turns=3), got %d", len(resp.ToolCalls))
	}
}

func TestSendAgentic_DefaultMaxTurnsNoLimit(t *testing.T) {
	const toolTurns = 30
	turnCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++
		w.Header().Set("Content-Type", "text/event-stream")

		if turnCount <= toolTurns {
			callID := fmt.Sprintf("call_%d", turnCount)
			_, _ = w.Write([]byte(fmt.Sprintf(
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"%s\",\"name\":\"bash\"}}\n\n"+
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"%s\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\": \\\"echo hi\\\"}\"}}\n\n"+
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
				callID, callID, turnCount,
			)))
			return
		}

		_, _ = w.Write([]byte(fmt.Sprintf(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"completed after many turns\"}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
			turnCount,
		)))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "echo", &AgenticOptions{
		Model:   "gpt-5.3-codex",
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if turnCount != toolTurns+1 {
		t.Fatalf("expected %d total turns, got %d", toolTurns+1, turnCount)
	}
	if len(resp.ToolCalls) != toolTurns {
		t.Fatalf("expected %d tool calls, got %d", toolTurns, len(resp.ToolCalls))
	}
	if resp.Text != "completed after many turns" {
		t.Fatalf("expected final text after >25 turns, got %q", resp.Text)
	}
}

func TestSendAgentic_DisableTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		json.Unmarshal(body, &reqBody)

		// Verify no tools in request
		if _, ok := reqBody["tools"]; ok {
			t.Error("expected no tools when DisableTools=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"No tools available.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "No tools available." {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestSendAgentic_WithRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("Text = %q, want ok", resp.Text)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestSendAgentic_DoesNotRetryRateLimit(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
	})
	if err == nil {
		t.Fatal("expected rate-limit error")
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

func TestSendAgentic_RetriesStreamAfterPartialOutputFromTurnState(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts == 1 {
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"))
			return
		}
		_, _ = w.Write([]byte(
			`data: {"type":"response.output_text.delta","delta":"done"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	var streamed strings.Builder
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
		OnText: func(text string) {
			streamed.WriteString(text)
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
	// The provider response is rebuilt from retry state, while the live stream
	// intentionally preserves text already emitted by the failed attempt.
	if streamed.String() != "partialdone" {
		t.Fatalf("streamed text = %q, want partialdone", streamed.String())
	}
}

func TestSendAgentic_RetriedTurnReplaysCompletedToolOutput(t *testing.T) {
	var attempts int32
	var toolExecutions int32
	var retrySawToolOutput bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if attempt == 3 {
			input, _ := reqBody["input"].([]any)
			for _, item := range input {
				m, _ := item.(map[string]any)
				if m["type"] == "function_call_output" && m["call_id"] == "call_1" && m["output"] == "tool result" {
					retrySawToolOutput = true
				}
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		switch attempt {
		case 1:
			args, _ := json.Marshal(`{"query":"x"}`)
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup"}}` + "\n\n" +
					fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":%s}}`, args) + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":4}}}` + "\n\n",
			))
		case 2:
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"))
		default:
			_, _ = w.Write([]byte(
				`data: {"type":"response.output_text.delta","delta":"final"}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_3","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":12,"output_tokens":2}}}` + "\n\n",
			))
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "use lookup", &AgenticOptions{
		Model:            "gpt-5.3-codex",
		SkipDefaultTools: true,
		ExtraTools:       []ToolDefinition{{Type: "function", Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolExecutor: func(context.Context, string, json.RawMessage) (string, bool, error) {
			atomic.AddInt32(&toolExecutions, 1)
			return "tool result", false, nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if got := atomic.LoadInt32(&toolExecutions); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
	if !retrySawToolOutput {
		t.Fatal("retried turn did not include completed function_call_output")
	}
	if resp.Text != "final" {
		t.Fatalf("Text = %q, want final", resp.Text)
	}
}

func TestSendAgentic_OAuthUsesCorrectEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("originator"); got != "codex_cli_rs" {
			t.Errorf("originator = %q, want codex_cli_rs", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "0.144.0" {
			t.Errorf("client_version = %q, want 0.144.0", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := body["model"]; got != "gpt-5.5" {
			t.Fatalf("model = %#v, want gpt-5.5", got)
		}
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "" {
			t.Fatalf("responses-lite header = %q, want omitted for HTTP SSE", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.5",
		DisableTools: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("Text = %q, want ok", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionBeforeFirstTurn_APIKey(t *testing.T) {
	requests := 0
	var compactionCallback string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		switch requests {
		case 1:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 1 path = %q, want /responses/compact", r.URL.Path)
			}
			if got := body["instructions"]; got != openAICompactionInstructions {
				t.Fatalf("instructions = %#v, want compaction instructions", got)
			}
			input := body["input"].([]any)
			msg := input[0].(map[string]any)
			content := msg["content"].(string)
			if !strings.Contains(content, "previously compacted history") {
				t.Fatalf("compaction input missing prior history: %s", content)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"previously compacted history"},{"type":"compaction","encrypted_content":"history summary"}]}`))
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			input := body["input"].([]any)
			if len(input) < 3 {
				t.Fatalf("input len = %d, want at least 3 (compacted history + current prompt)", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatal("expected compacted history to include compaction item")
			}
			currentMsg, ok := input[len(input)-1].(map[string]any)
			if !ok {
				t.Fatalf("last input item type = %T, want map[string]any", input[len(input)-1])
			}
			if got := currentMsg["content"].(string); got != "current task" {
				t.Fatalf("current prompt = %q, want current task", got)
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"final answer\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	client.History = []Message{
		{Role: "user", Content: "previously compacted history"},
		{Role: "assistant", Content: "assistant response"},
	}

	resp, err := client.SendAgentic(context.Background(), "current task", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
		OnCompaction: func(summary string) {
			compactionCallback = summary
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if compactionCallback != "history summary" {
		t.Fatalf("OnCompaction summary = %q, want history summary", compactionCallback)
	}
	if resp.Text != "final answer" {
		t.Fatalf("Text = %q, want final answer", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionUsesDedicatedCompactionPrompt(t *testing.T) {
	requests := 0
	systemPrompt := "SYSTEM: use managed memory and selected project skills"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		switch requests {
		case 1:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 1 path = %q, want /responses/compact", r.URL.Path)
			}
			if got := body["instructions"]; got != openAICompactionInstructions {
				t.Fatalf("compaction instructions = %#v, want %#v", got, openAICompactionInstructions)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"older context"},{"type":"compaction","encrypted_content":"summary"}]}`))
		case 2:
			if got := body["instructions"]; got != systemPrompt {
				t.Fatalf("turn instructions = %#v, want %#v", got, systemPrompt)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	client.History = []Message{
		{Role: "user", Content: "older context"},
		{Role: "assistant", Content: "assistant response"},
	}

	resp, err := client.SendAgentic(context.Background(), "current task", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		System:                   systemPrompt,
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionUsesCompactionPromptOverride(t *testing.T) {
	requests := 0
	customCompactionPrompt := "Summarize recent work only. Return concise notes."

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		switch requests {
		case 1:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 1 path = %q, want /responses/compact", r.URL.Path)
			}
			if got := body["instructions"]; got != customCompactionPrompt {
				t.Fatalf("compaction instructions = %#v, want %#v", got, customCompactionPrompt)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"older context"},{"type":"compaction","encrypted_content":"summary"}]}`))
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	client.History = []Message{
		{Role: "user", Content: "older context"},
		{Role: "assistant", Content: "assistant response"},
	}

	resp, err := client.SendAgentic(context.Background(), "current task", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
		CompactionPrompt:         customCompactionPrompt,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionMidTurn_APIKey(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0
	var compactionCallback string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		switch requests {
		case 1:
			w.Header().Set("Content-Type", "text/event-stream")
			if _, ok := body["tools"]; !ok {
				t.Fatal("first request should include tools")
			}
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			addedEvt := fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`)
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr)
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":5}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 2:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 2 path = %q, want /responses/compact", r.URL.Path)
			}
			input := body["input"].([]any)
			foundToolCall := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "function_call" {
					foundToolCall = true
					break
				}
			}
			if !foundToolCall {
				t.Fatal("compaction input missing function_call")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"Read test.txt"},{"type":"compaction","encrypted_content":"tool summary"}]}`))
		case 3:
			w.Header().Set("Content-Type", "text/event-stream")
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want at least 2 after compaction", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
				}
			}
			if !foundCompaction {
				t.Fatal("post-compaction request should include compaction item")
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Read test.txt", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		WorkDir:                  tmpDir,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
		OnCompaction: func(summary string) {
			compactionCallback = summary
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if compactionCallback != "tool summary" {
		t.Fatalf("OnCompaction summary = %q, want tool summary", compactionCallback)
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionOAuthUsesOAuthRequestShape(t *testing.T) {
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		if got := r.Header.Get("originator"); got != "codex_cli_rs" {
			t.Fatalf("originator = %q, want codex_cli_rs", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "0.144.0" {
			t.Fatalf("client_version = %q, want 0.144.0", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := body["model"]; got != "gpt-5.5" {
			t.Fatalf("model = %#v, want gpt-5.5", got)
		}
		if got := r.Header.Get("x-openai-internal-codex-responses-lite"); got != "" {
			t.Fatalf("responses-lite header = %q, want omitted for HTTP SSE", got)
		}
		if strings.HasSuffix(r.URL.Path, "/responses/compact") {
			if _, ok := body["store"]; ok {
				t.Fatalf("compaction payload should omit store, got %#v", body["store"])
			}
		} else {
			if got, ok := body["store"].(bool); !ok || got {
				t.Fatalf("store = %#v, want false", body["store"])
			}
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Fatalf("max_output_tokens should be omitted for OAuth requests")
		}

		if requests == 1 {
			if strings.HasSuffix(r.URL.Path, "/responses/compact") || !strings.HasSuffix(r.URL.Path, "/responses") {
				t.Fatalf("request 1 path = %q, want /responses compaction", r.URL.Path)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"oauth summary\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compact\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n",
			))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		input := body["input"].([]any)
		if len(input) < 2 {
			t.Fatalf("input len = %d, want compaction plus current prompt", len(input))
		}
		foundCompaction := false
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if ok && item["type"] == "compaction" {
				foundCompaction = true
				break
			}
		}
		if !foundCompaction {
			t.Fatalf("turn input missing compaction item: %#v", input)
		}
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	client.History = []Message{{Role: "user", Content: "oauth history"}}

	resp, err := client.SendAgentic(context.Background(), "continue", &AgenticOptions{
		Model:                    "gpt-5.5",
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionOAuthUsesResponsesEndpoint(t *testing.T) {
	requests := 0
	var compactionCallback string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		if got := r.Header.Get("originator"); got != "codex_cli_rs" {
			t.Fatalf("originator = %q, want codex_cli_rs", got)
		}
		if got := r.URL.Query().Get("client_version"); got != "0.144.0" {
			t.Fatalf("client_version = %q, want 0.144.0", got)
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := body["model"]; got != "gpt-5.5" {
			t.Fatalf("model = %#v, want gpt-5.5", got)
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Fatalf("max_output_tokens should be omitted for OAuth requests")
		}

		switch requests {
		case 1:
			if strings.HasSuffix(r.URL.Path, "/responses/compact") || !strings.HasSuffix(r.URL.Path, "/responses") {
				t.Fatalf("request 1 path = %q, want /responses compaction", r.URL.Path)
			}
			if got := body["instructions"]; got != "You are a helpful assistant." {
				t.Fatalf("compaction instructions = %#v, want base instructions", got)
			}
			if got, ok := body["store"].(bool); !ok || got {
				t.Fatalf("compaction store = %#v, want false", body["store"])
			}
			input := body["input"].([]any)
			trigger, ok := input[len(input)-1].(map[string]any)
			if !ok || trigger["type"] != "compaction_trigger" {
				t.Fatalf("last compaction input = %#v, want compaction_trigger", input[len(input)-1])
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"compaction\",\"encrypted_content\":\"oauth summary\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compact\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n",
			))
		case 2:
			if !strings.HasSuffix(r.URL.Path, "/responses") {
				t.Fatalf("request 2 path = %q, want /responses", r.URL.Path)
			}
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want compaction plus current prompt", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if ok && item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatalf("turn input missing compaction item: %#v", input)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken(testOAuthJWT("org_test"), "refresh", time.Now().Add(2*time.Hour).UnixMilli(), "org_test")
	client.History = []Message{{Role: "user", Content: "oauth history"}}

	resp, err := client.SendAgentic(context.Background(), "continue", &AgenticOptions{
		Model:                    "gpt-5.5",
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 1,
		OnCompaction: func(summary string) {
			compactionCallback = summary
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if compactionCallback != "oauth summary" {
		t.Fatalf("OnCompaction summary = %q, want oauth summary", compactionCallback)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want ok", resp.Text)
	}
}

func TestSendAgentic_ContextLengthExceeded_TriggersForcedCompactionAndRetry(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "tiny.txt")
	if err := os.WriteFile(testFile, []byte("ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		switch requests {
		case 1:
			if _, ok := body["tools"]; !ok {
				t.Fatal("first request should include tools")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			addedEvt := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr)
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":120,"output_tokens":20}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 2:
			if _, ok := body["tools"]; !ok {
				t.Fatal("overflow request should include tools")
			}
			input := body["input"].([]any)
			foundToolOutput := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "function_call_output" {
					foundToolOutput = true
					break
				}
			}
			if !foundToolOutput {
				t.Fatal("overflow request should include function_call_output")
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","code":"context_length_exceeded"}}`))
		case 3:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 3 path = %q, want /responses/compact", r.URL.Path)
			}
			if _, ok := body["tools"]; !ok {
				t.Fatal("compaction request should include tools field")
			}
			input := body["input"].([]any)
			foundToolCall := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "function_call" {
					foundToolCall = true
					break
				}
			}
			if !foundToolCall {
				t.Fatal("compaction input should include function_call item")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"Read tiny.txt"},{"type":"compaction","encrypted_content":"forced summary"}]}`))
		case 4:
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want at least 2 after forced compaction", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatal("retry request should include compaction item")
			}

			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Read tiny.txt", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		WorkDir:                  tmpDir,
		AutoCompaction:           true,
		CompactionTokenThreshold: 200000,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 4 {
		t.Fatalf("requests = %d, want 4", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
}

func TestSendAgentic_ContextLengthExceeded_NoAutoCompactionReturnsError(t *testing.T) {
	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded","code":"context_length_exceeded"}}`))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:           "gpt-5.3-codex",
		DisableTools:    true,
		AutoCompaction:  false,
		MaxOutputTokens: 512,
	})
	if err == nil {
		t.Fatal("expected SendAgentic to fail without auto compaction")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "context") {
		t.Fatalf("error = %q, want context-related failure", err.Error())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestSendAgentic_AutoCompactionMidTurn_UsesObservedInputTokens(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "tiny.txt")
	if err := os.WriteFile(testFile, []byte("ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		switch requests {
		case 1:
			if _, ok := body["tools"]; !ok {
				t.Fatal("first request should include tools")
			}
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			addedEvt := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr)
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":250000,"output_tokens":5}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 2:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 2 path = %q, want /responses/compact", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"Read tiny.txt"},{"type":"compaction","encrypted_content":"usage summary"}]}`))
		case 3:
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want at least 2 after compaction", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatal("post-compaction input missing compaction item")
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Read tiny.txt", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		WorkDir:                  tmpDir,
		AutoCompaction:           true,
		CompactionTokenThreshold: 200000,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionMidTurn_UsesSessionLevelTokenAccounting(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "tiny.txt")
	if err := os.WriteFile(testFile, []byte("ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		switch requests {
		case 1:
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			addedEvt := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":%s}}`, argsStr)
			// input_tokens stays below threshold, but the full observed session total
			// (input + output) crosses it once we account for the response itself.
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":199900,"output_tokens":400}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 2:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 2 path = %q, want /responses/compact", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"Read tiny.txt"},{"type":"compaction","encrypted_content":"session summary"}]}`))
		case 3:
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want at least 2 after compaction", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatal("post-compaction input missing compaction item")
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_3\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Read tiny.txt", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		WorkDir:                  tmpDir,
		AutoCompaction:           true,
		CompactionTokenThreshold: 200000,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
}

func TestSendAgentic_AutoCompactionMidTurn_UsesLatestSessionTokenBaseline(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "tiny.txt")
	if err := os.WriteFile(testFile, []byte("ok\n"), 0644); err != nil {
		t.Fatal(err)
	}

	requests := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		switch requests {
		case 1, 2:
			if _, ok := body["tools"]; !ok {
				t.Fatalf("request %d should include tools", requests)
			}
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			callID := fmt.Sprintf("call_%d", requests)
			addedEvt := fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"%s","name":"read_file"}}`, callID)
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"%s","name":"read_file","arguments":%s}}`, callID, argsStr)
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_turn","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":70000,"output_tokens":1000}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 3:
			if _, ok := body["tools"]; !ok {
				t.Fatalf("request %d should include tools", requests)
			}
			argsJSON, _ := json.Marshal(map[string]string{"file_path": testFile})
			argsStr, _ := json.Marshal(string(argsJSON))
			callID := fmt.Sprintf("call_%d", requests)
			addedEvt := fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"%s","name":"read_file"}}`, callID)
			doneEvt := fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"%s","name":"read_file","arguments":%s}}`, callID, argsStr)
			// The latest observed turn baseline alone should be enough to trigger compaction.
			completedEvt := `data: {"type":"response.completed","response":{"id":"resp_turn","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":205000,"output_tokens":1000}}}`
			_, _ = w.Write([]byte(addedEvt + "\n\n" + doneEvt + "\n\n" + completedEvt + "\n\n"))
		case 4:
			if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
				t.Fatalf("request 4 path = %q, want /responses/compact", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[{"type":"message","role":"user","content":"Read tiny.txt"},{"type":"compaction","encrypted_content":"latest session summary"}]}`))
		case 5:
			input := body["input"].([]any)
			if len(input) < 2 {
				t.Fatalf("input len = %d, want at least 2 after compaction", len(input))
			}
			foundCompaction := false
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "compaction" {
					foundCompaction = true
					break
				}
			}
			if !foundCompaction {
				t.Fatal("post-compaction input missing compaction item")
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "Read tiny.txt", &AgenticOptions{
		Model:                    "gpt-5.3-codex",
		WorkDir:                  tmpDir,
		AutoCompaction:           true,
		CompactionTokenThreshold: 200000,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requests != 5 {
		t.Fatalf("requests = %d, want 5", requests)
	}
	if !resp.Compacted {
		t.Fatal("expected response to report compaction")
	}
	if resp.Text != "done" {
		t.Fatalf("Text = %q, want done", resp.Text)
	}
}

func TestAgenticSessionTokenLedger_ProjectedTokens(t *testing.T) {
	ledger := &agenticSessionTokenLedger{}

	localItems := []any{
		agenticInputItem{
			"type":    "function_call_output",
			"call_id": "call_1",
			"output":  strings.Repeat("A", 40),
		},
	}

	if got := ledger.projectedTokens(localItems); got != estimateInputItemsTokens(localItems) {
		t.Fatalf("projectedTokens without observed baseline = %d, want %d", got, estimateInputItemsTokens(localItems))
	}

	ledger.observeTurn(&agenticTurnResult{inputTokens: 120, outputTokens: 30})
	want := 150 + estimateInputItemsTokens(localItems)
	if got := ledger.projectedTokens(localItems); got != want {
		t.Fatalf("projectedTokens with observed baseline = %d, want %d", got, want)
	}

	ledger.observeTurn(&agenticTurnResult{inputTokens: 80, outputTokens: 20})
	want = 100 + estimateInputItemsTokens(localItems)
	if got := ledger.projectedTokens(localItems); got != want {
		t.Fatalf("projectedTokens with latest observed total = %d, want %d", got, want)
	}

	ledger.reset()
	if got := ledger.projectedTokens(localItems); got != estimateInputItemsTokens(localItems) {
		t.Fatalf("projectedTokens after reset = %d, want %d", got, estimateInputItemsTokens(localItems))
	}
}

func TestTruncateToolOutputForModelInput_UsesTokenLimit(t *testing.T) {
	limit := 100
	input := strings.Repeat("x", 2000)
	got := truncateToolOutputForModelInput(input, limit)
	if got == input {
		t.Fatal("expected output to be truncated")
	}

	maxChars := normalizedToolOutputTokenLimit(limit) * 4
	if len([]rune(got)) > maxChars {
		t.Fatalf("truncated output len=%d, want <= %d", len([]rune(got)), maxChars)
	}
	if !strings.Contains(got, "Tool output truncated to fit model context") {
		t.Fatalf("expected truncation marker in output, got: %q", got)
	}
}

func TestSendAgentic_ToolOutputTruncatedOnlyForModelInput(t *testing.T) {
	const (
		tokenLimit = 100
		callID     = "call_1"
	)
	largeOutput := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 1200)

	requestCount := 0
	var modelInputOutput string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		var reqBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")

		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\"}}\n\n" +
					"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"bash\",\"arguments\":\"{\\\"command\\\": \\\"echo hi\\\"}\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
			))
		case 2:
			input, ok := reqBody["input"].([]any)
			if !ok {
				t.Fatalf("request input has unexpected type %T", reqBody["input"])
			}
			for _, raw := range input {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if item["type"] == "function_call_output" && item["call_id"] == callID {
					modelInputOutput = stringFromAny(item["output"])
				}
			}
			if modelInputOutput == "" {
				t.Fatal("expected function_call_output in request 2 input")
			}
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}}\n\n",
			))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "run command", &AgenticOptions{
		Model:                "gpt-5.3-codex",
		ToolOutputTokenLimit: tokenLimit,
		ToolExecutor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			if name != "bash" {
				t.Fatalf("unexpected tool call %q", name)
			}
			return largeOutput, false, nil
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if requestCount != 2 {
		t.Fatalf("requestCount = %d, want 2", requestCount)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Output != largeOutput {
		t.Fatalf("stored tool output should remain full length: got=%d want=%d", len(resp.ToolCalls[0].Output), len(largeOutput))
	}
	if len(modelInputOutput) >= len(largeOutput) {
		t.Fatalf("model input output should be truncated: got=%d full=%d", len(modelInputOutput), len(largeOutput))
	}
	if !strings.Contains(modelInputOutput, "Tool output truncated to fit model context") {
		t.Fatalf("missing truncation marker in model input output: %q", modelInputOutput)
	}

	maxChars := normalizedToolOutputTokenLimit(tokenLimit) * 4
	if len([]rune(modelInputOutput)) > maxChars {
		t.Fatalf("model input output len=%d, want <= %d", len([]rune(modelInputOutput)), maxChars)
	}
}

func TestShouldAutoCompactInputItems_CompactsLargeTranscript(t *testing.T) {
	inputItems := []any{
		agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": strings.Repeat("A", DefaultCompactionThreshold*4),
		},
	}

	if tokens := estimateInputItemsTokens(inputItems); tokens < DefaultCompactionThreshold {
		t.Fatalf("expected large transcript estimate, got %d tokens", tokens)
	}
	if !shouldAutoCompactInputItems(inputItems, DefaultCompactionThreshold) {
		t.Fatal("expected large transcript to trigger compaction")
	}
}

func TestShouldAutoCompactInputItems_DoesNotCompactSmallTranscript(t *testing.T) {
	inputItems := []any{
		agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": "Investigate the bug.",
		},
	}

	if shouldAutoCompactInputItems(inputItems, DefaultCompactionThreshold) {
		t.Fatal("did not expect compaction for a small transcript")
	}
}

func TestOpenAIAutoCompactionTokenLimit_UsesEffectiveContextPercent(t *testing.T) {
	got := openAIAutoCompactionTokenLimit("gpt-5.3-codex")
	want := (272000 * 95) / 100
	if got != want {
		t.Fatalf("openAIAutoCompactionTokenLimit = %d, want %d", got, want)
	}
}

func TestOpenAIAutoCompactionTokenLimit_GPT56UsesExpandedContext(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			got := openAIAutoCompactionTokenLimit(model)
			want := (272000 * 95) / 100
			if got != want {
				t.Fatalf("openAIAutoCompactionTokenLimit(%q) = %d, want %d", model, got, want)
			}
		})
	}
}

func TestOpenAIAutoCompactionTokenLimit_GPT6AstraUsesFullContext(t *testing.T) {
	got := openAIAutoCompactionTokenLimit("gpt-6-astra")
	want := (1050000 * 95) / 100
	if got != want {
		t.Fatalf("openAIAutoCompactionTokenLimit(gpt-6-astra) = %d, want %d", got, want)
	}
}

func TestOpenAIAutoCompactionTokenLimit_SparkUses128KContext(t *testing.T) {
	got := openAIAutoCompactionTokenLimit("gpt-5.3-codex-spark")
	want := (128000 * 95) / 100
	if got != want {
		t.Fatalf("openAIAutoCompactionTokenLimit = %d, want %d", got, want)
	}
}

func TestNormalizedCompactionThresholdForModel_CapsToModelLimit(t *testing.T) {
	modelLimit := openAIAutoCompactionTokenLimit("gpt-5.3-codex")
	got := normalizedCompactionThresholdForModel(modelLimit+50000, "gpt-5.3-codex")
	if got != modelLimit {
		t.Fatalf("normalizedCompactionThresholdForModel = %d, want %d", got, modelLimit)
	}
}

func TestClampCompactionTranscript_PreservesHeadAndTail(t *testing.T) {
	head := "USER:\nTask objective: move Idea Quality Grade off /history and redesign it to match How Am I Doing.\n\n"
	middle := strings.Repeat("TOOL_RESULT read_file:\nnoise\n\n", 12000)
	tail := "TOOL_RESULT grep_search:\nlatest matching lines near insights templ and history templ\n\n"

	transcript := head + middle + tail
	clamped := clampCompactionTranscript(transcript)

	if len([]rune(clamped)) > openAICompactionTranscriptLimit {
		t.Fatalf("clamped transcript length = %d, want <= %d", len([]rune(clamped)), openAICompactionTranscriptLimit)
	}
	if !strings.Contains(clamped, "Task objective: move Idea Quality Grade off /history") {
		t.Fatalf("expected clamped transcript to preserve head, got %q", clamped[:min(len(clamped), 300)])
	}
	if !strings.Contains(clamped, "latest matching lines near insights templ and history templ") {
		t.Fatalf("expected clamped transcript to preserve tail")
	}
	if !strings.Contains(clamped, "[Middle conversation content omitted before compaction]") {
		t.Fatal("expected clamped transcript to include omission marker")
	}
}

func TestIsCodexGeneratedInputItem_CoversToolCallTypes(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
		want bool
	}{
		{
			name: "function_call",
			item: map[string]any{"type": "function_call"},
			want: true,
		},
		{
			name: "function_call_output",
			item: map[string]any{"type": "function_call_output"},
			want: true,
		},
		{
			name: "tool_search_call",
			item: map[string]any{"type": "tool_search_call"},
			want: true,
		},
		{
			name: "custom_tool_call_output",
			item: map[string]any{"type": "custom_tool_call_output"},
			want: true,
		},
		{
			name: "developer_message",
			item: map[string]any{"type": "message", "role": "developer"},
			want: true,
		},
		{
			name: "user_message",
			item: map[string]any{"type": "message", "role": "user"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCodexGeneratedInputItem(tt.item)
			if got != tt.want {
				t.Fatalf("isCodexGeneratedInputItem(%v) = %v, want %v", tt.item, got, tt.want)
			}
		})
	}
}

func TestTrimCompactionInputItemsToFitContextWindow_TrimsTrailingFunctionCall(t *testing.T) {
	inputItems := []any{
		agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": "Task objective: diagnose compaction behavior",
		},
		agenticInputItem{
			"type":      "function_call",
			"call_id":   "call_1",
			"name":      "read_file",
			"arguments": `{"file_path":"MEMORY.md","padding":"` + strings.Repeat("A", 1_200_000) + `"}`,
		},
	}

	trimmed := trimCompactionInputItemsToFitContextWindow(inputItems, nil, "", "gpt-5.3-codex")
	if len(trimmed) != 1 {
		t.Fatalf("trimmed len = %d, want 1", len(trimmed))
	}
	first, ok := trimmed[0].(map[string]any)
	if !ok {
		t.Fatalf("trimmed[0] type = %T, want map[string]any", trimmed[0])
	}
	if got := first["type"]; got != "message" {
		t.Fatalf("trimmed[0].type = %v, want message", got)
	}
}

func TestTrimCompactionInputItemsToFitContextWindow_PreservesObjectiveAndRecentContext(t *testing.T) {
	inputItems := []any{
		agenticInputItem{
			"type":    "message",
			"role":    "user",
			"content": "Task objective: diagnose compaction behavior",
		},
		agenticInputItem{
			"type":      "function_call",
			"call_id":   "call_1",
			"name":      "read_file",
			"arguments": `{"file_path":"MEMORY.md","padding":"` + strings.Repeat("A", 900_000) + `"}`,
		},
		agenticInputItem{
			"type":    "function_call_output",
			"call_id": "call_1",
			"output":  strings.Repeat("B", 900_000),
		},
		agenticInputItem{
			"type":    "message",
			"role":    "assistant",
			"content": "Latest context: reviewing compaction handoff and next fix.",
		},
	}

	trimmed := trimCompactionInputItemsToFitContextWindow(inputItems, nil, "", "gpt-5.3-codex")
	if len(trimmed) >= len(inputItems) {
		t.Fatalf("expected compaction input to be trimmed; len=%d original=%d", len(trimmed), len(inputItems))
	}
	if estimateCompactionRequestTokens(trimmed, nil, "") > 272000 {
		t.Fatalf("trimmed payload should fit context window; estimated tokens=%d", estimateCompactionRequestTokens(trimmed, nil, ""))
	}

	first, ok := trimmed[0].(map[string]any)
	if !ok {
		t.Fatalf("trimmed[0] type = %T, want map[string]any", trimmed[0])
	}
	if got := first["type"]; got != "message" {
		t.Fatalf("trimmed[0].type = %v, want message", got)
	}
	if got := first["role"]; got != "user" {
		t.Fatalf("trimmed[0].role = %v, want user", got)
	}

	last, ok := trimmed[len(trimmed)-1].(map[string]any)
	if !ok {
		t.Fatalf("trimmed[last] type = %T, want map[string]any", trimmed[len(trimmed)-1])
	}
	if got := last["type"]; got != "message" {
		t.Fatalf("trimmed[last].type = %v, want message", got)
	}
	if got := last["role"]; got != "assistant" {
		t.Fatalf("trimmed[last].role = %v, want assistant", got)
	}
}

func TestToolDefinitionMarshal(t *testing.T) {
	tools := DefaultTools()
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}

	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("tool %s type = %q, want function", tool.Name, tool.Type)
		}
		if tool.Name == "" {
			t.Errorf("tool has empty name")
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal tool %s: %v", tool.Name, err)
		}

		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal tool %s: %v", tool.Name, err)
		}

		if m["type"] != "function" {
			t.Errorf("tool %s JSON type = %v, want function", tool.Name, m["type"])
		}
		if m["name"] == nil || m["name"] == "" {
			t.Error("name is empty")
		}
		if m["description"] == nil || m["description"] == "" {
			t.Error("description is empty")
		}
		if m["parameters"] == nil {
			t.Error("parameters is nil")
		}
	}
}

func TestAgenticResponse_UpdatesHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Response text\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "Hello", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}

	if len(client.History) != 2 {
		t.Fatalf("history len = %d, want 2", len(client.History))
	}
	if client.History[0].Role != "user" || client.History[0].Content != "Hello" {
		t.Errorf("history[0] = %+v", client.History[0])
	}
	if client.History[1].Role != "assistant" || client.History[1].Content != "Response text" {
		t.Errorf("history[1] = %+v", client.History[1])
	}
}

func TestSendAgentic_DisableToolsWithExtraTools(t *testing.T) {
	// Verify that when DisableTools=true, ExtraTools are also excluded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		json.Unmarshal(body, &reqBody)

		// Verify no tools in request (neither built-in nor extra)
		if _, ok := reqBody["tools"]; ok {
			t.Error("expected no tools when DisableTools=true, even with ExtraTools provided")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:        "gpt-5.3-codex",
		DisableTools: true,
		ExtraTools: []ToolDefinition{
			{
				Type:        "function",
				Name:        "test_tool",
				Description: "A test MCP tool",
			},
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestSendAgentic_ToolFilterRemovesDeniedToolsFromRequest(t *testing.T) {
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
			seen[fmt.Sprint(tool["name"])] = true
		}
		if !seen["memory_view"] || seen["list_files"] || seen["Bash"] {
			t.Fatalf("expected memory_view only, got %#v", seen)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model: "gpt-5.3-codex",
		ExtraTools: []ToolDefinition{
			{
				Type:        "function",
				Name:        "memory_view",
				Description: "Load an authorized selected memory",
			},
		},
		ToolFilter: func(name string) bool { return name == "memory_view" }})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestSendAgentic_SkipDefaultToolsUsesOnlyExtraTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)

		tools, ok := reqBody["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("expected exactly one extra tool, got %#v", reqBody["tools"])
		}
		tool, _ := tools[0].(map[string]any)
		if tool["name"] != "memory_read" {
			t.Fatalf("expected only runtime tool, got %#v", tool)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:            "gpt-5.3-codex",
		SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{
			{
				Type:        "function",
				Name:        "memory_read",
				Description: "Read memory",
			},
		},
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestOpenAIModelSupportsWebSearch(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gpt-6-astra", true},
		{"gpt-5.6-sol", false},
		{"gpt-5.6-terra", false},
		{"gpt-5.6-luna", false},
		{"gpt-5.4", true},
		{"gpt-5.4-mini", true},
		{"GPT-5.4", true},
		{"gpt-5.3-codex", true},
		{"gpt-5.3", true},
		{"gpt-5.2", true},
		{"gpt-5.2-mini", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4.1", false},
		{"o3", false},
		{"o3-mini", false},
		{"", false},
	}
	for _, tt := range tests {
		got := openAIModelSupportsWebSearch(tt.model)
		if got != tt.expected {
			t.Errorf("openAIModelSupportsWebSearch(%q) = %v, want %v", tt.model, got, tt.expected)
		}
	}
}

func TestIsProviderNativeOutputItem(t *testing.T) {
	tests := []struct {
		itemType string
		expected bool
	}{
		{"web_search_call", true},
		{"tool_search_call", true},
		{"Web_Search_Call", true},
		{"function_call", false},
		{"message", false},
		{"reasoning", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isProviderNativeOutputItem(tt.itemType)
		if got != tt.expected {
			t.Errorf("isProviderNativeOutputItem(%q) = %v, want %v", tt.itemType, got, tt.expected)
		}
	}
}

func TestSendAgentic_WebSearchToolIncluded(t *testing.T) {
	// Verify that web_search is added to the tools payload
	// when WebSearchEnabled=true and model supports it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		tools, ok := reqBody["tools"].([]interface{})
		if !ok {
			t.Fatal("expected tools array in request")
		}
		// Should have default tools + web_search
		foundWebSearch := false
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			if !ok {
				continue
			}
			if toolMap["type"] == openAIWebSearchToolType {
				foundWebSearch = true
			}
		}
		if !foundWebSearch {
			t.Errorf("expected %q tool in request, not found", openAIWebSearchToolType)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Found it.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	resp, err := client.SendAgentic(context.Background(), "search for Go docs", &AgenticOptions{
		Model:            "gpt-5.3-codex",
		WebSearchEnabled: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Found it." {
		t.Errorf("Text = %q, want %q", resp.Text, "Found it.")
	}
}

func TestSendAgentic_WebSearchNotIncludedForUnsupportedModel(t *testing.T) {
	// Verify that web_search is NOT added when the model doesn't support it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		tools, ok := reqBody["tools"].([]interface{})
		if !ok {
			// No tools array is also fine for unsupported model
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-4o\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
			))
			return
		}
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			if !ok {
				continue
			}
			if toolMap["type"] == openAIWebSearchToolType {
				t.Errorf("%q should NOT be in request for unsupported model", openAIWebSearchToolType)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-4o\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIAPIBaseURL = oldBaseURL }()

	client := NewWithAPIKey("sk-test")
	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:            "gpt-4o",
		WebSearchEnabled: true, // enabled, but model doesn't support it
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
}

func TestSendAgentic_WebSearchToolIncluded_OAuthPath(t *testing.T) {
	// Regression: ChatGPT OAuth codex endpoint rejects legacy
	// "web_search_preview", so ensure we send "web_search".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		tools, ok := reqBody["tools"].([]interface{})
		if !ok {
			t.Fatal("expected tools array in request")
		}
		foundWebSearch := false
		foundLegacy := false
		for _, tool := range tools {
			toolMap, ok := tool.(map[string]interface{})
			if !ok {
				continue
			}
			if toolMap["type"] == openAIWebSearchToolType {
				foundWebSearch = true
			}
			if toolMap["type"] == "web_search_preview" {
				foundLegacy = true
			}
		}
		if !foundWebSearch {
			t.Errorf("expected %q tool in request, not found", openAIWebSearchToolType)
		}
		if foundLegacy {
			t.Error("did not expect legacy web_search_preview tool type on OAuth path")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Done.\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.3-codex\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken("oauth-token", "refresh-token", time.Now().Add(2*time.Hour).UnixMilli(), "acct_123")
	resp, err := client.SendAgentic(context.Background(), "search for go docs", &AgenticOptions{
		Model:            "gpt-5.3-codex",
		WebSearchEnabled: true,
	})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "Done." {
		t.Errorf("Text = %q, want %q", resp.Text, "Done.")
	}
}

func TestParseAgenticStream_WebSearchCall(t *testing.T) {
	// Simulate a stream with a web_search_call output item.
	stream := buildSSE([]string{
		`{"type":"response.output_text.delta","delta":"Let me search for that."}`,
		`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","results":[{"title":"Go Docs","url":"https://go.dev/doc","snippet":"Official Go documentation"}]}}`,
		`{"type":"response.output_text.delta","delta":" Here's what I found."}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":20,"output_tokens":15}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// web_search_call should be in outputItems but NOT in toolCalls
	if len(result.toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d (web search should not create tool calls)", len(result.toolCalls))
	}

	// Should have the web_search_call in outputItems
	foundWebSearch := false
	for _, item := range result.outputItems {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if stringFromAny(m["type"]) == "web_search_call" {
			foundWebSearch = true
			break
		}
	}
	if !foundWebSearch {
		t.Error("expected web_search_call in outputItems")
	}
}

func TestParseAgenticStream_WebSearchWithFunctionCall(t *testing.T) {
	// Verify that web_search_call items coexist with function_call items.
	stream := buildSSE([]string{
		`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","results":[]}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"read_file"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"file_path\":\"main.go\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":10,"output_tokens":8}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Should have 1 function_call tool call
	if len(result.toolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.toolCalls))
	}
	if result.toolCalls[0].Name != "read_file" {
		t.Errorf("tool call name = %q, want read_file", result.toolCalls[0].Name)
	}

	// Should have both items in outputItems
	webSearchCount := 0
	functionCallCount := 0
	for _, item := range result.outputItems {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch stringFromAny(m["type"]) {
		case "web_search_call":
			webSearchCount++
		case "function_call":
			functionCallCount++
		}
	}
	if webSearchCount != 1 {
		t.Errorf("expected 1 web_search_call in outputItems, got %d", webSearchCount)
	}
	if functionCallCount != 1 {
		t.Errorf("expected 1 function_call in outputItems, got %d", functionCallCount)
	}
}

func TestParseAgenticStream_WebSearchCallEmitsToolCallbacks(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"golang context cancellation","sources":[{"url":"https://go.dev"}]}}}`,
		`{"type":"response.output_text.delta","delta":"Found useful results."}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":11,"output_tokens":7}}}`,
	})

	var (
		toolUseCount    int
		toolResultCount int
		useName         string
		useInput        json.RawMessage
		resultName      string
		resultOutput    string
		resultIsError   bool
	)

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStreamWithToolCallbacks(
		strings.NewReader(stream),
		nil,
		nil,
		func(name string, input json.RawMessage) {
			toolUseCount++
			useName = name
			useInput = append(json.RawMessage{}, input...)
		},
		func(name string, output string, isError bool) {
			toolResultCount++
			resultName = name
			resultOutput = output
			resultIsError = isError
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 0 {
		t.Fatalf("expected 0 local tool calls for provider-native web search, got %d", len(result.toolCalls))
	}
	if toolUseCount != 1 {
		t.Fatalf("toolUseCount = %d, want 1", toolUseCount)
	}
	if toolResultCount != 1 {
		t.Fatalf("toolResultCount = %d, want 1", toolResultCount)
	}
	if useName != "web_search" {
		t.Errorf("useName = %q, want web_search", useName)
	}
	if resultName != "web_search" {
		t.Errorf("resultName = %q, want web_search", resultName)
	}
	if resultIsError {
		t.Error("resultIsError = true, want false")
	}
	if !strings.Contains(resultOutput, "status: completed") {
		t.Errorf("resultOutput = %q, expected status marker", resultOutput)
	}

	var inputMap map[string]any
	if err := json.Unmarshal(useInput, &inputMap); err != nil {
		t.Fatalf("unmarshal useInput: %v", err)
	}
	if inputMap["query"] != "golang context cancellation" {
		t.Errorf("query = %v, want %q", inputMap["query"], "golang context cancellation")
	}
}

func TestParseAgenticStream_WebSearchCallUsesAddedDetailsForToolUse(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"in_progress","action":{"type":"search","query":"summarize crunchydata wal recovery","sources":[{"url":"https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts"}]}}}`,
		`{"type":"response.output_item.done","item":{"type":"web_search_call","id":"ws_1","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":12,"output_tokens":4}}}`,
	})

	var (
		toolUseCount    int
		toolResultCount int
		useInput        json.RawMessage
		resultOutput    string
	)

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStreamWithToolCallbacks(
		strings.NewReader(stream),
		nil,
		nil,
		func(_ string, input json.RawMessage) {
			toolUseCount++
			useInput = append(json.RawMessage{}, input...)
		},
		func(_ string, output string, _ bool) {
			toolResultCount++
			resultOutput = output
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if toolUseCount != 1 {
		t.Fatalf("toolUseCount = %d, want 1", toolUseCount)
	}
	if toolResultCount != 1 {
		t.Fatalf("toolResultCount = %d, want 1", toolResultCount)
	}

	var inputMap map[string]any
	if err := json.Unmarshal(useInput, &inputMap); err != nil {
		t.Fatalf("unmarshal useInput: %v", err)
	}
	if inputMap["query"] != "summarize crunchydata wal recovery" {
		t.Errorf("query = %v, want %q", inputMap["query"], "summarize crunchydata wal recovery")
	}
	if inputMap["url"] != "https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts" {
		t.Errorf("url = %v", inputMap["url"])
	}
	if !strings.Contains(resultOutput, "status: completed") {
		t.Errorf("resultOutput = %q, expected status marker", resultOutput)
	}
	if strings.Contains(resultOutput, "url=") || strings.Contains(resultOutput, "query=") {
		t.Errorf("resultOutput should not include serialized detail tuples: %q", resultOutput)
	}
}

func TestParseAgenticStream_WebSearchCallUsesDoneDetailsWhenAddedIsSparse(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_2","status":"in_progress","action":{"type":"search"}}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"search","query":"openvibely web search url display"}}}`,
		`{"type":"response.completed","response":{"id":"resp_2","status":"completed","model":"gpt-5.3-codex","usage":{"input_tokens":9,"output_tokens":3}}}`,
	})

	var (
		toolUseCount int
		useInput     json.RawMessage
	)

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	_, err := client.parseAgenticStreamWithToolCallbacks(
		strings.NewReader(stream),
		nil,
		nil,
		func(_ string, input json.RawMessage) {
			toolUseCount++
			useInput = append(json.RawMessage{}, input...)
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if toolUseCount != 1 {
		t.Fatalf("toolUseCount = %d, want 1", toolUseCount)
	}

	var inputMap map[string]any
	if err := json.Unmarshal(useInput, &inputMap); err != nil {
		t.Fatalf("unmarshal useInput: %v", err)
	}
	if inputMap["query"] != "openvibely web search url display" {
		t.Errorf("query = %v", inputMap["query"])
	}
}

func TestLegacyToolCallsUnaffectedByWebSearch(t *testing.T) {
	// Regression test: ensure normal function call behavior is unaffected
	// when WebSearchEnabled is false.
	stream := buildSSE([]string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"bash"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"command\":\"echo hello\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-4o","usage":{"input_tokens":5,"output_tokens":3}}}`,
	})

	client := &Client{auth: &StoredAuth{APIKey: "test"}}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.toolCalls))
	}
	if result.toolCalls[0].Name != "bash" {
		t.Errorf("name = %q, want bash", result.toolCalls[0].Name)
	}
	if result.toolCalls[0].Arguments != `{"command":"echo hello"}` {
		t.Errorf("args = %q", result.toolCalls[0].Arguments)
	}
}

func TestSendAgenticTurnRecoversOAuthUnauthorizedAndRetries(t *testing.T) {
	var seenAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if len(seenAuth) == 1 {
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		}))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken("old-token", "old-refresh", time.Now().Add(time.Hour).UnixMilli(), "acct")
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, tokenUsed string) (OAuthTokens, bool, error) {
		if tokenUsed != "old-token" {
			t.Fatalf("tokenUsed = %q", tokenUsed)
		}
		return OAuthTokens{AccessToken: "new-token", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(), AccountID: "acct"}, true, nil
	})

	resp, err := client.SendAgentic(context.Background(), "hello", &AgenticOptions{Model: "gpt-test", MaxTurns: 1, DisableTools: true})
	if err != nil {
		t.Fatalf("SendAgentic: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(seenAuth) != 2 {
		t.Fatalf("requests = %d, want 2", len(seenAuth))
	}
	if seenAuth[0] != "Bearer old-token" || seenAuth[1] != "Bearer new-token" {
		t.Fatalf("auth headers = %#v", seenAuth)
	}
	if client.CurrentAuth().Token != "new-token" || client.CurrentAuth().RefreshToken != "new-refresh" {
		t.Fatalf("client auth not updated: %#v", client.CurrentAuth())
	}
}

func TestSendRecoversOAuthUnauthorizedAndRetries(t *testing.T) {
	var seenAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if len(seenAuth) == 1 {
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, buildSSE([]string{
			`{"type":"response.output_text.delta","delta":"ok"}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		}))
	}))
	defer srv.Close()

	oldChatGPTBaseURL := OpenAIChatGPTAPIBaseURL
	OpenAIChatGPTAPIBaseURL = srv.URL + "/"
	defer func() { OpenAIChatGPTAPIBaseURL = oldChatGPTBaseURL }()

	client := NewWithOAuthToken("old-token", "old-refresh", time.Now().Add(time.Hour).UnixMilli(), "acct")
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, tokenUsed string) (OAuthTokens, bool, error) {
		if tokenUsed != "old-token" {
			t.Fatalf("tokenUsed = %q", tokenUsed)
		}
		return OAuthTokens{AccessToken: "new-token", RefreshToken: "new-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(), AccountID: "acct"}, true, nil
	})

	resp, err := client.Send(context.Background(), "hello", &SendOptions{Model: "gpt-test"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(seenAuth) != 2 {
		t.Fatalf("requests = %d, want 2", len(seenAuth))
	}
	if seenAuth[0] != "Bearer old-token" || seenAuth[1] != "Bearer new-token" {
		t.Fatalf("auth headers = %#v", seenAuth)
	}
}

func TestExternallyManagedOAuthSkipsPackagePreflightRefresh(t *testing.T) {
	client := NewWithOAuthToken("old-token", "old-refresh", time.Now().Add(-time.Hour).UnixMilli(), "acct")
	client.SetOAuthUnauthorizedHandler(func(ctx context.Context, tokenUsed string) (OAuthTokens, bool, error) {
		return OAuthTokens{}, false, nil
	})
	if err := client.ensureValidToken(); err != nil {
		t.Fatalf("ensureValidToken: %v", err)
	}
	if client.CurrentAuth().Token != "old-token" || client.CurrentAuth().RefreshToken != "old-refresh" {
		t.Fatalf("client auth changed unexpectedly: %#v", client.CurrentAuth())
	}
}

func TestOpenAIAgenticCompactionTranscriptAndImageHelpers(t *testing.T) {
	if got := openAICompactionOutputTokens(0); got != 4096 {
		t.Fatalf("default compaction tokens = %d", got)
	}
	if got := openAICompactionOutputTokens(128); got != 512 {
		t.Fatalf("minimum compaction tokens = %d", got)
	}
	if got := openAICompactionOutputTokens(2048); got != 2048 {
		t.Fatalf("explicit compaction tokens = %d", got)
	}
	if got := openAICompactionOutputTokens(9999); got != 4096 {
		t.Fatalf("max compaction tokens = %d", got)
	}

	transcript := openAIInputItemsTranscript([]any{
		map[string]any{"type": "message", "role": "system", "content": "system note"},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}, map[string]any{"type": "input_image", "image_url": "ignored"}}},
		map[string]any{"type": "function_call", "name": "read_file", "arguments": `{"file_path":"README.md"}`},
		map[string]any{"type": "function_call_output", "call_id": "call-1", "output": map[string]any{"text": "file output"}},
		map[string]any{"type": "unknown", "value": "kept"},
		"ignored",
	})
	for _, want := range []string{"SYSTEM:\nsystem note", "USER:\nhello", "TOOL_CALL read_file", `{"file_path":"README.md"}`, "TOOL_RESULT call-1", "file output", `"type":"unknown"`} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q in:\n%s", want, transcript)
		}
	}
	if got := openAIInputItemsTranscript([]any{map[string]any{"type": "message", "role": "user", "content": "   "}}); got != "" {
		t.Fatalf("blank transcript item should be omitted, got %q", got)
	}

	short := "short transcript"
	if got := clampCompactionTranscript(short); got != short {
		t.Fatalf("short transcript should not be clamped: %q", got)
	}
	long := strings.Repeat("a", openAICompactionTranscriptLimit+100) + "tail"
	clamped := clampCompactionTranscript(long)
	if len([]rune(clamped)) > openAICompactionTranscriptLimit || !strings.Contains(clamped, openAICompactionTranscriptGap) || !strings.HasSuffix(clamped, "tail") {
		t.Fatalf("unexpected clamped transcript length=%d value suffix=%q", len([]rune(clamped)), clamped[len(clamped)-10:])
	}

	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	dataURL := "data:image/png;base64," + onePixelPNG
	if payload, ok := parseAgenticBase64DataURL(dataURL, "image/"); !ok || payload != onePixelPNG {
		t.Fatalf("parseAgenticBase64DataURL = %q, %v", payload, ok)
	}
	if _, ok := parseAgenticBase64DataURL("data:text/plain;base64,"+onePixelPNG, "image/"); ok {
		t.Fatal("text data URL should not match image prefix")
	}
	if got := estimateAgenticOriginalImageBytes(dataURL); got <= 0 || got == openAIResizedImageBytesEstimate {
		t.Fatalf("expected data URL image byte estimate from dimensions, got %d", got)
	}
	if got := estimateAgenticOriginalImageBytes("https://example.test/image.png"); got != openAIResizedImageBytesEstimate {
		t.Fatalf("non-data URL estimate = %d", got)
	}
}
