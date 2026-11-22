package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func TestGeminiHistoryImageBudget(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			image := &geminiclient.InlineData{MimeType: "image/png", Data: strings.Repeat("A", 6*1024*1024)}
			part := geminiclient.Part{InlineData: image}
			if nested {
				part = geminiclient.Part{FunctionResponse: &geminiclient.FunctionResponse{Name: "screenshot", Response: map[string]any{"output": "captured"}, Parts: []geminiclient.FunctionResponsePart{{InlineData: image}}}}
			}
			content := geminiclient.Content{Role: "user", Parts: []geminiclient.Part{part}}
			raw, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			replay := models.ReplayFromRows("contents", []json.RawMessage{raw})
			if legacy {
				replay = models.ExecutionReplayMessage{TranscriptJSON: replay.FullTranscriptJSON()}
			}
			// Display copies must not be counted in addition to model-facing replay.
			history := []models.Execution{{PromptSent: strings.Repeat("display", 10000), ReplayMessages: []models.ExecutionReplayMessage{replay}}}
			got := estimateHistoryTokens(models.ProviderGemini, history)
			want, err := geminiclient.EstimateContentsTokens([]geminiclient.Content{content})
			if err != nil {
				t.Fatal(err)
			}
			if got != want || got < 1122 || got > 1500 {
				t.Fatalf("nested=%v legacy=%v: got %d want %d", nested, legacy, got, want)
			}
			if historyNeedsCompaction(requestBudget{HistoryTokens: got, SafeInputLimit: 900000}, 800000) {
				t.Fatal("image incorrectly triggers compaction")
			}
		}
	}
}

func TestGeminiHistoryLargeTextStillTriggersCompaction(t *testing.T) {
	text := strings.Repeat("x", 4*1024*1024)
	for _, native := range []bool{false, true} {
		exec := models.Execution{PromptSent: text}
		if native {
			raw, err := json.Marshal(geminiclient.TextContent("user", text))
			if err != nil {
				t.Fatal(err)
			}
			exec = models.Execution{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("contents", []json.RawMessage{raw})}}
		}
		got := estimateHistoryTokens(models.ProviderGemini, []models.Execution{exec})
		if !historyNeedsCompaction(requestBudget{HistoryTokens: got, SafeInputLimit: 900000}, 800000) {
			t.Fatalf("native=%v: large text failed to trigger compaction: %d", native, got)
		}
	}
}
