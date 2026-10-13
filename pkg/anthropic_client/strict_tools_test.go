package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

func TestTaskLookupValidationBeforeExecution(t *testing.T) {
	def := chatcontrol.Get("list_tasks")
	called := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: def.Name, Schema: def.Schema, StrictPriority: def.StrictPriority}}, ToolExecutor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, error) {
		called++
		if string(input) != `{"category":"backlog"}` {
			t.Errorf("unexpected filters %s", input)
		}
		return "ok", false, nil
	}}
	if out, bad := runAnthropicToolUse(context.Background(), opts, "list_tasks", json.RawMessage(`{"category":"backlog","status":null,"merge_status":null}`)); bad {
		t.Fatal(out)
	}
	if _, bad := runAnthropicToolUse(context.Background(), opts, "list_tasks", json.RawMessage(`{"status":"invalid"}`)); !bad {
		t.Fatal("invalid call accepted")
	}
	if called != 1 {
		t.Fatal("invalid call executed")
	}
}

func TestProviderWireStrictSchemas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		tools := body["tools"].([]any)
		tool := tools[0].(map[string]any)
		if tool["strict"] != false {
			t.Errorf("missing strict flag: %v", tool)
		}
		schema := tool["input_schema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if schema["additionalProperties"] != false || len(dRequired(schema)) != 0 {
			t.Error("not a strict schema")
		}
		if props["status"].(map[string]any)["type"] != "string" {
			t.Error("optional filter rewritten")
		}
		if _, ok := props["limit"].(map[string]any)["minimum"]; !ok {
			t.Error("original constraint removed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = srv.URL
	defer func() { AnthropicAPIHost = old }()
	d := chatcontrol.Get("list_tasks")
	_, err := NewWithAPIKey("test").SendAgentic(context.Background(), "backlog", &AgenticOptions{Model: "claude-opus-4-6", MaxTokens: 1024, SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: d.Name, Schema: d.Schema, StrictPriority: d.StrictPriority}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenRootToolStaysNonStrict(t *testing.T) {
	raw := json.RawMessage(`{"type":"object"}`)
	got := prepareStrictTools([]ToolDefinition{{Name: "custom", InputSchema: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.InputSchema) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestEmptyOpenRootToolStaysNonStrict(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{}}`)
	got := prepareStrictTools([]ToolDefinition{{Name: "custom", InputSchema: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.InputSchema) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestDeclaredOpenToolPreservesArguments(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`)
	got := prepareStrictTools([]ToolDefinition{{Name: "custom", InputSchema: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.InputSchema) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
	input := json.RawMessage(`{"label":"example","custom":42}`)
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{got}}
	normalized, err := normalizeToolArguments(opts, "custom", input)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal(normalized, &values); err != nil {
		t.Fatal(err)
	}
	if values["custom"] != float64(42) {
		t.Fatalf("lost arbitrary argument: %s", normalized)
	}
}

func TestUnsupportedExternalSchemaReachesExecutor(t *testing.T) {
	input := json.RawMessage(`{"code":"alice","optional":null}`)
	calls := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: "external", InputSchema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^(?!admin$)[a-z]+$"},"optional":{"type":"string"}}}`)}}, ToolExecutor: func(_ context.Context, _ string, got json.RawMessage) (string, bool, error) {
		calls++
		if string(got) != string(input) {
			t.Errorf("changed external arguments: %s", got)
		}
		return "executed", false, nil
	}}
	out, bad := runAnthropicToolUse(context.Background(), opts, "external", input)
	if bad || out != "executed" || calls != 1 {
		t.Fatalf("executor calls=%d result=%q error=%v", calls, out, bad)
	}
}

func TestExternalReferenceNullReachesExecutor(t *testing.T) {
	input := json.RawMessage(`{"clear":null}`)
	calls := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: "external", InputSchema: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","definitions":{"nullable":{"type":["string","null"]}},"properties":{"clear":{"$ref":"#/definitions/nullable","type":"string"}}}`)}}, ToolExecutor: func(_ context.Context, _ string, got json.RawMessage) (string, bool, error) {
		calls++
		if string(got) != string(input) {
			t.Errorf("changed external arguments: %s", got)
		}
		return "executed", false, nil
	}}
	out, bad := runAnthropicToolUse(context.Background(), opts, "external", input)
	if bad || out != "executed" || calls != 1 {
		t.Fatalf("executor calls=%d result=%q error=%v", calls, out, bad)
	}
}

func TestStrictDisabledReachesProviderRequest(t *testing.T) {
	var defs []ToolDefinition
	for _, d := range chatcontrol.Registry() {
		defs = append(defs, ToolDefinition{Name: d.Name, Schema: d.Schema, StrictPriority: d.StrictPriority})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name   string `json:"name"`
				Strict *bool  `json:"strict"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		selected := map[string]bool{}
		for _, tool := range body.Tools {
			if tool.Strict == nil || *tool.Strict {
				t.Errorf("%s must explicitly disable strict", tool.Name)
			}
			selected[tool.Name] = tool.Strict != nil && *tool.Strict
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = srv.URL
	defer func() { AnthropicAPIHost = old }()
	_, err := NewWithAPIKey("test").SendAgentic(context.Background(), "test", &AgenticOptions{Model: "claude-opus-4-6", MaxTokens: 1024, ExtraTools: defs})
	if err != nil {
		t.Fatal(err)
	}
}

func dRequired(schema map[string]any) []any { v, _ := schema["required"].([]any); return v }

func TestStrictDisabledPreservesAllSchemas(t *testing.T) {
	defs := DefaultTools()
	for _, d := range chatcontrol.Registry() {
		defs = append(defs, ToolDefinition{Name: d.Name, Schema: d.Schema, Strict: toolschema.Pointer(true)})
	}
	for i, got := range prepareStrictTools(defs) {
		if got.Strict == nil || *got.Strict {
			t.Fatalf("strict enabled: %s", got.Name)
		}
		if string(got.ParameterSchema()) != string(defs[i].ParameterSchema()) {
			t.Fatalf("schema changed: %s", got.Name)
		}
	}
}
