package normalize

import "testing"

func TestNormalizeToolCallIDsInText(t *testing.T) {
	in := `{"call_id":"call_abc|xyz:123","tool_use_id":"  u-1/2  "}`
	out := NormalizeToolCallIDsInText(in)
	if out == in {
		t.Fatalf("expected normalization to change text")
	}
	if !contains(out, `"call_id":"call_abc_xyz_123"`) {
		t.Fatalf("expected normalized call_id, got %s", out)
	}
	if !contains(out, `"tool_use_id":"u-1_2"`) {
		t.Fatalf("expected normalized tool_use_id, got %s", out)
	}
}

func TestNormalizeReplayOutputText_StripsNullAndNormalizesIDs(t *testing.T) {
	in := "abc\x00def {\"tool_call_id\":\"a|b\"}"
	out := NormalizeReplayOutputText(in)
	if out == in {
		t.Fatalf("expected replay normalization to change text")
	}
	if contains(out, "\x00") {
		t.Fatalf("expected null bytes stripped, got %q", out)
	}
	if !contains(out, `"tool_call_id":"a_b"`) {
		t.Fatalf("expected normalized tool_call_id, got %s", out)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (indexOf(s, sub) >= 0))) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
