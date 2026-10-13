package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

func TestStrictTaskLookupAndValidation(t *testing.T) {
	def := chatcontrol.Get("list_tasks")
	called := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "function", Name: def.Name, Schema: def.Schema}}, ToolExecutor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, error) {
		called++
		if string(input) != `{"category":"backlog"}` {
			t.Errorf("unexpected filters: %s", input)
		}
		return "ok", false, nil
	}}
	wire := agenticToolDefinitions(opts, false)
	if wire[0].Strict == nil || !*wire[0].Strict {
		t.Fatal("list_tasks not strict")
	}
	for _, input := range []string{`{"category":"backlog","status":null,"merge_status":null,"limit":null}`, `{"category":"backlog"}`} {
		if out, bad, _ := runOpenAIToolTask(context.Background(), opts, "list_tasks", json.RawMessage(input)); bad {
			t.Fatal(out)
		}
	}
	for _, input := range []string{`{"status":"invalid"}`, `{"limit":0}`, `{"limit":"lots"}`} {
		if _, bad, _ := runOpenAIToolTask(context.Background(), opts, "list_tasks", json.RawMessage(input)); !bad {
			t.Fatalf("accepted %s", input)
		}
	}
	if called != 2 {
		t.Fatalf("invalid arguments executed: %d", called)
	}
}

func TestStrictFallbackAndNamespace(t *testing.T) {
	defs := []ToolDefinition{{Type: "function", Name: "open", Parameters: json.RawMessage(`{"type":"object","properties":{"data":{"type":"object"}}}`)}, {Type: "namespace", Name: "files", Tools: DefaultTools()}}
	got := prepareStrictTools(defs)
	if got[0].Strict == nil || *got[0].Strict {
		t.Fatal("unsafe schema strict")
	}
	if got[1].Tools[0].Strict == nil || !*got[1].Tools[0].Strict {
		t.Fatal("namespace not converted")
	}
	if defs[1].Tools[0].Strict != nil {
		t.Fatal("mutated original")
	}
}

func TestProviderWireStrictSchemas(t *testing.T) {
	for _, mode := range []struct{ completions, strict bool }{{false, true}, {true, true}, {true, false}} {
		completions := mode.completions
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				tools := body["tools"].([]any)
				tool := tools[0].(map[string]any)
				if completions {
					tool = tool["function"].(map[string]any)
				}
				if mode.strict && tool["strict"] != true {
					t.Errorf("missing strict flag: %v", tool)
				}
				if !mode.strict {
					if _, exists := tool["strict"]; exists {
						t.Error("strict flag sent to non-strict endpoint")
					}
				}
				schema := tool["parameters"].(map[string]any)
				props := schema["properties"].(map[string]any)
				required, _ := schema["required"].([]any)
				if schema["additionalProperties"] != false || (mode.strict && len(required) != len(props)) || (!mode.strict && len(required) != 0) {
					t.Error("not a strict schema")
				}
				if _, ok := props["status"].(map[string]any)["type"].([]any); ok != mode.strict {
					t.Error("incorrect optional filter nullability")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if completions {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer srv.Close()
			old := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL + "/"
			defer func() { OpenAIAPIBaseURL = old }()
			d := chatcontrol.Get("list_tasks")
			defs := []ToolDefinition{{Type: "function", Name: d.Name, Schema: d.Schema}}
			client := newHTTPTestAPIKeyClient("test")
			var err error
			if completions {
				_, err = client.SendCompletions(context.Background(), "backlog", &CompletionsOptions{Model: "test", StrictTools: mode.strict, SkipDefaultTools: true, ExtraTools: defs})
			} else {
				_, err = client.SendAgentic(context.Background(), "backlog", &AgenticOptions{Model: "test", SkipDefaultTools: true, ExtraTools: defs})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegistrySchemasRemainValid(t *testing.T) {
	strictCount := 0
	for _, name := range chatcontrol.AllActionNames() {
		def := chatcontrol.Get(name)
		_, err := toolschema.ValidateArguments(def.Schema, nil, json.RawMessage(`{}`))
		if err != nil && !strings.Contains(err.Error(), "invalid tool arguments:") {
			t.Errorf("%s: %v", name, err)
		}
		if wire, err := toolschema.MarshalOpenAI(def.Schema, nil); err == nil {
			strictCount++
			_, err = jsonschema.CompileString("https://tools.invalid/test", string(wire))
			if err != nil {
				t.Errorf("converted %s: %v", name, err)
			}
		} else {
			t.Logf("non-strict %s: %v", name, err)
		}
	}
	t.Logf("%d/%d registry tools support strict conversion", strictCount, len(chatcontrol.AllActionNames()))
}

func TestNamespacedArgumentsValidatedBeforeExecution(t *testing.T) {
	calls := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "namespace", Name: "files", Tools: DefaultTools()}}, ToolExecutor: func(_ context.Context, name string, input json.RawMessage) (string, bool, error) {
		calls++
		if name != "files.read_file" || string(input) != `{"file_path":"example.txt"}` {
			t.Errorf("unexpected execution: %s %s", name, input)
		}
		return "ok", false, nil
	}}
	if out, bad, _ := runOpenAIToolTask(context.Background(), opts, "files.read_file", json.RawMessage(`{"file_path":"example.txt","offset":null,"limit":null}`)); bad {
		t.Fatal(out)
	}
	for _, input := range []string{`{}`, `{"file_path":3}`, `{"file_path":"example.txt","limit":"bad"}`} {
		if _, bad, _ := runOpenAIToolTask(context.Background(), opts, "files.read_file", json.RawMessage(input)); !bad {
			t.Fatalf("accepted %s", input)
		}
	}
	if calls != 1 {
		t.Fatalf("executor called %d times", calls)
	}
}

func TestOpenRootToolStaysNonStrict(t *testing.T) {
	raw := json.RawMessage(`{"type":"object"}`)
	got := prepareStrictTools([]ToolDefinition{{Type: "function", Name: "custom", Parameters: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.ParameterSchema()) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestEmptyOpenRootToolStaysNonStrict(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{}}`)
	got := prepareStrictTools([]ToolDefinition{{Type: "function", Name: "custom", Parameters: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.ParameterSchema()) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestDeclaredOpenToolPreservesArguments(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`)
	got := prepareStrictTools([]ToolDefinition{{Type: "function", Name: "custom", Parameters: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.ParameterSchema()) != string(raw) {
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
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "function", Name: "external", Parameters: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","pattern":"^(?!admin$)[a-z]+$"},"optional":{"type":"string"}}}`)}}, ToolExecutor: func(_ context.Context, _ string, got json.RawMessage) (string, bool, error) {
		calls++
		if string(got) != string(input) {
			t.Errorf("changed external arguments: %s", got)
		}
		return "executed", false, nil
	}}
	out, bad, _ := runOpenAIToolTask(context.Background(), opts, "external", input)
	if bad || out != "executed" || calls != 1 {
		t.Fatalf("executor calls=%d result=%q error=%v", calls, out, bad)
	}
}

func TestExternalReferenceNullReachesExecutor(t *testing.T) {
	input := json.RawMessage(`{"clear":null}`)
	calls := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "function", Name: "external", Parameters: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","definitions":{"nullable":{"type":["string","null"]}},"properties":{"clear":{"$ref":"#/definitions/nullable","type":"string"}}}`)}}, ToolExecutor: func(_ context.Context, _ string, got json.RawMessage) (string, bool, error) {
		calls++
		if string(got) != string(input) {
			t.Errorf("changed external arguments: %s", got)
		}
		return "executed", false, nil
	}}
	out, bad, _ := runOpenAIToolTask(context.Background(), opts, "external", input)
	if bad || out != "executed" || calls != 1 {
		t.Fatalf("executor calls=%d result=%q error=%v", calls, out, bad)
	}
}
