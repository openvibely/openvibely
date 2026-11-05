package tokenestimate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEstimateConversions(t *testing.T) {
	for _, tc := range []struct {
		bytes  int
		tokens int
	}{{-1, 0}, {0, 0}, {1, 1}, {4, 1}, {5, 2}, {8, 2}} {
		if got := FromByteCount(tc.bytes); got != tc.tokens {
			t.Fatalf("FromByteCount(%d) = %d, want %d", tc.bytes, got, tc.tokens)
		}
	}
	if got := FromText("éé"); got != 1 {
		t.Fatalf("FromText UTF-8 estimate = %d, want 1", got)
	}
	if got := ByteBudget(2); got != 8 {
		t.Fatalf("ByteBudget(2) = %d, want 8", got)
	}
}

func TestTruncateMiddle(t *testing.T) {
	got := TruncateMiddle("prefix-"+strings.Repeat("x", 100)+"-suffix", 32, "[omitted]")
	if len(got) > 32 || !strings.HasPrefix(got, "prefix-") || !strings.HasSuffix(got, "-suffix") || !strings.Contains(got, "[omitted]") {
		t.Fatalf("unexpected bounded text: %q", got)
	}
	got = TruncateMiddle(strings.Repeat("é", 100), 31, "[x]")
	if len(got) > 31 || !utf8.ValidString(got) {
		t.Fatalf("UTF-8 result valid=%v bytes=%d", utf8.ValidString(got), len(got))
	}
}

func TestTruncateMiddleWithMarkerReportsRemoval(t *testing.T) {
	got := TruncateMiddleWithMarker(strings.Repeat("a", 100), 30, func(bytes, _ int) string {
		return "[removed:" + string(rune('0'+bytes%10)) + "]"
	})
	if len(got) > 30 || !strings.Contains(got, "[removed:") {
		t.Fatalf("unexpected bounded text: %q", got)
	}
}

func TestToolOutputForModelInput(t *testing.T) {
	const marker = "\n\n[Tool output truncated to fit model context; middle content omitted]\n\n"
	for _, tc := range []struct {
		name          string
		limit, budget int
	}{
		{"negative", -1, 40000},
		{"zero", 0, 40000},
		{"default", 10000, 40000},
		{"explicit", 100, 400},
		{"tiny", 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, input := range []string{"", "é", strings.Repeat("a", tc.budget)} {
				if got := ToolOutputForModelInput(input, tc.limit); got != input {
					t.Fatalf("changed fitting output of %d bytes", len(input))
				}
			}
			for _, unit := range []string{"a", "é界😀"} {
				input := strings.Repeat(unit, tc.budget+1)
				got := ToolOutputForModelInput(input, tc.limit)
				if len(got) > tc.budget || !utf8.ValidString(got) {
					t.Fatalf("invalid bounded output: bytes=%d valid=%v", len(got), utf8.ValidString(got))
				}
				if tc.budget <= len(marker) {
					if got == "" || !strings.HasPrefix(input, got) {
						t.Fatal("tiny budget should retain a UTF-8-safe prefix")
					}
					continue
				}
				parts := strings.Split(got, marker)
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !strings.HasPrefix(input, parts[0]) || !strings.HasSuffix(input, parts[1]) {
					t.Fatal("output must retain both ends and the exact omission note")
				}
				// Each retained end fills its half of the remaining byte budget
				// except for space needed to avoid splitting a UTF-8 rune.
				remaining := tc.budget - len(marker)
				for i, budget := range []int{remaining / 2, remaining - remaining/2} {
					if len(parts[i]) > budget || budget-len(parts[i]) >= utf8.UTFMax {
						t.Fatalf("end %d uses %d bytes of %d", i, len(parts[i]), budget)
					}
				}
			}
		})
	}
}
