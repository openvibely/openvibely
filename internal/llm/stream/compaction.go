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
		if inThinking != nil && *inThinking {
			*inThinking = false
			if fence := transcript.UnclosedMarkdownFence(writer.String()); fence != "" {
				writer.Write([]byte("\n" + fence + "\n"))
			}
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
	text = transcript.NormalizeMarkers(text)
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
