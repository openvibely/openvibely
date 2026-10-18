package anthropicclient

import (
	"context"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"strings"
)

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

// A native checkpoint may already contain the first environment restored from
// execution history. Preserve the checkpoint prefix without repeating it.
func appendHistoryMessage(items []agenticMessage, message Message) []agenticMessage {
	if message.Role == "user" && strings.HasPrefix(message.Content, executionenv.Marker+"\n") {
		for i := len(items) - 1; i >= 0; i-- {
			text, ok := items[i].Content.(string)
			if ok && items[i].Role == "user" && strings.HasPrefix(text, executionenv.Marker+"\n") {
				if text == message.Content {
					return items
				}
				break
			}
		}
	}
	if len(message.ContentBlocks) > 0 {
		return append(items, agenticMessage{Role: message.Role, Content: message.ContentBlocks})
	}
	return append(items, agenticMessage{Role: message.Role, Content: message.Content})
}

func ensureAttachmentContext(ctx context.Context, items []agenticMessage) []agenticMessage {
	items = managedcontext.Ensure(ctx, items, func(m agenticMessage) (string, string) { s, _ := m.Content.(string); return m.Role, s }, func(s string) agenticMessage { return agenticMessage{Role: "user", Content: s} })
	return attachmentcontext.Ensure(ctx, items, func(m agenticMessage) (string, string) { s, _ := m.Content.(string); return m.Role, s }, func(s string) agenticMessage { return agenticMessage{Role: "user", Content: s} })
}
