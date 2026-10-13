package openai_compatible

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

func TestRuntimeSchemaPropagation(t *testing.T) {
	d := chatcontrol.Get("list_tasks")
	raw := json.RawMessage(`{"type":"object","properties":{"metadata":{"type":"object"}}}`)
	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: d.Name, Schema: d.Schema},
		{Name: "external", Parameters: raw},
	}}
	tools := runtimeTools(llmcontracts.WithRuntimeTools(context.Background(), rt))
	if tools[0].Schema != d.Schema {
		t.Fatal("adapter lost typed schema")
	}
	if tools[1].Schema != nil || string(tools[1].ParameterSchema()) != string(raw) {
		t.Fatal("adapter changed external schema")
	}
	body, err := json.Marshal(tools)
	if err != nil || len(body) == 0 {
		t.Fatalf("marshal definitions: %s %v", body, err)
	}
}
