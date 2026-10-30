package main

import (
	"encoding/json"
	"testing"
)

// A marker in ordinary conversation text must not let a run pass without the
// model actually receiving a tool result in its provider's native schema.
func TestToolResultGateRequiresNativeResult(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, messages string
		want                     bool
	}{
		{"responses result", "openai", `[{"type":"function_call_output","output":"TOOL_5_END"}]`, true},
		{"responses assistant", "openai", `[{"role":"assistant","content":"TOOL_5_END"}]`, false},
		{"responses old result", "openai", `[{"type":"function_call_output","output":"TOOL_4_END"}]`, false},
		{"compatible result", "openai-compatible", `[{"role":"tool","content":"TOOL_5_END"}]`, true},
		{"compatible assistant", "openai-compatible", `[{"role":"assistant","content":"TOOL_5_END"}]`, false},
		{"compatible user", "openai-compatible", `[{"role":"user","content":"TOOL_5_END"}]`, false},
		{"anthropic result", "anthropic", `[{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"TOOL_5_END"}]}]}]`, true},
		{"anthropic user text", "anthropic", `[{"role":"user","content":[{"type":"text","text":"TOOL_5_END"}]}]`, false},
		{"anthropic assistant", "anthropic", `[{"role":"assistant","content":[{"type":"tool_result","content":"TOOL_5_END"}]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var items []any
			if err := json.Unmarshal([]byte(tc.messages), &items); err != nil {
				t.Fatal(err)
			}
			if got := hasToolResult(tc.protocol, items, "TOOL_5_END"); got != tc.want {
				t.Fatalf("hasToolResult = %v, want %v", got, tc.want)
			}
		})
	}
}
