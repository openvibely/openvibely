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
