package openaiclient

import (
	"context"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/openvibely/openvibely/pkg/managedcontext"
)

func ensureExecutionEnvironment(items []any, dir string) []any {
	// Keep pending configuration updates trailing so the caller can replace
	// them before appending the next user request.
	end := len(items)
	for end > 0 {
		item, ok := items[end-1].(map[string]any)
		if !ok || item["type"] != "configuration_update" {
			break
		}
		end--
	}
	start := 0
	for i := end - 1; i >= 0; i-- {
		if item, ok := items[i].(map[string]any); ok && item["type"] == "compaction" {
			start = i + 1
			break
		}
	}
	withEnvironment := executionenv.Ensure(items[start:end], dir, func(item any) (string, string) {
		m, ok := item.(map[string]any)
		if !ok {
			return "", ""
		}
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		return role, text
	}, func(text string) any { return map[string]any{"type": "message", "role": "user", "content": text} })
	if len(withEnvironment) == end-start {
		return items
	}
	out := make([]any, 0, len(items)+1)
	out = append(out, items[:start]...)
	out = append(out, withEnvironment...)
	return append(out, items[end:]...)
}
func ensureCompletionsEnvironment(items []completionsMessage, dir string) []completionsMessage {
	return executionenv.Ensure(items, dir, func(m completionsMessage) (string, string) {
		text, _ := m.Content.(string)
		return m.Role, text
	}, func(text string) completionsMessage { return completionsMessage{Role: "user", Content: text} })
}

func ensureHistoryEnvironment(items []Message, dir string) []Message {
	return executionenv.Ensure(items, dir, func(m Message) (string, string) {
		return m.Role, m.Content
	}, func(text string) Message { return Message{Role: "user", Content: text} })
}

func ensureCompletionsHistoryEnvironment(items []CompletionsHistoryMessage, dir string) []CompletionsHistoryMessage {
	return executionenv.Ensure(items, dir, func(m CompletionsHistoryMessage) (string, string) {
		return m.Role, m.Content
	}, func(text string) CompletionsHistoryMessage {
		return CompletionsHistoryMessage{Role: "user", Content: text}
	})
}

// Restore references after the last opaque compaction item; older visible
// messages may no longer be available to the model.
func ensureAttachmentContext(ctx context.Context, items []any) []any {
	start, end := 0, len(items)
	for end > 0 {
		m, ok := items[end-1].(map[string]any)
		if !ok || m["type"] != "configuration_update" {
			break
		}
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if m, ok := items[i].(map[string]any); ok && m["type"] == "compaction" {
			start = i + 1
			break
		}
	}
	restored := managedcontext.Ensure(ctx, items[start:end], func(item any) (string, string) {
		m, ok := item.(map[string]any)
		if !ok {
			return "", ""
		}
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		return role, text
	}, func(text string) any { return map[string]any{"type": "message", "role": "user", "content": text} })
	restored = attachmentcontext.Ensure(ctx, restored, func(item any) (string, string) {
		m, ok := item.(map[string]any)
		if !ok {
			return "", ""
		}
		role, _ := m["role"].(string)
		text, _ := m["content"].(string)
		return role, text
	}, func(text string) any { return map[string]any{"type": "message", "role": "user", "content": text} })
	out := append([]any(nil), items[:start]...)
	out = append(out, restored...)
	return append(out, items[end:]...)
}
func ensureCompletionsAttachments(ctx context.Context, items []completionsMessage) []completionsMessage {
	items = managedcontext.Ensure(ctx, items, func(m completionsMessage) (string, string) { s, _ := m.Content.(string); return m.Role, s }, func(s string) completionsMessage { return completionsMessage{Role: "user", Content: s} })
	return attachmentcontext.Ensure(ctx, items, func(m completionsMessage) (string, string) { s, _ := m.Content.(string); return m.Role, s }, func(s string) completionsMessage { return completionsMessage{Role: "user", Content: s} })
}
