package tokenestimate

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEnvelopeRowsMatchSerializedBudget(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `[null]`, `[{"content":"text"}]`, `[{"content":[{"type":"image","source":{"data":"AAAA"}}]}]`, `[{"content":[{"type":"tool_result","content":[{"type":"image_url","url":"AAA"}]}]}]`} {
		rows, err := jsonview.Array([]byte(raw))
		require.NoError(t, err)
		wire, err := json.Marshal(map[string]any{"messages": json.RawMessage(raw), "version": 1})
		require.NoError(t, err)
		for _, kind := range []string{"image", "image_url"} {
			expected, err := FromMessagesJSON(wire, kind, 2000)
			require.NoError(t, err)
			actual, err := FromEnvelopeRows(len(wire), rows, kind, 2000)
			require.NoError(t, err)
			require.Equal(t, expected, actual)
		}
	}
}
