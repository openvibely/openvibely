package anthropicclient

import "github.com/openvibely/openvibely/pkg/executionenv"

func ensureExecutionEnvironment(items []agenticMessage, dir string) []agenticMessage {
	return executionenv.Ensure(items, dir, func(m agenticMessage) (string, string) {
		text, _ := m.Content.(string)
		return m.Role, text
	}, func(text string) agenticMessage { return agenticMessage{Role: "user", Content: text} })
}

func ensureHistoryEnvironment(items []Message, dir string) []Message {
	return executionenv.Ensure(items, dir, func(m Message) (string, string) {
		return m.Role, m.Content
	}, func(text string) Message { return Message{Role: "user", Content: text} })
}
