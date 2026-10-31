package streamdecode

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestFramingAndLimits(t *testing.T) {
	for _, tc := range []struct {
		line   string
		strict bool
		want   string
		ok     bool
	}{
		{" data: value \r", false, "value", true}, {"data:value", false, "value", true},
		{"data:value", true, "", false}, {"data: value ", true, "value ", true}, {"event: x", false, "", false},
	} {
		data, ok := Data([]byte(tc.line), tc.strict)
		require.Equal(t, tc.ok, ok)
		require.Equal(t, tc.want, string(data))
	}
	s := Scanner(strings.NewReader(strings.Repeat("x", 100000)+"\n"), 200000)
	require.True(t, s.Scan())
	require.Len(t, s.Bytes(), 100000)
	require.NoError(t, s.Err())
	s = Scanner(strings.NewReader(strings.Repeat("x", 200001)), 200000)
	require.False(t, s.Scan())
	require.Error(t, s.Err())
}
func TestRecoveryViewsPreserveOrderAndFiltering(t *testing.T) {
	var text Text
	a, b := new(strings.Builder), new(strings.Builder)
	a.WriteString("first")
	text.View(a, a.String(), 0)
	a.WriteString(" second")
	text.View(a, a.String(), 5)
	b.WriteString(" other")
	text.View(b, b.String(), 0)
	start := a.Len()
	a.WriteString(" last")
	text.View(a, a.String(), start)
	text.Append(" filtered")
	require.Equal(t, "first second other last filtered", text.String())
	require.Len(t, text.parts, 4)
}
