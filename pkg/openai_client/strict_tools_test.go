package openaiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/pkg/toolschema"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
)

func TestStrictTaskLookupAndValidation(t *testing.T) {
	def := chatcontrol.Get("list_tasks")
	called := 0
	opts := &AgenticOptions{SkipDefaultTools: true, ExtraTools: []ToolDefinition{{Type: "function", Name: def.Name, Parameters: def.Parameters}}, ToolExecutor: func(_ context.Context, _ string, input json.RawMessage) (string, bool, error) {
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
	for _, completions := range []bool{false, true} {
		t.Run(fmt.Sprint(completions), func(t *testing.T) {
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
				if tool["strict"] != true {
					t.Errorf("missing strict flag: %v", tool)
				}
				schema := tool["parameters"].(map[string]any)
				props := schema["properties"].(map[string]any)
				if schema["additionalProperties"] != false || len(schema["required"].([]any)) != len(props) {
					t.Error("not a strict schema")
				}
				if _, ok := props["status"].(map[string]any)["type"].([]any); !ok {
					t.Error("optional filter not nullable")
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
			defs := []ToolDefinition{{Type: "function", Name: d.Name, Parameters: d.Parameters}}
			client := newHTTPTestAPIKeyClient("test")
			var err error
			if completions {
				_, err = client.SendCompletions(context.Background(), "backlog", &CompletionsOptions{Model: "test", StrictTools: true, SkipDefaultTools: true, ExtraTools: defs})
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
		_, err := toolschema.NormalizeValidate(def.Parameters, json.RawMessage(`{}`))
		if err != nil && !strings.Contains(err.Error(), "invalid tool arguments:") {
			t.Errorf("%s: %v", name, err)
		}
		if wire, _, err := toolschema.Strict(def.Parameters, false); err == nil {
			strictCount++
			_, err = toolschema.NormalizeValidate(wire, json.RawMessage(`{}`))
			if err != nil && !strings.Contains(err.Error(), "invalid tool arguments:") {
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
	if got.Strict == nil || *got.Strict || string(got.Parameters) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestEmptyOpenRootToolStaysNonStrict(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{}}`)
	got := prepareStrictTools([]ToolDefinition{{Type: "function", Name: "custom", Parameters: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.Parameters) != string(raw) {
		t.Fatalf("open schema changed: %+v", got)
	}
}

func TestDeclaredOpenToolPreservesArguments(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}}}`)
	got := prepareStrictTools([]ToolDefinition{{Type: "function", Name: "custom", Parameters: raw}})[0]
	if got.Strict == nil || *got.Strict || string(got.Parameters) != string(raw) {
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
