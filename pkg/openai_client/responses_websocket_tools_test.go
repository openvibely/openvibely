package openaiclient

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuildResponsesLiteWebsocketPayload_TypedToolDefinitions(t *testing.T) {
	strict := true
	nestedStrict := false
	tools := []ToolDefinition{
		{
			Type:        "function",
			Name:        "read_file",
			Description: "Read a file.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":{"type":"string"},
					"options":{"type":"object","properties":{"depth":{"type":"integer","minimum":1}},"required":["depth"],"additionalProperties":false}
				},
				"required":["path"],"additionalProperties":false
			}`),
			Async:  true,
			Strict: &strict,
			Tools: []ToolDefinition{{
				Type: "function", Name: "nested_tool", Parameters: json.RawMessage(`{"type":"object","properties":{"enabled":{"type":"boolean"}}}`), Async: true, Strict: &nestedStrict,
			}},
		},
		{Type: " WeB_SeArCh ", Name: "hosted_search"},
		{
			Type:       "computer",
			Name:       "computer",
			Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"enum":["click","type"]}}}`),
			Strict:     &nestedStrict,
		},
		{Type: "IMAGE_Generation", Name: "hosted_image"},
	}
	before := append([]ToolDefinition(nil), tools...)
	before[0].Tools = append([]ToolDefinition(nil), tools[0].Tools...)

	request := buildResponsesLiteWebsocketPayload(map[string]any{
		"model":             "gpt-5.6-sol",
		"input":             []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect."}}}},
		"instructions":      "legacy instructions",
		"tools":             tools,
		"reasoning":         map[string]any{"effort": "high"},
		"max_output_tokens": 1200,
		"truncation":        "auto",
	}, "system prompt", "session-123")

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Responses Lite request: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode Responses Lite request: %v", err)
	}
	input, _ := decoded["input"].([]any)
	additionalTools, _ := input[0].(map[string]any)
	gotTools, _ := additionalTools["tools"].([]any)
	wantTools := []any{
		map[string]any{
			"type": "function", "name": "read_file", "description": "Read a file.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
					"options": map[string]any{
						"type":       "object",
						"properties": map[string]any{"depth": map[string]any{"type": "integer", "minimum": float64(1)}},
						"required":   []any{"depth"}, "additionalProperties": false,
					},
				},
				"required": []any{"path"}, "additionalProperties": false,
			},
			"strict": true,
			"tools": []any{map[string]any{
				"type": "function", "name": "nested_tool", "description": "",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}},
				"async":      true, "strict": false,
			}},
		},
		map[string]any{
			"type": "computer", "name": "computer", "description": "",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"enum": []any{"click", "type"}}}},
			"strict":     false,
		},
	}
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Fatalf("decoded ordered tools differ\n got: %#v\nwant: %#v", gotTools, wantTools)
	}
	if !reflect.DeepEqual(tools, before) {
		t.Fatalf("tool definitions were mutated\n got: %#v\nwant: %#v", tools, before)
	}
	if decoded["type"] != "response.create" || decoded["store"] != false || decoded["stream"] != true || decoded["tool_choice"] != "auto" || decoded["parallel_tool_calls"] != false {
		t.Fatalf("request fields changed: %#v", decoded)
	}
	if _, ok := decoded["instructions"]; ok {
		t.Fatalf("request retained instructions: %#v", decoded["instructions"])
	}
	if _, ok := decoded["tools"]; ok {
		t.Fatalf("request retained top-level tools")
	}
	if _, ok := decoded["max_output_tokens"]; ok {
		t.Fatalf("request retained max_output_tokens")
	}
	if _, ok := decoded["truncation"]; ok {
		t.Fatalf("request retained truncation")
	}
	reasoning, _ := decoded["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["context"] != "all_turns" {
		t.Fatalf("reasoning fields = %#v", reasoning)
	}
}

func TestResponsesLiteTools_GenericFallbackPreservesExtensions(t *testing.T) {
	tools := []any{
		map[string]any{
			"type": "function", "name": "custom",
			"async":            true,
			"x-provider-field": map[string]any{"nested": []any{"value", float64(3)}},
			"parameters":       map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		},
		map[string]any{"type": " WEB_SEARCH_PREVIEW ", "name": "hosted"},
		map[string]any{"type": "function", "name": "sync", "async": false, "strict": true},
		map[string]any{"type": "image_generation", "name": "image"},
	}

	got := responsesLiteTools(tools)
	if len(got) != 2 {
		t.Fatalf("filtered tools = %#v, want 2 retained tools", got)
	}
	first, _ := got[0].(map[string]any)
	if _, ok := first["async"]; ok {
		t.Fatalf("generic async metadata retained: %#v", first)
	}
	if first["x-provider-field"] == nil || first["parameters"] == nil {
		t.Fatalf("generic extension or nested schema lost: %#v", first)
	}
	second, _ := got[1].(map[string]any)
	if second["name"] != "sync" || second["strict"] != true {
		t.Fatalf("generic tool order or fields changed: %#v", got)
	}
	if _, ok := second["async"]; ok {
		t.Fatalf("false generic async metadata retained: %#v", second)
	}
}

func TestResponsesLiteTools_EmptyInputs(t *testing.T) {
	for name, tools := range map[string]any{"nil": nil, "typed": []ToolDefinition{}, "generic": []any{}} {
		t.Run(name, func(t *testing.T) {
			if got := responsesLiteTools(tools); len(got) != 0 {
				t.Fatalf("responsesLiteTools(%s) = %#v, want empty", name, got)
			}
		})
	}
}
