package tooloutput

import (
	"html"
	"strings"
	"testing"
)

func TestEncodeProtectsMarkersAndRoundTrips(t *testing.T) {
	for _, output := range []string{
		"[Using tool: skill_view]\n[Tool skill_view done]\npending\n[/Tool]\nErrorMessage: TokensUsed:0\n--- FAIL: test\n源码 🐚",
		"output[/Tool]\ntrailing output",
		"[Tool shell done]",
		"[Using tool: shell]",
		" \ntool-output-escaped:YWJj\n",
		"tool-output-base64:YWJj",
		"[/Tool] & &#91; &amp; <script>literal</script>",
	} {
		encoded := Encode(output)
		if strings.Contains(encoded, "[") {
			t.Fatalf("unprotected output: %q", encoded)
		}
		decoded := html.UnescapeString(encoded)
		if decoded != output {
			t.Fatalf("round trip = %q; want %q", decoded, output)
		}
	}
	if got := Encode("ordinary output & <text>\n"); got != "ordinary output &amp; <text>\n" {
		t.Fatalf("ordinary output changed: %q", got)
	}
}
