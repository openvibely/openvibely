package anthropic

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/replayjson"
	client "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReplayEncoderDetachesContentBlocks(t *testing.T) {
	e := newReplayEncoder()
	value := anthropicEnvironmentReplay{Environment: "env", Messages: []client.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"text":"a"}]`)}}}
	first, err := e.Encode(value)
	require.NoError(t, err)
	again, err := e.Encode(value)
	require.NoError(t, err)
	require.Equal(t, first, again)
	for i, c := range value.Messages[0].ContentBlocks {
		if c == 'a' {
			value.Messages[0].ContentBlocks[i] = 'b'
		}
	}
	changed, err := e.Encode(value)
	require.NoError(t, err)
	require.NotEqual(t, first, changed)
	expected, err := json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
	value.Environment = "different"
	changed, err = e.Encode(value)
	require.NoError(t, err)
	expected, err = json.Marshal(value)
	require.NoError(t, err)
	require.Equal(t, string(expected), changed)
}

func BenchmarkReplayCheckpointAndFinal(b *testing.B) {
	value := anthropicEnvironmentReplay{Environment: "env", Messages: []client.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"text":"` + strings.Repeat("history text ", 65536) + `"}]`)}}}
	for _, reuse := range []bool{false, true} {
		name := "encode-twice"
		if reuse {
			name = "shared-encoder"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				e := newReplayEncoder()
				for j := 0; j < 2; j++ {
					var err error
					if reuse {
						_, err = e.Encode(value)
					} else {
						_, err = replayjson.Encode(value)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
