package jsonview

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBorrowedStringSizes(t *testing.T) {
	for _, raw := range []string{`""`, `"literal <>&世"`, `"\u0061\u003c\u2028"`, `"\ud83d\ude00"`, `"\ud800\u0061"`, `"\n\t\/\\\""`, ` "space" `, string([]byte{'"', 0xff, '"'})} {
		var text string
		if err := json.Unmarshal([]byte(raw), &text); err != nil {
			t.Fatal(err)
		}
		v, err := Borrow([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		n, err := v.DecodedStringBytes()
		if err != nil || n != len(text) {
			t.Fatalf("%q bytes=%d want=%d err=%v", raw, n, len(text), err)
		}
		encoded, _ := json.Marshal(text)
		n, err = v.StringSize()
		if err != nil || n != len(encoded) {
			t.Fatalf("%q encoded=%d want=%d err=%v", raw, n, len(encoded), err)
		}
	}
}
func FuzzBorrowedStringSizes(f *testing.F) {
	for _, s := range []string{"abc", "世😀<>&\n", "\x00\xff", "\\\""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		raw, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Borrow(raw)
		if err != nil {
			t.Fatal(err)
		}
		var decoded string
		_ = json.Unmarshal(raw, &decoded)
		n, err := v.DecodedStringBytes()
		if err != nil || n != len(decoded) {
			t.Fatal(n, len(decoded), err)
		}
		n, err = v.StringSize()
		if err != nil || n != len(raw) {
			t.Fatal(n, len(raw), err)
		}
	})
}
func TestBorrowedProjectionAndMetadata(t *testing.T) {
	raw := json.RawMessage(`{"type":"function_call_output","output":"large result","call_id":"c"}`)
	item, err := ToolOutput(raw)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := item.(map[string]any)["output"].(Value)
	if !ok {
		t.Fatal("output decoded")
	}
	if string(output.Raw()) != `"large result"` {
		t.Fatal(string(output.Raw()))
	}
	var text string
	_ = output.Decode(&text)
	if text != "large result" {
		t.Fatal(text)
	}
	got, ok := FindString([]byte(`{"child":{"type":"nested"},"type":"root","huge":"`+strings.Repeat("x", 10000)+`"}`), "type")
	if !ok || got != "root" {
		t.Fatal(got, ok)
	}
}

func TestBorrowedStringSizeDoesNotAllocatePerSpan(t *testing.T) {
	raw, err := json.Marshal(strings.Repeat("long text span with unicode 世 and an escape\n", 1024))
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(20, func() {
		view, err := Borrow(raw)
		if err != nil {
			t.Fatal(err)
		}
		size, err := view.StringSize()
		if err != nil || size != len(raw) {
			t.Fatalf("size=%d want=%d err=%v", size, len(raw), err)
		}
	})
	if allocs > 2 {
		t.Fatalf("counting 1024 text spans allocated %.0f objects; expected bounded overhead", allocs)
	}
}

func TestStringBytesSizeMatchesEncoder(t *testing.T) {
	for _, text := range []string{"", "abc", "世😀<>&\n\t\"\\", "\u2028\u2029", "\x00\xff"} {
		raw, err := json.Marshal(text)
		if err != nil {
			t.Fatal(err)
		}
		if n := StringBytesSize([]byte(text)); n != len(raw) {
			t.Fatalf("%q: got %d want %d", text, n, len(raw))
		}
	}
}
