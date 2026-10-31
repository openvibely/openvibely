package anthropicclient

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRepairedRawBlocksPreserveWireAndSource(t *testing.T) {
	rows := []json.RawMessage{json.RawMessage(`{ "type":"tool_result", "tool_use_id":"a", "content":"result" }`)}
	before := string(rows[0])
	got, err := marshalAgenticMessage(agenticMessage{Role: "user", Content: rows})
	require.NoError(t, err)
	require.JSONEq(t, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"result"}]}`, string(got))
	require.Contains(t, string(got), before)
	require.Equal(t, before, string(rows[0]))
}

func TestCompactionPresenceValidatesFields(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`[{"type":"compaction","content":""}]`, true},
		{`[{"type":"compaction","content":null}]`, false},
		{`[{"type":"compaction","content":12}]`, false},
		{`[{"type":"compaction","content":"x","encrypted_content":12}]`, false},
		{`[{"Type":"compaction","Content":"x"}]`, true},
	} {
		require.Equal(t, tc.want, hasLeadingCompactionMessage([]agenticMessage{{Role: "assistant", Content: json.RawMessage(tc.raw)}}), tc.raw)
	}
}
