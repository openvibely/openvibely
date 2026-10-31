package recovery

import (
	"encoding/json"
	"testing"
)

func TestSnapshotSurvivesSourceMutation(t *testing.T) {
	source := []map[string]any{{"role": "tool", "content": "result"}}
	snap, err := Capture(source, func(v map[string]any) ([]byte, error) { return json.Marshal(v) })
	if err != nil {
		t.Fatal(err)
	}
	source[0]["content"] = "changed"
	rows, err := snap.Rows("")
	if err != nil {
		t.Fatal(err)
	}
	rows[0] = nil
	got, _ := snap.Rows("")
	if string(got[0]) != `{"content":"result","role":"tool"}` {
		t.Fatal(string(got[0]))
	}
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(snap.JSON("")), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded[0]["content"] != "result" {
		t.Fatal(decoded)
	}
	legacy := `[{"content":"legacy"}]`
	if snap.JSON(legacy) != legacy {
		t.Fatal("lost legacy override")
	}
	if _, err := snap.Rows("invalid"); err == nil {
		t.Fatal("accepted invalid legacy")
	}
}

func BenchmarkFailedConversationSnapshot(b *testing.B) {
	values := make([]map[string]any, 20)
	for i := range values {
		values[i] = map[string]any{"role": "tool", "content": string(make([]byte, 1<<15))}
	}
	b.Run("whole-array", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			raw, _ := json.Marshal(values)
			var rows []json.RawMessage
			if err := json.Unmarshal(raw, &rows); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("rows", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			snap, err := Capture(values, func(v map[string]any) ([]byte, error) { return json.Marshal(v) })
			if err != nil {
				b.Fatal(err)
			}
			if _, err := snap.Rows(""); err != nil {
				b.Fatal(err)
			}
		}
	})
}
