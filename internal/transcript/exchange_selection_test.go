package transcript

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExchangeSelectionShared(t *testing.T) {
	prefix := []any{"old"}
	for _, format := range []string{"responses_input", "messages", "array"} {
		t.Run(format, func(t *testing.T) {
			delta := selectExchange(ExchangeUpdate{Items: []any{"old", "new"}, Prefix: prefix, CompleteHistory: true, FirstChanged: 1}, true, false)
			require.Equal(t, []any{"new"}, delta.Items)
			require.False(t, delta.ReplacesHistory)
			require.Zero(t, delta.FirstChanged)
			local := selectExchange(ExchangeUpdate{Items: []any{"new"}}, true, false)
			require.Equal(t, delta.Items, local.Items)
			for _, tc := range []struct {
				items              []any
				durable, rewritten bool
			}{
				{[]any{"changed", "new"}, true, false},
				{[]any{"old", "new"}, false, false},
				{[]any{"old", "new"}, true, true},
				{[]any{}, true, false},
			} {
				got := selectExchange(ExchangeUpdate{Items: tc.items, Prefix: prefix, CompleteHistory: true, FirstChanged: 1}, tc.durable, tc.rewritten)
				require.True(t, got.ReplacesHistory)
				require.Equal(t, tc.items, got.Items)
				require.Zero(t, got.FirstChanged)
			}
		})
	}
}
