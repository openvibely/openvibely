package tokenestimate

import "testing"

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
