package stream

import (
	"fmt"
	"regexp"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/transcript"
)

// CompactionReporter keeps activity in the persisted transcript, out of text-only output.
func CompactionReporter(writer *Writer, inThinking *bool) func(llmcontracts.CompactionProgress) {
	if inThinking == nil {
		thinking := unfinishedThinking(writer.String())
		inThinking = &thinking
	}
	return func(progress llmcontracts.CompactionProgress) {
		// Activity must be outside Markdown even when the interrupted content
		// was visible response text rather than thinking.
		if progress.State == "started" || *inThinking {
			if fence := transcript.UnclosedMarkdownFence(withoutToolOutput(writer.String())); fence != "" {
				writer.Write([]byte("\n" + fence + "\n"))
			}
		}
		if *inThinking {
			*inThinking = false
			WriteEvent(writer, Event{Type: EventThinkingEnd}, false)
		}
		switch progress.State {
		case "started":
			writer.Write([]byte("\n[Compaction started]\n"))
		case "done", "failed":
			writer.Write([]byte(fmt.Sprintf("\n[Compaction %s | %d]\n", progress.State, progress.Duration.Milliseconds())))
		}
	}
}

// Recover the thinking state when a fallback writer resumes persisted output.
var thinkingStateMarkers = regexp.MustCompile(`(?m)^[\t ]*(\[/?Thinking\]|\[Tool \S+ (?:done|error)\]|\[/Tool\])[\t ]*\r?$`)

func unfinishedThinking(text string) bool {
	text = transcript.NormalizeMarkers(withoutToolOutput(text))
	ranges := transcript.MarkdownCodeRanges(text)
	thinking, tool := false, false
	for _, match := range thinkingStateMarkers.FindAllStringIndex(text, -1) {
		marker := strings.TrimSpace(text[match[0]:match[1]])
		if tool {
			if marker == "[/Tool]" {
				tool = false
			}
			continue
		}
		if transcript.PositionInMarkdownCode(ranges, match[0]) {
			continue
		}
		switch marker {
		case "[Thinking]":
			thinking = true
		case "[/Thinking]":
			thinking = false
		case "[/Tool]":
		default:
			tool = true
		}
	}
	return thinking
}

// Tool results are separate transcript sections; their Markdown must not change
// the interpretation of subsequent assistant activity. Keep quoted tool blocks.
var recoveryToolBlocks = regexp.MustCompile(`(?m)^[\t ]*\[Tool \S+ (?:done|error)\][\t ]*\r?\n[\s\S]*?^[\t ]*\[/Tool\][\t ]*\r?(?:\n|$)`)

func withoutToolOutput(text string) string {
	var out strings.Builder
	previous := 0
	for _, match := range recoveryToolBlocks.FindAllStringIndex(text, -1) {
		out.WriteString(text[previous:match[0]])
		// Checking the prefix prevents an unfinished fence inside this tool result
		// from shielding its own opener or a later transcript section.
		if transcript.UnclosedMarkdownFence(out.String()) != "" {
			out.WriteString(text[match[0]:match[1]])
		} else {
			out.WriteByte('\n')
		}
		previous = match[1]
	}
	out.WriteString(text[previous:])
	return out.String()
}
