package transcript_test

import (
	"testing"

	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestExchangeTrackerRetainsSteeringAndPartialToolResults(t *testing.T) {
	var exchange transcript.ExchangeTracker[string]
	exchange.Append("prompt", "tool call")
	require.Zero(t, exchange.FirstChanged())
	exchange.Committed()
	exchange.Append("partial result")
	require.Equal(t, 2, exchange.FirstChanged())
	exchange.Committed()
	exchange.Set(2, "completed result")
	exchange.Append("steering", "second tool call")
	require.Equal(t, []string{"prompt", "tool call", "completed result", "steering", "second tool call"}, exchange.Items())
	require.Equal(t, 2, exchange.FirstChanged())
	// A failed save must not acknowledge the edited result. Later appends
	// still write from the earliest unsaved entry.
	exchange.Append("second result")
	require.Equal(t, 2, exchange.FirstChanged())
	exchange.Committed()
	require.Equal(t, 6, exchange.FirstChanged())
	require.False(t, exchange.ReplacesHistory())
}

func TestExchangeTrackerReplacementSurvivesContinuation(t *testing.T) {
	var exchange transcript.ExchangeTracker[string]
	exchange.Append("old prompt", "old answer")
	exchange.Committed()
	compacted := []string{"retained prompt", "native summary"}
	exchange.ReplaceHistory(compacted)
	compacted[0] = "mutated source container"
	require.Equal(t, "retained prompt", exchange.Items()[0])
	require.True(t, exchange.ReplacesHistory())
	require.Zero(t, exchange.FirstChanged())
	exchange.Committed()
	exchange.Append("continued tool call", "continued result", "steering")
	require.True(t, exchange.ReplacesHistory())
	require.Equal(t, 2, exchange.FirstChanged())
	require.NotContains(t, exchange.Items(), "old answer")
}

func TestExchangeTrackerContextInsertionPreservesEarlierWork(t *testing.T) {
	var exchange transcript.ExchangeTracker[string]
	exchange.Append("prompt", "answer", "steering")
	exchange.Committed()
	exchange.ReplaceTail(2, "context update", "steering")
	require.Equal(t, []string{"prompt", "answer", "context update", "steering"}, exchange.Items())
	require.Equal(t, 2, exchange.FirstChanged())
	require.False(t, exchange.ReplacesHistory())
	exchange.ReplaceTail(2)
	require.Equal(t, []string{"prompt", "answer"}, exchange.Items())
	require.Equal(t, 2, exchange.FirstChanged())
}
