package replayjson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type counted struct {
	Text  string
	Calls *int
	Fail  bool
}

func (v counted) MarshalJSON() ([]byte, error) {
	*v.Calls++
	if v.Fail {
		return nil, errors.New("encode failure")
	}
	return json.Marshal(v.Text)
}
func TestEncoderReuseAndChanges(t *testing.T) {
	calls := 0
	e := Encoder[counted]{Snapshot: func(v counted) (counted, bool) { return v, true }}
	v := counted{Text: "first", Calls: &calls}
	first, err := e.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Encode(v)
	if err != nil || first != again || calls != 1 {
		t.Fatalf("reuse: %q %v calls=%d", again, err, calls)
	}
	v.Text = "second"
	second, err := e.Encode(v)
	if err != nil || first == second || calls != 2 {
		t.Fatalf("change: %q %v calls=%d", second, err, calls)
	}
	v.Fail = true
	for i := 0; i < 2; i++ {
		if _, err = e.Encode(v); err == nil {
			t.Fatal("encoding error lost")
		}
	}
	if calls != 4 {
		t.Fatal("failed encoding reused")
	}
}
func BenchmarkFinalSnapshot(b *testing.B) {
	value := strings.Repeat("conversation history <>&\n", 65536)
	for _, reuse := range []bool{false, true} {
		name := "encode-again"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			e := Encoder[string]{Snapshot: func(v string) (string, bool) { return v, true }}
			if _, err := e.Encode(value); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if reuse {
					_, err = e.Encode(value)
				} else {
					_, err = Encode(value)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
