package output

import (
	"regexp"
	"testing"
)

func TestNoMatchCleanupPreservesMarkdown(t *testing.T) {
	text := "before\r\n```text\r\nquoted <tag>\r\n```\r\n`inline` é"
	if got := ReplaceOutsideMarkdownCode(text, regexp.MustCompile(`\[absent\]`), ""); got != text {
		t.Fatalf("changed marker-free text: %q", got)
	}
	if got := CleanChatOutput(text); got != text {
		t.Fatalf("changed code: %q", got)
	}
	if got := DeduplicateTaskSummaries(text); got != text {
		t.Fatalf("changed plain summary: %q", got)
	}
}
func TestThinkingGuardStillProtectsLiteralAndRemovesRealControls(t *testing.T) {
	literal := "```text\n[Thinking]quoted[/Thinking]\n```"
	if got := CleanChatOutput(literal + "\n[Thinking]secret[/Thinking]\nanswer"); got != literal+"\n\nanswer" {
		t.Fatalf("%q", got)
	}
}
