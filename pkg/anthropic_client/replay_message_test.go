package anthropicclient

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBorrowedReplayMessageMatchesPublicDecoder(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"role":"user","content":"é","content_blocks":[{"type":"tool_result","content":"large"}]}`, `{"ROLE":"first","role":"user","CONTENT_BLOCKS":null}`, `{"role":3}`, `{"content":[]}`, `!`} {
		var want Message
		err := json.Unmarshal([]byte(raw), &want)
		got, gerr := DecodeReplayMessage(json.RawMessage(raw))
		require.Equal(t, err == nil, gerr == nil, raw)
		if err == nil {
			require.Equal(t, want, got)
		}
	}
}
