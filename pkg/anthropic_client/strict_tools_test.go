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

func TestStrictBudgetAndTaskLookup(t *testing.T) {
	defs := DefaultTools()
	for _, name := range chatcontrol.AllActionNames() {
		d := chatcontrol.Get(name)
		defs = append(defs, ToolDefinition{Name: name, Schema: d.Schema, StrictPriority: d.StrictPriority})
	}
	got := prepareStrictTools(defs)
	count, optional := 0, 0
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
					if props, ok := n["properties"].(map[string]any); ok {
						required, _ := n["required"].([]any)
						optional += len(props) - len(required)
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
	if count == 0 || count > maxStrictTools || optional > maxStrictOptional {
		t.Fatalf("budget: tools=%d optional=%d", count, optional)
	}
	if len(got) != len(defs) {
		t.Fatal("lost tools")
	}
	// Exercise the tool-count limit independently of optional-field capacity.
	var empty []ToolDefinition
	for i := 0; i < 25; i++ {
		empty = append(empty, ToolDefinition{Name: fmt.Sprint(i), InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)})
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
		if tool["strict"] != true {
			t.Errorf("missing strict flag: %v", tool)
		}
		schema := tool["input_schema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if schema["additionalProperties"] != false || len(schema["required"].([]any)) != 0 {
			t.Error("not a strict schema")
		}
		if props["status"].(map[string]any)["type"] != "string" {
			t.Error("optional filter rewritten")
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

func TestDefaultToolsExplicitlyClosedAndStrict(t *testing.T) {
	for _, def := range prepareStrictTools(DefaultTools()) {
		if def.Strict == nil || !*def.Strict {
			t.Fatalf("local tool %s lost strict support", def.Name)
		}
	}
}

func TestOptionalBudgetAndDefinitionPriority(t *testing.T) {
	schema := &toolschema.Schema{Type: "object", AdditionalProperties: toolschema.Pointer(false), Properties: map[string]*toolschema.Schema{}}
	for i := 0; i < 12; i++ {
		schema.Properties[fmt.Sprint(i)] = &toolschema.Schema{Type: "string"}
	}
	defs := []ToolDefinition{
		{Name: "first", Schema: schema},
		{Name: "second", Schema: schema},
		{Name: "important", Schema: schema, StrictPriority: 10},
		{Name: "no_arguments", Schema: &toolschema.Schema{Type: "object", AdditionalProperties: toolschema.Pointer(false)}},
	}
	got := prepareStrictTools(defs)
	if !*got[0].Strict || *got[1].Strict || !*got[2].Strict || !*got[3].Strict {
		t.Fatalf("optional budget/priority not respected: %+v", got)
	}
	for i, d := range got {
		if d.Name != defs[i].Name || defs[i].Strict != nil {
			t.Fatal("source or request order mutated")
		}
	}
	// The tool excluded from strict mode must still marshal its complete typed schema.
	raw, err := json.Marshal(got[1])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		InputSchema struct {
			Properties map[string]any `json:"properties"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.InputSchema.Properties) != 12 {
		t.Fatalf("fallback lost schema: %s", raw)
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

func TestStrictSelectionUsesImpactInsteadOfRegistrationOrder(t *testing.T) {
	defs := DefaultTools()
	defs = append(defs, ToolDefinition{Name: "delete_file", Schema: &toolschema.Schema{Type: "object", AdditionalProperties: toolschema.Pointer(false), Properties: map[string]*toolschema.Schema{"file_path": {Type: "string"}}, Required: []string{"file_path"}}})
	for _, d := range chatcontrol.Registry() {
		if _, ok := strictToolPriorities[d.Name]; !ok {
			t.Fatalf("classify new registry tool %s by impact", d.Name)
		}
		defs = append(defs, ToolDefinition{Name: d.Name, Schema: d.Schema})
	}
	for _, d := range DefaultTools() {
		if _, ok := strictToolPriorities[d.Name]; !ok {
			t.Fatalf("unclassified local tool %s", d.Name)
		}
	}
	selected := func(defs []ToolDefinition) map[string]bool {
		result := map[string]bool{}
		for i, d := range prepareStrictTools(defs) {
			if d.Name != defs[i].Name {
				t.Fatal("changed advertised order")
			}
			result[d.Name] = *d.Strict
		}
		return result
	}
	want := selected(defs)
	for i, j := 0, len(defs)-1; i < j; i, j = i+1, j-1 {
		defs[i], defs[j] = defs[j], defs[i]
	}
	got := selected(defs)
	for name, enabled := range want {
		if got[name] != enabled {
			t.Fatalf("registration order changed strict for %s", name)
		}
	}
	for _, name := range []string{"list_tasks", "github_replace_pull_request_branch", "delete_file", "delete_automation", "github_close_issue"} {
		if !want[name] {
			t.Errorf("high-impact tool %s lost strict capacity", name)
		}
	}
	if want["list_capabilities"] {
		t.Fatal("informational tool consumed scarce capacity")
	}
}

func TestStrictSelectionSkipsIneligibleHighPriorityAndHonorsOverrides(t *testing.T) {
	schema := &toolschema.Schema{Type: "object", AdditionalProperties: toolschema.Pointer(false)}
	no := false
	defs := []ToolDefinition{
		{Name: "list_tasks", Schema: schema, Strict: &no},
		{Name: "merge_task", Schema: &toolschema.Schema{Type: "object"}},
		{Name: "external_override", Schema: schema, StrictPriority: 1000},
	}
	for i := 0; i < 21; i++ {
		defs = append(defs, ToolDefinition{Name: fmt.Sprintf("external_%02d", i), Schema: schema})
	}
	got := prepareStrictTools(defs)
	if *got[0].Strict || *got[1].Strict || !*got[2].Strict {
		t.Fatal("disabled, unsupported, or override policy lost")
	}
	if !*got[3].Strict || *got[len(got)-1].Strict {
		t.Fatal("fallback ranking or count budget lost")
	}
}

func TestStrictImpactPolicyReachesProviderRequest(t *testing.T) {
	var defs []ToolDefinition
	for _, d := range chatcontrol.Registry() {
		defs = append(defs, ToolDefinition{Name: d.Name, Schema: d.Schema, StrictPriority: d.StrictPriority})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name   string `json:"name"`
				Strict bool   `json:"strict"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		selected := map[string]bool{}
		for _, tool := range body.Tools {
			selected[tool.Name] = tool.Strict
		}
		for _, name := range []string{"list_tasks", "github_replace_pull_request_branch", "github_close_issue", "delete_automation"} {
			if !selected[name] {
				t.Errorf("%s not strict on wire", name)
			}
		}
		if selected["list_capabilities"] {
			t.Error("informational tool displaced high-impact tools")
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
