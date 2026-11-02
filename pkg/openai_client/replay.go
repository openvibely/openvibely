package openaiclient

// EstimateResponsesInputTokens uses the same image-aware estimator as requests.
func EstimateResponsesInputTokens(items []any) int {
	return estimateInputItemsTokens(items)
}

// repairInterruptedToolCalls follows Codex's history normalization: an
// interrupted call receives an "aborted" result, never another tool execution.
// Run only on restored history, not while a live call is awaiting its result.
func repairInterruptedToolCalls(items []any) []any {
	outputs := make(map[string]bool)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := stringFromAny(item["type"])
		if kind == "function_call_output" || kind == "custom_tool_call_output" {
			outputs[kind+":"+stringFromAny(item["call_id"])] = true
		}
	}
	result := make([]any, 0, len(items))
	for _, raw := range items {
		result = append(result, raw)
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind := stringFromAny(item["type"])
		id := stringFromAny(item["call_id"])
		if (kind == "function_call" || kind == "custom_tool_call") && id != "" && !outputs[kind+"_output:"+id] {
			result = append(result, map[string]any{"type": kind + "_output", "call_id": id, "output": "aborted"})
		}
	}
	return result
}
