package preparedhistory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReusableStateIsolatesRequests(t *testing.T) {
	scope := NewReusableState()
	count := 0
	decode := func(s string) ([]any, error) {
		count++
		var v []any
		err := json.Unmarshal([]byte(s), &v)
		return v, err
	}
	raw := `[{"role":"user","content":{"text":"hello"}}]`
	first, err := TakeState(scope.Context(context.Background()), raw, decode)
	require.NoError(t, err)
	first[0].(map[string]any)["content"].(map[string]any)["text"] = "mutated"
	second, err := TakeState(scope.Context(context.Background()), raw, decode)
	require.NoError(t, err)
	require.Equal(t, "hello", second[0].(map[string]any)["content"].(map[string]any)["text"])
	require.Equal(t, 1, count)
	_, err = TakeState(scope.Context(context.Background()), `[]`, decode)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	ForgetState[[]any](scope.Context(context.Background()))
	_, err = TakeState(scope.Context(context.Background()), `[]`, decode)
	require.NoError(t, err)
	require.Equal(t, 3, count)
}
func TestReusableStateConcurrentMutation(t *testing.T) {
	scope := NewReusableState()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			value, err := TakeState(scope.Context(context.Background()), "same", func(string) ([]any, error) { return []any{map[string]any{"content": []any{"original"}}}, nil })
			if err != nil {
				t.Error(err)
				return
			}
			value[0].(map[string]any)["content"].([]any)[0] = "changed"
		})
	}
	wg.Wait()
}
func BenchmarkRepeatedCheckpointDecode(b *testing.B) {
	raw := `[{"role":"user","content":[{"type":"input_image","image_url":"` + strings.Repeat("a", 11<<20) + `"}]}]`
	decode := func(s string) ([]any, error) { var v []any; err := json.Unmarshal([]byte(s), &v); return v, err }
	for _, reuse := range []bool{false, true} {
		name := "reload"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			scope := NewReusableState()
			ctx := scope.Context(context.Background())
			_, _ = ReadState(ctx, raw, decode)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if !reuse {
					ctx = WithScope(context.Background())
				}
				v, err := TakeState(ctx, raw, decode)
				if err != nil || len(v) != 1 {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestReusableStateKeepsRecoveryReplacementLocal(t *testing.T) {
	scope := NewReusableState()
	scope.BindReplay(models.ExecutionReplayMessage{TranscriptJSON: "original"})
	counts := map[string]int{}
	decode := func(raw string) ([]any, error) { counts[raw]++; return []any{raw}, nil }
	first := scope.Context(context.Background())
	_, err := TakeState(first, "original", decode)
	require.NoError(t, err)
	_, err = TakeState(first, "recovery", decode)
	require.NoError(t, err)
	second := scope.Context(context.Background())
	_, err = TakeState(second, "original", decode)
	require.NoError(t, err)
	_, err = TakeState(second, "recovery", decode)
	require.NoError(t, err)
	require.Equal(t, 1, counts["original"])
	require.Equal(t, 2, counts["recovery"])
}
