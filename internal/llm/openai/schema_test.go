package openai

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

func TestRuntimeSchemaAndPolicyPropagation(t *testing.T) {
	d := chatcontrol.Get("list_tasks")
	disabled := false
	raw := json.RawMessage(`{"type":"object","properties":{"metadata":{"type":"object"}}}`)
	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: d.Name, Schema: d.Schema, Strict: &disabled, StrictPriority: d.StrictPriority},
		{Name: "external", Parameters: raw},
	}}
	tools := runtimeOpenAITools(rt, false)
	if tools[0].Schema != d.Schema || tools[0].Strict != &disabled || tools[0].StrictPriority != d.StrictPriority {
		t.Fatal("adapter lost typed schema or policy")
	}
	if tools[1].Schema != nil || string(tools[1].ParameterSchema()) != string(raw) {
		t.Fatal("adapter changed external schema")
	}
	body, err := json.Marshal(tools)
	if err != nil || len(body) == 0 {
		t.Fatalf("marshal definitions: %s %v", body, err)
	}
}
