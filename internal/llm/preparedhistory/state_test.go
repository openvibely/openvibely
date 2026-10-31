package preparedhistory

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNativeStateOwnershipAndInvalidation(t *testing.T) {
	ctx := WithScope(context.Background())
	calls := 0
	decode := func(raw string) ([]string, error) {
		calls++
		var v []string
		err := json.Unmarshal([]byte(raw), &v)
		return v, err
	}
	a, err := ReadState(ctx, `["a"]`, decode)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := TakeState(ctx, `["a"]`, decode)
	if calls != 1 || &a[0] != &b[0] {
		t.Fatal("not reused")
	}
	b[0] = "mutated"
	c, _ := ReadState(ctx, `["a"]`, decode)
	if calls != 2 || c[0] != "a" {
		t.Fatal("retained mutable state")
	}
	d, _ := TakeState(ctx, `["compacted"]`, decode)
	if calls != 3 || d[0] != "compacted" {
		t.Fatal("stale state")
	}
	_, _ = ReadState(ctx, `invalid`, decode)
	_, _ = ReadState(ctx, `invalid`, decode)
	if calls != 5 {
		t.Fatal("cached error")
	}
	_, _ = ReadState(WithScope(ctx), `["a"]`, decode)
	if calls != 6 {
		t.Fatal("nested scope reused")
	}
}

func BenchmarkNativeStateBudgetAndSend(b *testing.B) {
	payload := make([]string, 20)
	for i := range payload {
		payload[i] = string(make([]byte, 1<<15))
	}
	encoded, _ := json.Marshal(payload)
	raw := string(encoded)
	decode := func(s string) ([]string, error) { var v []string; err := json.Unmarshal([]byte(s), &v); return v, err }
	b.Run("separate", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := decode(raw); err != nil {
				b.Fatal(err)
			}
			if _, err := decode(raw); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("shared", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ctx := WithScope(context.Background())
			if _, err := ReadState(ctx, raw, decode); err != nil {
				b.Fatal(err)
			}
			if _, err := TakeState(ctx, raw, decode); err != nil {
				b.Fatal(err)
			}
		}
	})
}
