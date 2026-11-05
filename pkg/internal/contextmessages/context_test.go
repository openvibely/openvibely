package contextmessages

import (
	"reflect"
	"testing"
)

type message struct{ role, text string }

func read(m message) (string, string) { return m.role, m.text }
func matches(role, text string) bool  { return role == "user" && text == "context" }

func TestInsertUpdateReplayBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		visible []message
		replay  []string
		want    []string
	}{
		{"empty history", nil, nil, []string{"update"}},
		{"empty replay with user tail", []message{{"user", "question"}}, nil, []string{"update"}},
		{"empty visible", nil, []string{"question"}, []string{"question", "update"}},
		{"user request", []message{{"assistant", "prior"}, {"user", "question"}}, []string{"prefix", "question"}, []string{"prefix", "update", "question"}},
		{"context tail", []message{{"user", "context"}}, []string{"context"}, []string{"context", "update"}},
		{"structured input", []message{{"user", ""}}, []string{"question"}, []string{"question", "update"}},
		{"tool exchange", []message{{"assistant", "call"}, {"tool", "result"}}, []string{"call", "result"}, []string{"call", "result", "update"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Spare capacity catches accidental overwrites of the caller's slice.
			backing := make([]string, len(tc.replay)+2)
			copy(backing, tc.replay)
			backing[len(tc.replay)] = "sentinel"
			before := append([]string(nil), backing...)
			replay := backing[:len(tc.replay)]
			makeMessage := func(s string) string { return s }
			got := InsertUpdate(tc.visible, replay, read, matches, "update", makeMessage)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(backing, before) {
				t.Fatal("insertion modified replay backing storage")
			}
			unchanged := InsertUpdate(tc.visible, replay, read, matches, "", func(string) string {
				t.Fatal("constructed message for suppressed update")
				return ""
			})
			if !reflect.DeepEqual(unchanged, replay) {
				t.Fatal("suppressed update changed replay")
			}
		})
	}
}
