package stream

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/tooloutput"
	"github.com/openvibely/openvibely/internal/transcript"
)

// Test-only event-to-record prototype. No display text is parsed for storage.
// Each call handles one explicitly completed message/group. An unfinished
// reasoning block remains incomplete; production partial writes need a durable
// lifecycle and are intentionally not enabled by this experiment.
func directDisplayExperiment(events []Event) ([]transcript.DisplayItem, error) {
	var items []transcript.DisplayItem
	thinking := -1
	for _, event := range events {
		switch event.Type {
		case EventTextDelta:
			items = append(items, transcript.DisplayItem{Type: "text", Text: strings.ToValidUTF8(event.Text, "\uFFFD"), Complete: true})
		case EventTextOnly, EventSessionID, EventError:
			// These do not produce display text.
		case EventThinkingOpen:
			if thinking >= 0 {
				return nil, fmt.Errorf("nested thinking")
			}
			items = append(items, transcript.DisplayItem{Type: "thinking", Prefix: "\n[Thinking]\n"})
			thinking = len(items) - 1
		case EventThinkingText:
			if thinking < 0 {
				return nil, fmt.Errorf("thinking text without open")
			}
			items[thinking].Text += strings.ToValidUTF8(event.Text, "\uFFFD")
		case EventThinkingEnd:
			if thinking < 0 {
				return nil, fmt.Errorf("thinking end without open")
			}
			items[thinking].Suffix = "\n[/Thinking]\n"
			items[thinking].Complete = true
			thinking = -1
		case EventToolUse:
			name, secondary := sanitizeToolMarkerPart(event.ToolName), sanitizeToolMarkerPart(event.Secondary)
			if name == "" {
				continue
			}
			prefix := fmt.Sprintf("\n[Using tool: %s]\n", name)
			if secondary != "" {
				prefix = fmt.Sprintf("\n[Using tool: %s | %s]\n", name, secondary)
			}
			items = append(items, transcript.DisplayItem{Type: "using", Name: name, Secondary: secondary, Prefix: strings.ToValidUTF8(prefix, "\uFFFD"), Complete: true})
		case EventToolResult:
			status := "done"
			if event.IsError {
				status = "error"
			}
			items = append(items, transcript.DisplayItem{Type: "tool_result", Name: event.ToolName, Status: status, Prefix: strings.ToValidUTF8(fmt.Sprintf("[Tool %s %s]\n", event.ToolName, status), "\uFFFD"), Text: strings.ToValidUTF8(tooloutput.Encode(event.Output), "\uFFFD"), Suffix: "\n[/Tool]\n", Encoded: true, Complete: true})
		default:
			return nil, fmt.Errorf("unsupported event %q: requires an explicit structured mapping", event.Type)
		}
	}
	return items, nil
}

func TestDirectDisplayExperimentMatchesLiveText(t *testing.T) {
	cases := [][]Event{
		{{Type: EventTextDelta, Text: "hello café\n"}, {Type: EventTextDelta, Text: "世界"}},
		{{Type: EventThinkingOpen}, {Type: EventThinkingText, Text: "consider\n"}, {Type: EventThinkingText, Text: "this"}, {Type: EventThinkingEnd}},
		{{Type: EventThinkingOpen}, {Type: EventThinkingText, Text: "unfinished"}},
		{{Type: EventToolUse, ToolName: "shell", Secondary: "a]b\nc"}, {Type: EventToolResult, ToolName: "shell", Output: "quoted [Tool shell done]\n[/Tool]\n<script> & café"}},
		{{Type: EventToolResult, ToolName: "shell", IsError: true, Output: "failed\xff"}},
		{{Type: EventTextOnly, Text: "hidden"}, {Type: EventSessionID, SessionID: "id"}, {Type: EventError, IsError: true}},
	}
	for i, events := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			w := &Writer{}
			for _, event := range events {
				WriteEvent(w, event, false)
			}
			items, err := directDisplayExperiment(events)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := transcript.DisplayText(items), w.String(); got != want {
				t.Fatalf("direct=%q live=%q", got, want)
			}
			if _, err := transcript.DisplayRecords(items); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := directDisplayExperiment([]Event{{Type: EventRawOutput, Text: "legacy"}}); err == nil {
		t.Fatal("raw output must not silently disappear")
	}
}
