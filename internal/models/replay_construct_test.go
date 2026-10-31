package models

import (
	"encoding/json"
	"testing"
)

func TestReplayRowsCompatibilitySize(t *testing.T) {
	for _, key := range []string{"", "messages", "responses_input"} {
		for _, rows := range [][]json.RawMessage{nil, {}, {json.RawMessage(` {"content":"世"} `), json.RawMessage(`null`)}} {
			replay := ReplayFromRows(key, rows)
			for _, envelope := range []map[string]json.RawMessage{nil, {"version": json.RawMessage(`1`), "metadata": json.RawMessage(`"<>&"`)}} {
				replay.Rows.Envelope = envelope
				if got, want := replay.TranscriptBytes(), len(replay.FullTranscriptJSON()); got != want {
					t.Fatalf("%s size=%d want=%d", replay.FullTranscriptJSON(), got, want)
				}
			}
		}
	}
}
