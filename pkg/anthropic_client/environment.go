package anthropicclient

import "github.com/openvibely/openvibely/pkg/executionenv"

func ensureExecutionEnvironment(items []agenticMessage, dir string) []agenticMessage {
	return executionenv.Ensure(items, dir, func(m agenticMessage) (string, string) {
		text, _ := m.Content.(string)
		return m.Role, text
	}, func(text string) agenticMessage { return agenticMessage{Role: "user", Content: text} })
}
