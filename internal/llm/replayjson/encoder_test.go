package replayjson

import (
	"encoding/json"
	"errors"
	"testing"
)

type failingJSON struct{ err error }

func (v failingJSON) MarshalJSON() ([]byte, error) { return nil, v.err }

func TestEncodePreservesJSONAndErrors(t *testing.T) {
	for _, value := range []any{nil, []string{}, map[string]any{"text": "<>&\n\u2028", "raw": json.RawMessage(` { "value": 1 } `)}} {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Encode(value)
		if err != nil || got != string(want) {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	failure := errors.New("encoding failed")
	if _, err := Encode(failingJSON{failure}); !errors.Is(err, failure) {
		t.Fatalf("lost encoding error: %v", err)
	}
}
