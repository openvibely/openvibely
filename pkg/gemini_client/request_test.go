package geminiclient

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
)

func TestPreparedRequestBudgetAndPermissions(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		c := NewWithAPIKey("test")
		if oauth {
			c = NewWithOAuth("token", "project")
		}
		cache := &requestPreparation{}
		opts := &Options{Model: "gemini-3.8-flash", System: "system <&>", MaxOutputTokens: 1024}
		contents := []Content{{Role: "user", Parts: []Part{{Text: "question"}, {InlineData: &InlineData{MimeType: "image/png", Data: strings.Repeat("a", 1024*1024)}}}}}
		defs := []ToolDefinition{{Name: "allowed", Parameters: json.RawMessage(`{"type":"object"}`)}}
		first, tokens, err := c.prepareRequest(contents, defs, opts, cache)
		if err != nil {
			t.Fatal(err)
		}
		if tokens != estimateRequestTokens(first) {
			t.Fatalf("budget differs: %d != %d", tokens, estimateRequestTokens(first))
		}
		oldParts := cache.contents[0].Parts
		oldTools := cache.tools
		contents = append(contents, TextContent("model", "answer"))
		_, _, err = c.prepareRequest(contents, defs, opts, cache)
		if err != nil {
			t.Fatal(err)
		}
		if &oldParts[0] != &cache.contents[0].Parts[0] || oldTools != cache.tools {
			t.Fatal("unchanged fields were encoded again")
		}
		opts.ToolFilter = func(string) bool { return false }
		body, _, err := c.prepareRequest(contents, defs, opts, cache)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "functionDeclarations") {
			t.Fatal("cached tools bypassed permission change")
		}
		contents[0].Parts[0].Text = "changed"
		body, _, err = c.prepareRequest(contents, defs, opts, cache)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "changed") {
			t.Fatal("stale content reused")
		}
	}
}

func TestContentSizingAndBorrowedReplay(t *testing.T) {
	raw := json.RawMessage(`{"role":"user","parts":[{"functionResponse":{"id":"call","name":"tool","scheduling":"WHEN_IDLE","response":{"output":"large <&> answer","nested":{"list":[1,true,null]}},"parts":[{"fileData":{"fileUri":"https://example.com/image","mimeType":"image/png"}}]}}]}`)
	content, err := DecodeReplayContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	response := content.Parts[0].FunctionResponse.Response
	if _, ok := response["nested"].(jsonview.Value); !ok {
		t.Fatal("tool data was eagerly decoded")
	}
	clone := CloneContents([]Content{content})
	clone[0].Parts[0].FunctionResponse.Response["output"] = "changed"
	if _, ok := response["output"].(jsonview.Value); !ok {
		t.Fatal("clone changed source")
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("replay changed: %s", encoded)
	}
	size, err := requestjson.Size(contentValue(content))
	if err != nil || size != len(encoded) {
		t.Fatalf("size=%d encoded=%d err=%v", size, len(encoded), err)
	}
	tokens, err := EstimateContentsTokens([]Content{content})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(generateRequest{Contents: []Content{content}})
	// The request envelope only adds a few text tokens.
	if delta := estimateRequestTokens(body) - tokens; delta < 0 || delta > 10 {
		t.Fatalf("budget mismatch: %d", delta)
	}
}

func BenchmarkGeminiHistoryBudgetLargeToolResult(b *testing.B) {
	raw, _ := json.Marshal(map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "tool", "response": map[string]any{"output": strings.Repeat("x", 1024*1024)}}}}})
	content, err := DecodeReplayContent(raw)
	if err != nil {
		b.Fatal(err)
	}
	history := []Content{content}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EstimateContentsTokens(history); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPreparedRequestDetectsNestedMutation(t *testing.T) {
	c := NewWithAPIKey("test")
	cache := &requestPreparation{}
	opts := &Options{Model: "gemini-3.8-flash"}
	response := map[string]any{"output": "before"}
	contents := []Content{{Role: "user", Parts: []Part{{FunctionResponse: &FunctionResponse{Name: "tool", Response: response}}}}}
	if _, _, err := c.prepareRequest(contents, nil, opts, cache); err != nil {
		t.Fatal(err)
	}
	response["output"] = "after"
	body, _, err := c.prepareRequest(contents, nil, opts, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "after") || strings.Contains(string(body), "before") {
		t.Fatal("cached stale tool response")
	}
}

func TestContentProjectionMatchesProtocolEncoding(t *testing.T) {
	content := Content{Role: "model", Parts: []Part{
		{Text: "<>&\n", Thought: true, ThoughtSignature: "signature", VideoMetadata: json.RawMessage(`{"startOffset":"1s"}`)},
		{ExecutableCode: json.RawMessage(`{"code":"print(1)"}`), CodeExecutionResult: json.RawMessage(`{"output":"1"}`)},
		{FunctionCall: &FunctionCall{ID: "call", Name: "tool", Args: json.RawMessage(`{"x":1}`), WillContinue: true, PartialArgs: []PartialArg{{JSONPath: "$.x", NullValue: json.RawMessage(`null`)}}}},
		{FunctionResponse: &FunctionResponse{Name: "tool", Response: map[string]any{"output": "text"}, Parts: []FunctionResponsePart{{InlineData: &InlineData{MimeType: "image/png", Data: "abc"}}}}},
	}}
	want, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	got, err := requestjson.MarshalValue(contentValue(content))
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(want, &a)
	_ = json.Unmarshal(got, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("projection changed fields: %s != %s", want, got)
	}
	size, err := requestjson.Size(contentValue(content))
	if err != nil || size != len(want) {
		t.Fatalf("size mismatch %d != %d: %v", size, len(want), err)
	}
}
