package jsonview

import (
	"encoding/json"
	"testing"
	"unsafe"
)

func TestBorrowedFieldsAndDuplicateOrder(t *testing.T) {
	raw := []byte(` {"content": "old", "CONTENT": [{"text":"é"}], "extra": true} `)
	var got json.RawMessage
	if err := Object(raw, func(k string, v json.RawMessage) error {
		if k == "CONTENT" {
			got = v
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(got) != `[{"text":"é"}]` {
		t.Fatalf("bad view %s", got)
	}
	base := uintptr(unsafe.Pointer(&raw[0]))
	ptr := uintptr(unsafe.Pointer(&got[0]))
	if ptr < base || ptr >= base+uintptr(len(raw)) {
		t.Fatal("copied value")
	}
	rows, err := Array(got)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	content, err := Content(raw)
	if err != nil || string(content) != string(got) {
		t.Fatal(string(content), err)
	}
	for _, raw := range []string{`{"content":1} junk`, `[]`, `{"content":`} {
		if _, err := Content([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestEncodedSizeMatchesMarshal(t *testing.T) {
	for _, raw := range []string{` { "a": "<>&", "n":1.20 } `, `"é\u003c"`, `"a\\\"b"`, "\"\u2028\u2029\"", `[ null, true, {"a":1}]`, `invalid`, "\"\xff\""} {
		expected, err := json.Marshal(json.RawMessage(raw))
		n, e := EncodedSize([]byte(raw))
		if (err == nil) != (e == nil) || err == nil && n != len(expected) {
			t.Fatalf("%q: %d != %d; %v/%v (%s)", raw, n, len(expected), e, err, expected)
		}
	}
}

func FuzzEncodedSize(f *testing.F) {
	for _, seed := range []string{`{"x":"<>&"}`, `{"x":[null,1.20]}`, `"é"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, err := json.Marshal(json.RawMessage(s))
		got, e := EncodedSize([]byte(s))
		if (err == nil) != (e == nil) || err == nil && got != len(want) {
			t.Fatalf("%q: %d != %d %v/%v", s, got, len(want), e, err)
		}
	})
}
