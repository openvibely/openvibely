package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRuntimeToolsContextRoundTrip(t *testing.T) {
	rt := &RuntimeTools{
		Definitions: []RuntimeToolDefinition{
			{
				Name:        "create_task",
				Description: "Create a task",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	ctx := WithRuntimeTools(context.Background(), rt)
	got := RuntimeToolsFromContext(ctx)
	if got == nil {
		t.Fatalf("expected runtime tools in context")
	}
	if !got.HasDefinition("create_task") {
		t.Fatalf("expected create_task definition")
	}
}

func TestRuntimeToolsContextNilSafe(t *testing.T) {
	if got := RuntimeToolsFromContext(context.TODO()); got != nil {
		t.Fatalf("expected nil runtime tools for context without runtime tools")
	}
	ctx := WithRuntimeTools(context.TODO(), nil)
	if ctx == nil {
		t.Fatalf("expected non-nil context")
	}
	if got := RuntimeToolsFromContext(ctx); got != nil {
		t.Fatalf("expected nil runtime tools when none set")
	}
}

func TestWithoutRuntimeToolsMasksInheritedToolsAndPreservesCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	parent = WithRuntimeTools(parent, &RuntimeTools{Definitions: []RuntimeToolDefinition{{Name: "send_message"}}})

	masked := WithoutRuntimeTools(parent)
	if got := RuntimeToolsFromContext(masked); got != nil {
		t.Fatalf("expected runtime tools to be masked, got %#v", got)
	}

	cancel()
	select {
	case <-masked.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("masked context did not preserve parent cancellation")
	}
}

func TestCompositeRuntimeToolsDeduplicatesDefinitions(t *testing.T) {
	base := &RuntimeTools{Definitions: []RuntimeToolDefinition{
		{Name: "memory_view", Description: "selected memory", Parameters: json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string"}}}`), Access: RuntimeToolAccessRead},
	}}
	actions := &RuntimeTools{Definitions: []RuntimeToolDefinition{
		{Name: "memory_view", Description: "placeholder memory action", Parameters: json.RawMessage(`{"type":"object"}`), Access: RuntimeToolAccessRead},
		{Name: "list_capabilities", Description: "list actions", Parameters: json.RawMessage(`{"type":"object"}`), Access: RuntimeToolAccessRead},
	}}

	got := CompositeRuntimeTools(base, actions)
	if got == nil {
		t.Fatalf("expected composite runtime tools")
	}
	if len(got.Definitions) != 2 {
		t.Fatalf("expected duplicate memory_view definition to be collapsed, got %#v", got.Definitions)
	}
	if got.Definitions[0].Description != "selected memory" {
		t.Fatalf("expected first memory_view definition to win, got %#v", got.Definitions[0])
	}
	if !got.HasDefinition("memory_view") || !got.HasDefinition("list_capabilities") {
		t.Fatalf("expected memory_view and list_capabilities definitions, got %#v", got.Definitions)
	}
}

func TestCompositeRuntimeToolsExecutorAndFilterChain(t *testing.T) {
	wantErr := errors.New("tool failed")
	first := &RuntimeTools{
		Definitions:      []RuntimeToolDefinition{{Name: "first"}},
		SkipDefaultTools: true,
		Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
			return "", false, false, nil
		},
		Filter: func(name string) (bool, bool) {
			if name == "first" {
				return true, true
			}
			return false, false
		},
	}
	second := &RuntimeTools{
		Definitions: []RuntimeToolDefinition{{Name: "second"}},
		Executor: func(_ context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
			if name == "explode" {
				return "", false, false, wantErr
			}
			return "ok", true, false, nil
		},
		Filter: func(name string) (bool, bool) {
			if name == "second" {
				return false, true
			}
			return false, false
		},
	}
	got := CompositeRuntimeTools(nil, first, second)
	if got == nil || !got.SkipDefaultTools {
		t.Fatalf("expected composite with SkipDefaultTools, got %#v", got)
	}
	out, handled, isErr, err := got.Executor(context.Background(), "second", json.RawMessage(`{}`))
	if out != "ok" || !handled || isErr || err != nil {
		t.Fatalf("executor chain = out=%q handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	if _, _, _, err := got.Executor(context.Background(), "explode", nil); !errors.Is(err, wantErr) {
		t.Fatalf("expected executor error, got %v", err)
	}
	if allow, handled := got.Filter("first"); !allow || !handled {
		t.Fatalf("expected first filter to allow, got allow=%v handled=%v", allow, handled)
	}
	if allow, handled := got.Filter("second"); allow || !handled {
		t.Fatalf("expected second filter to deny, got allow=%v handled=%v", allow, handled)
	}
	if allow, handled := got.Filter("unknown"); allow || handled {
		t.Fatalf("expected unknown filter to fall through, got allow=%v handled=%v", allow, handled)
	}
	if CompositeRuntimeTools(nil, nil) != nil {
		t.Fatal("all nil composite should be nil")
	}
	if CompositeRuntimeTools(first) != first {
		t.Fatal("single runtime tool should be returned unchanged")
	}
}

func TestRuntimeToolsHelpersCoverNilAndTrimmedNames(t *testing.T) {
	if (*RuntimeTools)(nil).HasDefinition("anything") {
		t.Fatal("nil RuntimeTools should not have definitions")
	}
	rt := &RuntimeTools{Definitions: []RuntimeToolDefinition{{Name: " List_Models "}}}
	if !rt.HasDefinition(" list_models ") || rt.HasDefinition(" ") {
		t.Fatalf("HasDefinition did not normalize names")
	}
	if RuntimeToolsFromContext(nil) != nil {
		t.Fatal("nil context should have no runtime tools")
	}
	if WithRuntimeTools(nil, nil) == nil || WithoutRuntimeTools(nil) == nil {
		t.Fatal("nil-safe context helpers should return contexts")
	}
}

func TestRuntimeToolsNormalizeToolInput(t *testing.T) {
	rt := &RuntimeTools{Definitions: []RuntimeToolDefinition{{
		Name:       "list_alerts",
		Parameters: json.RawMessage(`{"type":"object","properties":{"processing_state":{"type":["null","string"]},"read":{"type":["null","boolean"]},"limit":{"type":["null","integer"]},"required_value":{"type":["null","string"]}},"required":["required_value"]}`),
	}}}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "omitted stays omitted", input: `{"limit":50}`, want: `{"limit":50}`},
		{name: "optional nulls become absent", input: `{"processing_state":null,"read":null,"limit":null}`, want: `{}`},
		{name: "case insensitive property", input: `{"READ":null}`, want: `{}`},
		{name: "explicit values stay present", input: `{"processing_state":"not_applicable","read":false,"limit":0}`, want: `{"processing_state":"not_applicable","read":false,"limit":0}`},
		{name: "required null stays present", input: `{"required_value":null}`, want: `{"required_value":null}`},
		{name: "unknown null stays present", input: `{"unknown":null}`, want: `{"unknown":null}`},
		{name: "malformed stays unchanged", input: `{`, want: `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rt.NormalizeToolInput("LIST_ALERTS", json.RawMessage(tt.input))
			if string(got) != tt.want {
				t.Fatalf("NormalizeToolInput() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRuntimeToolsDefinitionNames(t *testing.T) {
	if got := (*RuntimeTools)(nil).DefinitionNames(); got != nil {
		t.Fatalf("nil RuntimeTools DefinitionNames = %#v, want nil", got)
	}

	rt := &RuntimeTools{Definitions: []RuntimeToolDefinition{
		{Name: " create_task "},
		{Name: ""},
		{Name: " \t "},
		{Name: "github_create_issue"},
		{Name: " send_message\n"},
	}}
	want := []string{"create_task", "github_create_issue", "send_message"}
	if got := rt.DefinitionNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefinitionNames() = %#v, want %#v", got, want)
	}
}
