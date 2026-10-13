package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
)

func TestStrictBudgetAndTaskLookup(t *testing.T) {
	defs := DefaultTools()
	for _, name := range chatcontrol.AllActionNames() {
		d := chatcontrol.Get(name)
		defs = append(defs, ToolDefinition{Name: name, InputSchema: d.Parameters})
	}
	got := prepareStrictTools(defs)
	count, unions := 0, 0
	for i, def := range got {
		if def.Strict == nil {
			t.Fatal("missing policy")
		}
		if *def.Strict {
			count++
			var s any
			if err := json.Unmarshal(def.InputSchema, &s); err != nil {
				t.Fatal(err)
			}
			var walk func(any)
			walk = func(v any) {
				switch n := v.(type) {
				case map[string]any:
					if _, ok := n["type"].([]any); ok {
						unions++
					}
					for _, c := range n {
						walk(c)
					}
				case []any:
					for _, c := range n {
						walk(c)
					}
				}
			}
			walk(s)
		}
		if def.Name == "list_tasks" && !*def.Strict {
			t.Fatal("list_tasks not prioritized")
		}
		if defs[i].Strict != nil {
			t.Fatal("mutated original")
		}
	}
	if count == 0 || count > 20 || unions > 16 {
		t.Fatalf("budget: tools=%d unions=%d", count, unions)
	}
	if len(got) != len(defs) {
		t.Fatal("lost tools")
	}
	// Exercise the tool-count limit independently of union capacity.
	var empty []ToolDefinition
	for i := 0; i < 25; i++ {
		empty = append(empty, ToolDefinition{Name: fmt.Sprint(i), InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)})
	}
	count = 0
	for _, d := range prepareStrictTools(empty) {
		if *d.Strict {
			count++
		}
	}
	if count != 20 {
		t.Fatalf("count %d", count)
	}
}

func TestTaskLookupValidationBeforeExecution(t *testing.T) {
	def := chatcontrol.Get("list_tasks")
	called := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: def.Name, InputSchema: def.Parameters}}, ToolExecutor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, error) {
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
		if tool["strict"] != true {
			t.Errorf("missing strict flag: %v", tool)
		}
		schema := tool["input_schema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if schema["additionalProperties"] != false || len(schema["required"].([]any)) != len(props) {
			t.Error("not a strict schema")
		}
		if _, ok := props["status"].(map[string]any)["type"].([]any); !ok {
			t.Error("optional filter not nullable")
		}
		if _, ok := props["limit"].(map[string]any)["minimum"]; ok {
			t.Error("unsupported constraint sent")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":1}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	old := AnthropicAPIHost
	AnthropicAPIHost = srv.URL
	defer func() { AnthropicAPIHost = old }()
	d := chatcontrol.Get("list_tasks")
	_, err := NewWithAPIKey("test").SendAgentic(context.Background(), "backlog", &AgenticOptions{Model: "claude-opus-4-6", MaxTokens: 1024, SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Name: d.Name, InputSchema: d.Parameters}}})
	if err != nil {
		t.Fatal(err)
	}
}
