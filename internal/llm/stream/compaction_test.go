package stream

import (
	"context"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"strings"
	"testing"
	"time"
)

func TestCompactionReporterClosesThinkingAndKeepsTextClean(t *testing.T) {
	writer := NewWriter("", "", nil, context.Background(), time.Hour)
	defer writer.Stop()
	thinking := true
	WriteEvent(writer, Event{Type: EventThinkingOpen}, false)
	report := CompactionReporter(writer, &thinking)
	report(llmcontracts.CompactionProgress{State: "started"})
	report(llmcontracts.CompactionProgress{State: "done", Duration: 4 * time.Second})
	WriteEvent(writer, Event{Type: EventTextDelta, Text: "Continuing"}, false)
	if thinking || !strings.Contains(writer.String(), "[/Thinking]\n\n[Compaction started]\n\n[Compaction done | 4000]\n") {
		t.Fatalf("unexpected transcript %q", writer.String())
	}
	if writer.TextString() != "Continuing" {
		t.Fatalf("activity leaked into answer: %q", writer.TextString())
	}
}

func TestCompactionRecoversPersistedThinkingState(t *testing.T) {
	for _, text := range []string{
		"[Thinking]\nInterrupted",
		"[Thinking]\nInterrupted\n[Tool bash done]\n[/Thinking]\n[/Tool]\n",
		"[Thinking]\nInterrupted\n```text\n[/Thinking]\n```\n",
	} {
		writer := NewWriter("", "", nil, context.Background(), time.Hour)
		writer.Write([]byte(text))
		CompactionReporter(writer, nil)(llmcontracts.CompactionProgress{State: "started"})
		writer.Stop()
		if !strings.HasSuffix(writer.String(), "[/Thinking]\n\n[Compaction started]\n") {
			t.Fatalf("output=%q", writer.String())
		}
	}
}
