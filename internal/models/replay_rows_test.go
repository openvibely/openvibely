package models

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReplayRowsDecodeMatchesSerializedHistory(t *testing.T) {
	for _, r := range []ReplayRows{
		{Format: "array", Items: []json.RawMessage{json.RawMessage(`{"role":"user","content":"世界"}`), json.RawMessage(`{"role":"assistant","content":"hello"}`)}},
		{Format: "envelope", ItemsKey: "messages", Envelope: map[string]json.RawMessage{"environment": json.RawMessage(`"context"`)}, Items: []json.RawMessage{json.RawMessage(`{"role":"assistant","content":"hello","unknown":{"value":1}}`)}},
		{Format: "envelope", ItemsKey: "messages", NullItems: true},
		{Format: "envelope", ItemsKey: "messages", Items: []json.RawMessage{}},
		{Format: "opaque", Items: []json.RawMessage{json.RawMessage(`{"private":{"id":"abc"}}`)}},
	} {
		t.Run(r.Format+string(r.ItemsKey), func(t *testing.T) {
			message := ExecutionReplayMessage{Rows: &r}
			raw := message.FullTranscriptJSON()
			type item struct{ Role, Content string }
			if r.Format == "array" {
				var want, got []item
				require.NoError(t, json.Unmarshal([]byte(raw), &want))
				require.NoError(t, message.DecodeTranscript(&got))
				require.Equal(t, want, got)
			} else {
				type envelope struct {
					Messages    []item
					Environment string
				}
				var want, got envelope
				require.NoError(t, json.Unmarshal([]byte(raw), &want))
				require.NoError(t, message.DecodeTranscript(&got))
				require.Equal(t, want, got)
			}
			var want, got any
			require.NoError(t, json.Unmarshal([]byte(raw), &want))
			require.NoError(t, message.DecodeTranscript(&got))
			require.Equal(t, want, got)
			encoded, err := json.Marshal(message)
			require.NoError(t, err)
			var roundtrip ExecutionReplayMessage
			require.NoError(t, json.Unmarshal(encoded, &roundtrip))
			require.JSONEq(t, raw, roundtrip.TranscriptJSON)
			require.Nil(t, roundtrip.Rows)
		})
	}
}

func TestReplayRowsExplicitReplacementWins(t *testing.T) {
	m := ExecutionReplayMessage{TranscriptJSON: `[{"role":"user","content":"new"}]`, Rows: &ReplayRows{Format: "array", Items: []json.RawMessage{json.RawMessage(`{"role":"user","content":"old"}`)}}}
	var got []map[string]any
	require.NoError(t, m.DecodeTranscript(&got))
	require.Equal(t, "new", got[0]["content"])
	require.Equal(t, m.TranscriptJSON, m.FullTranscriptJSON())
}

type customReplayMessages []string

func (m *customReplayMessages) UnmarshalJSON(raw []byte) error {
	var v []string
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*m = append(v, "custom-decoder")
	return nil
}

func TestReplayRowsPreserveCollectionDecoderAndNull(t *testing.T) {
	row := ExecutionReplayMessage{Rows: &ReplayRows{Format: "envelope", ItemsKey: "messages", Items: []json.RawMessage{json.RawMessage(`"hello"`)}}}
	var got struct {
		Messages customReplayMessages `json:"messages"`
	}
	require.NoError(t, row.DecodeTranscript(&got))
	require.Equal(t, customReplayMessages{"hello", "custom-decoder"}, got.Messages)
	row.Rows = &ReplayRows{Format: "array", NullItems: true}
	messages := []string{"old"}
	require.NoError(t, row.DecodeTranscript(&messages))
	require.Nil(t, messages)
}
