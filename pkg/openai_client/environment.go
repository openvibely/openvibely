package openaiclient

import "github.com/openvibely/openvibely/pkg/executionenv"

func ensureExecutionEnvironment(items []any, dir string) []any {
	return executionenv.Ensure(items, dir, func(item any) (string, string) {
		m, ok := item.(map[string]any)
		if !ok {
			return "", ""
		}
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		return role, text
	}, func(text string) any { return map[string]any{"type": "message", "role": "user", "content": text} })
}
func ensureCompletionsEnvironment(items []completionsMessage, dir string) []completionsMessage {
	return executionenv.Ensure(items, dir, func(m completionsMessage) (string, string) {
		text, _ := m.Content.(string)
		return m.Role, text
	}, func(text string) completionsMessage { return completionsMessage{Role: "user", Content: text} })
}
