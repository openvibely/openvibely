package stream

import (
	"fmt"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

// CompactionReporter keeps activity in the persisted transcript, out of text-only output.
func CompactionReporter(writer *Writer, inThinking *bool) func(llmcontracts.CompactionProgress) {
	return func(progress llmcontracts.CompactionProgress) {
		if inThinking != nil && *inThinking {
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
