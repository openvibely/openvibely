package openaiclient

// Complete restored tool rounds before new user/environment messages are added.
// The tool may already have executed, so recovery must not run it again.
func repairInterruptedCompletionsToolCalls(history []CompletionsHistoryMessage) []CompletionsHistoryMessage {
	if history == nil {
		return nil
	}
	out := make([]CompletionsHistoryMessage, 0, len(history))
	for i := 0; i < len(history); i++ {
		message := history[i]
		out = append(out, message)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		seen := map[string]bool{}
		for i+1 < len(history) && history[i+1].Role == "tool" {
			i++
			out = append(out, history[i])
			seen[history[i].ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				continue
			}
			seen[call.ID] = true
			out = append(out, CompletionsHistoryMessage{Role: "tool", ToolCallID: call.ID, Content: "Execution interrupted; outcome unknown."})
		}
	}
	return out
}
