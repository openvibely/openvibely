package transcript

import (
	"strings"
	"testing"
)

func TestMarkdownCodeRanges_ProtectsMultilineInlineSpansWithMatchingDelimiterRuns(t *testing.T) {
	input := "Before `single\nline [Thinking]coded[/Thinking]` after.\n" +
		"Double ``multi\nline [TASK_ID:coded]`` done.\n" +
		"CRLF `multi\r\nline [TASK_EDITED:coded]` done.\n" +
		"Mismatch ``still\nprotected ` here\nuntil exact`` then [Using tool: bash]."

	ranges := MarkdownCodeRanges(input)
	for _, literal := range []string{
		"`single\nline [Thinking]coded[/Thinking]`",
		"``multi\nline [TASK_ID:coded]``",
		"`multi\r\nline [TASK_EDITED:coded]`",
		"``still\nprotected ` here\nuntil exact``",
	} {
		start := strings.Index(input, literal)
		if start == -1 || !PositionInMarkdownCode(ranges, start+1) || !PositionInMarkdownCode(ranges, start+len(literal)-2) {
			t.Fatalf("multiline inline span was not fully protected %q in ranges %#v", literal, ranges)
		}
	}
	if real := strings.LastIndex(input, "[Using tool: bash]"); real == -1 || PositionInMarkdownCode(ranges, real) {
		t.Fatalf("real control after matching multiline closer was incorrectly protected: %#v", ranges)
	}
}

func TestMarkdownCodeRanges_ReconsidersRunsAfterUnmatchedDelimiter(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		literal string
		real    string
	}{
		{
			name:    "unmatched double before valid single span",
			input:   "Unmatched `` prefix; `[Thinking]coded\n[/Thinking]` then [Using tool: bash].",
			literal: "`[Thinking]coded\n[/Thinking]`",
			real:    "[Using tool: bash]",
		},
		{
			name:    "unmatched single before valid double span",
			input:   "Unmatched ` prefix; ``[TASK_ID:coded]\n[TASK_EDITED:coded]`` then [STATUS: SUCCESS].",
			literal: "``[TASK_ID:coded]\n[TASK_EDITED:coded]``",
			real:    "[STATUS: SUCCESS]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges := MarkdownCodeRanges(tt.input)
			start := strings.Index(tt.input, tt.literal)
			if start == -1 || !PositionInMarkdownCode(ranges, start+1) || !PositionInMarkdownCode(ranges, start+len(tt.literal)-2) {
				t.Fatalf("later valid span was not protected after unmatched delimiter %q: %#v", tt.literal, ranges)
			}
			real := strings.LastIndex(tt.input, tt.real)
			if real == -1 || PositionInMarkdownCode(ranges, real) {
				t.Fatalf("real control after later valid span was incorrectly protected: %#v", ranges)
			}
		})
	}
}

func TestMarkdownCodeRanges_RespectsBackslashEscapedDelimiterRuns(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		protected string
		real      string
	}{
		{
			name:  "odd backslash escapes single opener",
			input: `Escaped \` + "`" + `[Thinking]real[/Thinking] escaped \` + "`" + `.`,
			real:  "[Thinking]real[/Thinking]",
		},
		{
			name:      "even backslashes leave single opener active",
			input:     `Even \\` + "`" + `[Thinking]coded[/Thinking]` + "`" + ` then [Using tool: bash].`,
			protected: "[Thinking]coded[/Thinking]",
			real:      "[Using tool: bash]",
		},
		{
			name:  "odd backslash escapes first backtick of multiple run",
			input: `Escaped \` + "``" + `[Thinking]real[/Thinking]` + "``" + `.`,
			real:  "[Thinking]real[/Thinking]",
		},
		{
			name:      "even backslashes leave multiple opener active",
			input:     `Even \\` + "``" + `[TASK_ID:coded]` + "``" + ` then [TASK_ID:real].`,
			protected: "[TASK_ID:coded]",
			real:      "[TASK_ID:real]",
		},
		{
			name:      "odd backslash escapes only first backtick of multiple run",
			input:     `Escaped \` + "``" + `[Thinking]coded[/Thinking]` + "`" + ` then [Using tool: bash].`,
			protected: "[Thinking]coded[/Thinking]",
			real:      "[Using tool: bash]",
		},
		{
			name:  "three backslashes escape single opener",
			input: "Odd " + strings.Repeat("\\", 3) + "`[Thinking]real[/Thinking]" + strings.Repeat("\\", 3) + "`.",
			real:  "[Thinking]real[/Thinking]",
		},
		{
			name:      "four backslashes leave single opener active",
			input:     "Even " + strings.Repeat("\\", 4) + "`[Thinking]coded[/Thinking]` then [Using tool: bash].",
			protected: "[Thinking]coded[/Thinking]",
			real:      "[Using tool: bash]",
		},
		{
			name:      "escaped candidate does not mask later valid span",
			input:     `Escaped \` + "`" + ` prefix; ` + "``" + `[TASK_EDITED:coded]` + "``" + ` then [STATUS: SUCCESS].`,
			protected: "[TASK_EDITED:coded]",
			real:      "[STATUS: SUCCESS]",
		},
		{
			name:      "backslash inside code does not escape closer",
			input:     "`[Thinking]coded[/Thinking]\\` then [Thinking]real[/Thinking].",
			protected: "[Thinking]coded[/Thinking]",
			real:      "[Thinking]real[/Thinking]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ranges := MarkdownCodeRanges(tt.input)
			if tt.protected != "" {
				position := strings.Index(tt.input, tt.protected)
				if position == -1 || !PositionInMarkdownCode(ranges, position) {
					t.Fatalf("expected %q protected in %#v", tt.protected, ranges)
				}
			}
			if tt.real != "" {
				position := strings.LastIndex(tt.input, tt.real)
				if position == -1 || PositionInMarkdownCode(ranges, position) {
					t.Fatalf("expected %q outside code in %#v", tt.real, ranges)
				}
			}
		})
	}
}

func TestUnclosedMarkdownFence(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"```go\npartial", "```"},
		{"   ~~~~go\r\npartial", "~~~~"},
		{"`````go\n```\npartial", "`````"},
		{"```go\ncode\n```", ""},
		{"~~~\rpartial\r~~~", ""},
		{"    ```\nindented", ""},
		{"`inline`", ""},
	} {
		if got := UnclosedMarkdownFence(tc.text); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.text, got, tc.want)
		}
	}
}

// These fence checks used to be exercised through XML marker conversion.
// Keep the Markdown coverage independent of any transcript transformation.
func TestMarkdownCodeRanges_FenceBoundaries(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, test := range []struct{ name, opener, misleading, closer string }{
			{"short backticks", "`````text", "```", "`````"},
			{"wrong character", "```text", "~~~", "```"},
			{"short tildes", "~~~~~text", "~~~", "~~~~~~"},
			{"unicode suffix", "```text", "```\u00a0", "```\t "},
			{"non whitespace suffix", "~~~text", "~~~ suffix", "~~~ "},
		} {
			t.Run(test.name+"/"+strings.ReplaceAll(strings.ReplaceAll(ending, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
				input := strings.Join([]string{test.opener, test.misleading, "[Thinking]coded[/Thinking]", test.closer, "[Thinking]real[/Thinking]"}, ending)
				ranges := MarkdownCodeRanges(input)
				if !PositionInMarkdownCode(ranges, strings.Index(input, "[Thinking]coded")) {
					t.Fatal("invalid closer ended the code fence")
				}
				if PositionInMarkdownCode(ranges, strings.Index(input, "[Thinking]real")) {
					t.Fatal("matching closer did not end the code fence")
				}
			})
		}
	}
}
