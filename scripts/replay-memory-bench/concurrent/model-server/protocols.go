package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Translate the shared deterministic workload into each provider's real wire
// protocol. Only the external peer translates; the app uses its production client.
func newProtocolEmitter(protocol string, request []byte, send func(any) error) func(any) error {
	var body map[string]any
	_ = json.Unmarshal(request, &body)
	model, _ := body["model"].(string)
	started, tool, textOpen := false, false, false
	block := 0
	chunk := func(delta any, finish any, usage any) error {
		v := map[string]any{"id": "benchmark", "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			v["usage"] = usage
		}
		return send(v)
	}
	return func(value any) error {
		event := value.(map[string]any)
		if !started {
			started = true
			if protocol == "anthropic" {
				if err := send(map[string]any{"type": "message_start", "message": map[string]any{"id": "benchmark", "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": len(request) / 4, "output_tokens": 0}}}); err != nil {
					return err
				}
			} else if err := chunk(map[string]any{"role": "assistant"}, nil, nil); err != nil {
				return err
			}
		}
		switch event["type"] {
		case "response.output_text.delta":
			if protocol != "anthropic" {
				return chunk(map[string]any{"content": event["delta"]}, nil, nil)
			}
			if !textOpen {
				textOpen = true
				if err := send(map[string]any{"type": "content_block_start", "index": block, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
					return err
				}
			}
			return send(map[string]any{"type": "content_block_delta", "index": block, "delta": map[string]any{"type": "text_delta", "text": event["delta"]}})
		case "response.output_item.done":
			item := event["item"].(map[string]any)
			if item["type"] != "function_call" {
				return nil
			}
			tool = true
			if protocol != "anthropic" {
				return chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": item["arguments"]}}}}, nil, nil)
			}
			if textOpen {
				if err := send(map[string]any{"type": "content_block_stop", "index": block}); err != nil {
					return err
				}
				block++
				textOpen = false
			}
			if err := send(map[string]any{"type": "content_block_start", "index": block, "content_block": map[string]any{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": map[string]any{}}}); err != nil {
				return err
			}
			if err := send(map[string]any{"type": "content_block_delta", "index": block, "delta": map[string]any{"type": "input_json_delta", "partial_json": item["arguments"]}}); err != nil {
				return err
			}
			return send(map[string]any{"type": "content_block_stop", "index": block})
		case "response.completed":
			usage := event["response"].(map[string]any)["usage"].(map[string]int)
			if protocol != "anthropic" {
				reason := "stop"
				if tool {
					reason = "tool_calls"
				}
				return chunk(map[string]any{}, reason, map[string]int{"prompt_tokens": usage["input_tokens"], "completion_tokens": usage["output_tokens"], "total_tokens": usage["input_tokens"] + usage["output_tokens"]})
			}
			if textOpen {
				if err := send(map[string]any{"type": "content_block_stop", "index": block}); err != nil {
					return err
				}
			}
			reason := "end_turn"
			if tool {
				reason = "tool_use"
			}
			if err := send(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": usage}); err != nil {
				return err
			}
			return send(map[string]any{"type": "message_stop"})
		case "error":
			return fmt.Errorf("fixture error: %v", event)
		}
		return nil
	}
}

// Inspect only actual tool-result messages, not marker-shaped assistant text.
func hasToolResult(protocol string, items []any, marker string) bool {
	for _, value := range items {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if protocol == "openai" && item["type"] == "function_call_output" {
			raw, _ := json.Marshal(item["output"])
			if strings.Contains(string(raw), marker) {
				return true
			}
		}
		if protocol == "openai-compatible" && item["role"] == "tool" {
			raw, _ := json.Marshal(item["content"])
			if strings.Contains(string(raw), marker) {
				return true
			}
		}
		if protocol == "anthropic" && item["role"] == "user" {
			blocks, _ := item["content"].([]any)
			for _, value := range blocks {
				block, ok := value.(map[string]any)
				if !ok || block["type"] != "tool_result" {
					continue
				}
				raw, _ := json.Marshal(block["content"])
				if strings.Contains(string(raw), marker) {
					return true
				}
			}
		}
	}
	return false
}
