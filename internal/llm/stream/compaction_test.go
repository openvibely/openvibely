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

func TestCompactionClosesInterruptedThinkingFence(t *testing.T) {
	for _, fence := range []string{"```", "~~~~", "`````"} {
		for _, live := range []bool{false, true} {
			writer := NewWriter("", "", nil, context.Background(), time.Hour)
			writer.Write([]byte("[Thinking]\n" + fence + "go\npartial"))
			var thinking *bool
			if live {
				value := true
				thinking = &value
			}
			CompactionReporter(writer, thinking)(llmcontracts.CompactionProgress{State: "started"})
			writer.Stop()
			if !strings.Contains(writer.String(), "partial\n"+fence+"\n\n[/Thinking]\n\n[Compaction started]") {
				t.Fatalf("transcript=%q", writer.String())
			}
		}
	}
}

func TestCompactionRecoveryIgnoresToolMarkdown(t *testing.T) {
	for _, thinking := range []string{"[Thinking]\nInterrupted", "[Thinking]\n~~~~go\npartial"} {
		prefix := "[Tool read_file done]\n```text\n[Thinking]\nfile excerpt\n[/Tool]\n"
		writer := NewWriter("", "", nil, context.Background(), time.Hour)
		writer.Write([]byte(prefix + thinking))
		CompactionReporter(writer, nil)(llmcontracts.CompactionProgress{State: "started"})
		writer.Stop()
		got := writer.String()
		if !strings.HasPrefix(got, prefix+thinking) || !strings.HasSuffix(got, "[/Thinking]\n\n[Compaction started]\n") {
			t.Fatalf("incorrect recovery: %q", got)
		}
		if strings.Count(got, "```") != 1 {
			t.Fatalf("closed tool fence outside tool: %q", got)
		}
	}
	if unfinishedThinking("[Tool read_file done]\n[Thinking]\n[/Tool]\n") {
		t.Fatal("tool content became thinking")
	}
	quoted := "```text\n[Tool read_file done]\nexample\n[/Tool]\n```\n"
	if withoutToolOutput(quoted) != quoted {
		t.Fatal("modified quoted tool block")
	}
}

func TestCompactionClosesVisibleResponseFence(t *testing.T) {
	for _, fence := range []string{"```", "~~~~", "`````"} {
		for _, live := range []bool{false, true} {
			writer := NewWriter("", "", nil, context.Background(), time.Hour)
			original := "Here is the code:\n" + fence + "go\npartial"
			WriteEvent(writer, Event{Type: EventTextDelta, Text: original}, false)
			var thinking *bool
			if live {
				value := false
				thinking = &value
			}
			report := CompactionReporter(writer, thinking)
			report(llmcontracts.CompactionProgress{State: "started"})
			report(llmcontracts.CompactionProgress{State: "done", Duration: time.Second})
			writer.Stop()
			want := original + "\n" + fence + "\n\n[Compaction started]\n\n[Compaction done | 1000]\n"
			if got := writer.String(); got != want {
				t.Fatalf("transcript=%q, want %q", got, want)
			}
			if writer.TextString() != original {
				t.Fatal("recovery changed text-only output")
			}
		}
	}
}
