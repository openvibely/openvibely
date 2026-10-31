package requestjson

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/stretchr/testify/require"
)

func TestPreparedRequestPreservesWireAndBudget(t *testing.T) {
	for _, imageType := range []string{"image", "image_url"} {
		for _, content := range []any{"hello <>&\n世界", nil, []any{map[string]any{"type": imageType, "source": map[string]any{"data": strings.Repeat("A", 10000)}}}, []any{map[string]any{"type": "tool_result", "content": []any{map[string]any{"type": imageType, "data": "aaa"}}}}} {
			messages := []any{map[string]any{"role": "user", "content": content}}
			f, err := Prepare("messages", messages)
			require.NoError(t, err)
			meta := map[string]any{"model": "test", "messages": nil, "tools": []any{map[string]any{"description": "quoted \"messages\":null"}}}
			got, err := f.Marshal(meta)
			require.NoError(t, err)
			original := map[string]any{"model": "test", "messages": messages, "tools": meta["tools"]}
			want, err := json.Marshal(original)
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(got))
			again, err := f.Marshal(meta)
			require.NoError(t, err)
			require.Equal(t, string(got), string(again))
			size, err := f.Size(meta)
			require.NoError(t, err)
			require.Equal(t, len(want), size)
			adjust, err := tokenestimate.MessageArrayAdjustment(f.JSON(), imageType, 2000)
			require.NoError(t, err)
			tokens, err := tokenestimate.FromMessagesJSON(want, imageType, 2000)
			require.NoError(t, err)
			require.Equal(t, tokens, tokenestimate.FromByteCount(size+adjust))
		}
	}
}

func TestPreparedFieldRejectsPopulatedMetadata(t *testing.T) {
	f, err := Prepare("messages", []any{})
	require.NoError(t, err)
	_, err = f.Marshal(map[string]any{"messages": []any{"duplicate"}})
	require.Error(t, err)
	_, err = f.Marshal(nil)
	require.Error(t, err)
}

func TestRawItemsPreserveJSONSemantics(t *testing.T) {
	for _, items := range [][]any{nil, {}, {json.RawMessage(`{ "text": "<>&" }`), map[string]any{"text": "x"}}, {json.RawMessage(nil)}, {json.RawMessage(`invalid`)}} {
		want, werr := json.Marshal(map[string]any{"input": items, "model": "test"})
		got, err := MarshalItems(map[string]any{"model": "test"}, "input", items)
		if werr != nil {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got))
	}
}

func BenchmarkPreparedRequest(b *testing.B) {
	messages := []any{map[string]any{"role": "tool", "content": strings.Repeat("result line\n", 50000)}}
	for _, prepared := range []bool{false, true} {
		name := "legacy"
		if prepared {
			name = "shared"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if prepared {
					f, err := Prepare("messages", messages)
					if err != nil {
						b.Fatal(err)
					}
					meta := map[string]any{"tools": []any{}}
					size, _ := f.Size(meta)
					adjust, err := tokenestimate.MessageArrayAdjustment(f.JSON(), "image", 2000)
					if err != nil {
						b.Fatal(err)
					}
					_ = tokenestimate.FromByteCount(size + adjust)
					if _, err = f.Marshal(map[string]any{"model": "test"}); err != nil {
						b.Fatal(err)
					}
				} else {
					encoded, _ := json.Marshal(map[string]any{"messages": messages, "tools": []any{}})
					if _, err := tokenestimate.FromMessagesJSON(encoded, "image", 2000); err != nil {
						b.Fatal(err)
					}
					if _, err := json.Marshal(map[string]any{"model": "test", "messages": messages}); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
