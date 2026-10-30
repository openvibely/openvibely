package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var legacyThinkingClose = regexp.MustCompile(`(?m)^[\t ]*\[/Thinking\][\t ]*\r?(?:\n|$)`)
var legacyToolClose = regexp.MustCompile(`(?m)^[\t ]*\[/Tool\][\t ]*\r?(?:\n|$)`)

func TestFindCloseLineMatchesRegexp(t *testing.T) {
	inputs := []string{
		"", "[/Tool]", "[/Tool]\n", "x[/Tool]\n[/Tool]", " \t[/Tool] \t\r\nrest", "[/Tool]x\n[/Tool]\r",
		"a\n  [/Tool]  \nb", "[/Tool]\r\r\n[/Tool]\n", "a\n[/Tool] y\n\t[/Tool]", "\n\n[/Tool]",
		"[/Thinking]\n", "text\n [/Thinking]\t\nmore", "x [/Thinking]\n", "[/Thinking]\r", "é\n[/Thinking]",
		"a\r\n[/Tool]\r\n", "[/Tool][/Tool]\n", "\t\t\n[/Tool]", "abc [/Tool]\n  [/Tool]",
	}
	for _, in := range inputs {
		for marker, re := range map[string]*regexp.Regexp{"[/Tool]": legacyToolClose, "[/Thinking]": legacyThinkingClose} {
			want := re.FindStringIndex(in)
			got := findCloseLine(in, marker)
			if fmt.Sprint(want) != fmt.Sprint(got) {
				t.Errorf("%q %s: got %v want %v", in, marker, got, want)
			}
		}
	}
}

func legacyDisplayRecords(items []DisplayItem) [][]byte {
	var out [][]byte
	for _, item := range items {
		raw, _ := json.Marshal(item)
		out = append(out, raw)
	}
	return out
}

func sampleDisplayOutput() string {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "Some text <b>&</b> \u2028 line %d\n[Thinking]\nthinking %d\n[/Thinking]\n[Using tool: read_file | path/%d]\n[Tool read_file done]\n%s\n[/Tool]\n", i, i, i, strings.Repeat("code line \"quoted\"\n", 50))
	}
	b.WriteString("[Compaction done | 12]\n\xff tail")
	return b.String()
}

func TestDisplayRecordsMatchesMarshal(t *testing.T) {
	items := ParseDisplay(sampleDisplayOutput())
	items = append(items, DisplayItem{Type: "text", Text: "bad \xff utf8 \x00"})
	records, err := DisplayRecords(items)
	if err != nil {
		t.Fatal(err)
	}
	want := legacyDisplayRecords(items)
	for i := range want {
		if !bytes.Equal(records[i].Payload, want[i]) {
			t.Fatalf("item %d: got %s want %s", i, records[i].Payload, want[i])
		}
	}
}

func BenchmarkParseDisplayRecords(b *testing.B) {
	out := sampleDisplayOutput()
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DisplayRecords(ParseDisplay(out)); err != nil {
			b.Fatal(err)
		}
	}
}

func TestFindDisplayControlsMatchesRegexp(t *testing.T) {
	inputs := []string{
		"", "[Thinking]", "[Thinking]\n", " \t[Thinking] \r\nx", "a[Thinking]\n[Thinking]\n",
		"[Using tool: a | b]\n[Tool a done]\nout\n[/Tool]\n", "[Tool a error]\r", "[Tool a done]\r\r\n",
		"[Compaction started]\n[Compaction done | 5]\n[Compaction failed]", "[x] [Thinking]\n",
		"\n\n  [Using tool: x]  \n", "[Using tool: ]\n", "[[Thinking]\n", "é [Thinking]\n[Thinking]",
		"[Thinking]\n[Thinking]\n", "line\r\n[Thinking]\r\n", sampleDisplayOutput(),
	}
	for _, in := range inputs {
		want := displayControl.FindAllStringSubmatchIndex(in, -1)
		got := findDisplayControls(in)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}
