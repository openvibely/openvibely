package displayindex

import "testing"

func TestMayStartPlan(t *testing.T) {
	for _, text := range []string{"a < b", "<div>HTML</div>", "<proposed_other>", "<proposed_planx", "plain text"} {
		if mayStartPlan(text) {
			t.Errorf("ordinary text detected as plan: %q", text)
		}
	}
	const tag = "<proposed_plan>"
	for n := 1; n <= len(tag); n++ {
		if !mayStartPlan("intro" + tag[:n]) {
			t.Errorf("missed split at %d", n)
		}
	}
}
