package jsonview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeNormalizedMatchesConversion(t *testing.T) {
	for _, raw := range []string{
		`{"z":"large text","a":{"arg":"value"},"n":123.00}`,
		`{"x":9007199254740993,"e":1e-9,"minus":-0}`,
		`{"a":"first","a":"last"}`, `{"a":{"x":1},"a":{"y":2}}`,
		`{"a":"\u0061\u003C\/\u2028\uD834\uDD1E","b":"\ud800"}`,
		`{"text":"<>&\n\t","x":[true,null,{},[]]}`, `null`, `[]`, `"plain"`,
		`{"text":"` + string([]byte{0xff}) + `"}`, `{"x":1e400}`, `invalid`,
	} {
		t.Run(raw, func(t *testing.T) {
			var old any
			oldErr := json.Unmarshal([]byte(raw), &old)
			got, err := DecodeNormalized([]byte(raw))
			if oldErr != nil {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want, err := json.Marshal(old)
			require.NoError(t, err)
			actual, err := json.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, string(want), string(actual))
		})
	}
	raw := json.RawMessage(`{"text":"` + strings.Repeat("big", 4096) + `"}`)
	got, err := DecodeNormalized(raw)
	require.NoError(t, err)
	field := got.(map[string]any)["text"]
	borrowed, ok := field.(Value)
	require.True(t, ok, "large unchanged strings must remain borrowed")
	require.Equal(t, &raw[8], &borrowed.Raw()[0])
}
