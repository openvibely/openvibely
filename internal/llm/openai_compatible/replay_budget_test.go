package openai_compatible

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReplayBudgetCountsOutgoingReasoningOnce(t *testing.T) {
	reasoning := strings.Repeat("reasoning ", 1000)
	messages := []openaiclient.CompletionsHistoryMessage{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer", ReasoningContent: reasoning}}
	raw, err := json.Marshal(messages)
	require.NoError(t, err)
	history := []models.Execution{{PromptSent: "question", Output: "answer", ReasoningContent: reasoning, ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "question", AssistantContent: "answer", ReasoningContent: reasoning, TranscriptJSON: string(raw)}}}}
	wire, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	expected, err := tokenestimate.FromMessagesJSON(wire, "image_url", 2000)
	require.NoError(t, err)
	require.Equal(t, expected, EstimateHistoryTokens(history))
	require.Equal(t, EstimateHistoryTokens([]models.Execution{{ReplayMessages: history[0].ReplayMessages}}), EstimateHistoryTokens(history))
	messages[1].ReasoningContent = ""
	noReasoning, err := json.Marshal(messages)
	require.NoError(t, err)
	require.Greater(t, EstimateHistoryTokens(history), EstimateHistoryTokens([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(noReasoning)}}}}))
}
func TestReplayBudgetDoesNotCountImageBytesAsText(t *testing.T) {
	makeHistory := func(size int) []models.Execution {
		raw, _ := json.Marshal([]openaiclient.CompletionsHistoryMessage{{Role: "user", ImageContent: []map[string]interface{}{{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64," + strings.Repeat("A", size)}}}}})
		return []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
	}
	require.Equal(t, EstimateHistoryTokens(makeHistory(10)), EstimateHistoryTokens(makeHistory(1000000)))
}

func TestEmptyReplayHistoryHasNoBudgetCost(t *testing.T) {
	require.Zero(t, EstimateHistoryTokens(nil))
}

func TestTextBudgetMatchesSerializedRequest(t *testing.T) {
	for _, text := range []string{"", "ordinary text", "<tag>&\"\\\n\t\x00", "é世界\u2028\u2029", string([]byte{0xff, 0xfe, 0xc0})} {
		call := openaiclient.CompletionsToolCall{ID: "call-1", Type: "function"}
		call.Function.Name = "read_file"
		call.Function.Arguments = `{"path":"<a>&b"}`
		messages := []openaiclient.CompletionsHistoryMessage{
			{Role: "user", Content: text}, {Role: "assistant", Content: text, ReasoningContent: text, ToolCalls: []openaiclient.CompletionsToolCall{call}},
			{Role: "tool", ToolCallID: "call-1", Content: text},
		}
		raw, err := json.Marshal(messages)
		require.NoError(t, err)
		history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
		// Estimate the adapter's actual decoded output, including legacy normalization.
		wire, err := json.Marshal(map[string]any{"messages": buildClientHistory(history)})
		require.NoError(t, err)
		want, err := tokenestimate.FromMessagesJSON(wire, "image_url", 2000)
		require.NoError(t, err)
		require.Equal(t, want, EstimateHistoryTokens(history), "text %q", text)
	}
}

func TestJSONStringBytesMatchesEncoder(t *testing.T) {
	for i := 0; i < 256; i++ {
		s := string([]byte{byte(i)}) + "é\u2028\u2029<&>\"\\"
		raw, err := json.Marshal(s)
		require.NoError(t, err)
		require.Equal(t, len(raw), jsonStringBytes(s), "byte %d", i)
	}
}

func FuzzJSONStringBytes(f *testing.F) {
	for _, s := range []string{"", "a\x00\n<&>", "é世界\u2028\u2029", string([]byte{0xff, 0xc0})} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if n := jsonStringBytes(s); n != len(raw) {
			t.Fatalf("count %d != encoded length %d for %q", n, len(raw), s)
		}
	})
}

func BenchmarkTextReplayBudget(b *testing.B) {
	messages := []openaiclient.CompletionsHistoryMessage{{Role: "user", Content: "read file"}, {Role: "tool", ToolCallID: "call-1", Content: strings.Repeat("line <example>\n", 40000)}}
	raw, _ := json.Marshal(messages)
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
	for _, legacy := range []bool{true, false} {
		name := "counted"
		if legacy {
			name = "serialized"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if legacy {
					wire, _ := json.Marshal(map[string]any{"messages": buildClientHistory(history)})
					_, _ = tokenestimate.FromMessagesJSON(wire, "image_url", 2000)
				} else {
					_ = EstimateHistoryTokens(history)
				}
			}
		})
	}
}
