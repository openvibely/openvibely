package openaiclient

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestPreparedToolReplayPreservesBudgetAndEdits(t *testing.T) {
	for _, content := range []string{`"result"`, `"\u0061\u003c\ud83d\ude00"`, `"\n<&世"`} {
		row := json.RawMessage(`{"role":"tool","content":` + content + `,"tool_call_id":"c"}`)
		replay := models.ReplayFromRows("", []json.RawMessage{row})
		prepared, err := PrepareCompletionsHistory(replay)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.messages[0].Content != "" || prepared.messages[0].rawContent == nil {
			t.Fatal("tool result eagerly decoded")
		}
		decoded, err := DecodeCompletionsHistory(replay)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(map[string]any{"messages": decoded})
		tokens, err := tokenestimate.FromMessagesJSON(want, "image_url", 2000)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.EstimatedTokens() != tokens {
			t.Fatalf("tokens=%d want=%d for %s", prepared.EstimatedTokens(), tokens, row)
		}
		got, _ := json.Marshal(prepared.DecodedMessages())
		expected, _ := json.Marshal(decoded)
		if string(got) != string(expected) {
			t.Fatalf("%s != %s", got, expected)
		}
		prepared.messages[0].Content = "edited"
		encoded, _ := json.Marshal(prepared.messages[0])
		var message map[string]any
		_ = json.Unmarshal(encoded, &message)
		if message["content"] != "edited" {
			t.Fatal(string(encoded))
		}
	}
}
func TestPreparedToolReplayFallbacks(t *testing.T) {
	for _, raw := range []string{
		`{"role":"tool","content":"x","extension":42}`,
		`{"role":"tool","Content":"last","content":"first"}`,
		`{"role":"tool","content":"first","content":"last"}`,
		`{"role":"tool","content":[{"type":"text","text":"x"}]}`,
	} {
		replay := models.ReplayFromRows("", []json.RawMessage{json.RawMessage(raw)})
		prepared, err := PrepareCompletionsHistory(replay)
		want, werr := DecodeCompletionsHistory(replay)
		if (err == nil) != (werr == nil) {
			t.Fatal(err, werr)
		}
		if err != nil {
			continue
		}
		got, _ := json.Marshal(prepared.DecodedMessages())
		expected, _ := json.Marshal(want)
		if string(got) != string(expected) {
			t.Fatalf("%s != %s", got, expected)
		}
	}
}
func BenchmarkPreparedToolHistory(b *testing.B) {
	payload, _ := json.Marshal(strings.Repeat("tool result 世 <>&\n", 32768))
	row := json.RawMessage(`{"role":"tool","content":` + string(payload) + `,"tool_call_id":"c"}`)
	rows := make([]json.RawMessage, 10)
	for i := range rows {
		rows[i] = row
	}
	replay := models.ReplayFromRows("", rows)
	b.Run("decoded", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m, err := DecodeCompletionsHistory(replay)
			if err != nil || len(m) != 10 {
				b.Fatal(err)
			}
		}
	})
	b.Run("prepared", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			m, err := PrepareCompletionsHistory(replay)
			if err != nil || len(m.messages) != 10 {
				b.Fatal(err)
			}
		}
	})
}
