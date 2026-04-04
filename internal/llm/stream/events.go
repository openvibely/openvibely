package stream

import (
	"fmt"
	"strings"
)

// EventType represents a canonical stream event emitted by provider parsers/callbacks.
type EventType string

const (
	EventTextDelta    EventType = "text_delta"
	EventTextOnly     EventType = "text_only"
	EventRawOutput    EventType = "raw_output"
	EventThinkingOpen EventType = "thinking_open"
	EventThinkingText EventType = "thinking_text"
	EventThinkingEnd  EventType = "thinking_end"
	EventToolUse      EventType = "tool_use"
	EventToolResult   EventType = "tool_result"
	EventSessionID    EventType = "session_id"
	EventError        EventType = "error"
)

// Event is a provider-agnostic stream event shape.
type Event struct {
	Type      EventType
	Text      string
	ToolName  string
	Secondary string
	Output    string
	IsError   bool
	Subtype   string
	SessionID string
}

// WriteEvent maps canonical events to writer output/state in one place.
func WriteEvent(writer *Writer, event Event, skipThinking bool) {
	switch event.Type {
	case EventTextDelta:
		if event.Text == "" {
			return
		}
		writer.Write([]byte(event.Text))
		writer.WriteText([]byte(event.Text))
	case EventTextOnly:
		if event.Text == "" {
			return
		}
		writer.WriteText([]byte(event.Text))
	case EventRawOutput:
		if event.Text == "" {
			return
		}
		writer.Write([]byte(event.Text))
	case EventThinkingOpen:
		if skipThinking {
			return
		}
		writer.Write([]byte("\n[Thinking]\n"))
	case EventThinkingText:
		if skipThinking || event.Text == "" {
			return
		}
		writer.Write([]byte(event.Text))
	case EventThinkingEnd:
		if skipThinking {
			return
		}
		writer.Write([]byte("\n[/Thinking]\n"))
	case EventToolUse:
		toolName := sanitizeToolMarkerPart(event.ToolName)
		if toolName == "" {
			return
		}
		secondary := sanitizeToolMarkerPart(event.Secondary)
		if secondary != "" {
			writer.Write([]byte(fmt.Sprintf("\n[Using tool: %s | %s]\n", toolName, secondary)))
			return
		}
		writer.Write([]byte(fmt.Sprintf("\n[Using tool: %s]\n", toolName)))
	case EventToolResult:
		status := "done"
		if event.IsError {
			status = "error"
		}
		preview := event.Output
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		writer.Write([]byte(fmt.Sprintf("[Tool %s %s]\n%s\n[/Tool]\n", event.ToolName, status, preview)))
	case EventSessionID:
		if event.SessionID != "" {
			writer.SetSessionID(event.SessionID)
		}
	case EventError:
		if event.IsError {
			writer.MarkError(event.Subtype)
		}
	}
}

// sanitizeToolMarkerPart prevents reserved marker delimiters from leaking into
// [Using tool: ...] lines where they can break downstream parsing/rendering.
func sanitizeToolMarkerPart(value string) string {
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "]", ")")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
