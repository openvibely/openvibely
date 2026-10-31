package preparedhistory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRowsReuseInvalidationAndTransfer(t *testing.T) {
	ctx := WithScope(context.Background())
	rows := []json.RawMessage{json.RawMessage(`{"content":"first"}`)}
	calls := 0
	decode := func(r []json.RawMessage) ([]string, error) { calls++; return []string{string(r[0])}, nil }
	_, _ = ReadRows(ctx, rows, decode)
	_, _ = ReadRows(ctx, rows, decode)
	if calls != 1 {
		t.Fatal("same history rebuilt", calls)
	}
	rows[0] = json.RawMessage(`{"content":"summary"}`)
	_, _ = ReadRows(ctx, rows, decode)
	got, _ := TakeRows(ctx, rows, decode)
	if calls != 2 || got[0] != string(rows[0]) {
		t.Fatal("stale summary", calls, got)
	}
	got[0] = "sender mutation"
	again, _ := TakeRows(ctx, rows, decode)
	if calls != 3 || again[0] != string(rows[0]) {
		t.Fatal("ownership retained", calls, again)
	}
}
func TestRowsFailedPreparationDoesNotCache(t *testing.T) {
	ctx := WithScope(context.Background())
	calls := 0
	decode := func([]json.RawMessage) (int, error) { calls++; return 0, errors.New("invalid") }
	for i := 0; i < 2; i++ {
		if _, err := ReadRows(ctx, []json.RawMessage{json.RawMessage(`!`)}, decode); err == nil {
			t.Fatal("accepted invalid row")
		}
	}
	if calls != 2 {
		t.Fatal("cached failure")
	}
}
