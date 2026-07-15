package transcript

import (
	"strings"
	"testing"
)

func TestNormalizeMarkers_CanonicalizesLegacyThinkingAndMalformedToolInvoke(t *testing.T) {
	for _, opener := range []string{`[Using tool: bash"]`, `[Using tool: bash">`} {
		input := "[Thinking]\nreviewing\n</thinking>Normal text\n" + opener + "\n<parameter name=\"command\">echo one\n echo two</parameter>\n</invoke>done"
		got := NormalizeMarkers(input)

		if strings.Contains(got, "</thinking>") || strings.Contains(got, "<parameter") || strings.Contains(got, "</invoke>") || strings.Contains(got, `bash\"]`) || strings.Contains(got, `bash\">`) {
			t.Fatalf("expected legacy markers removed, got: %q", got)
		}
		if !strings.Contains(got, "[Thinking]\nreviewing\n\n[/Thinking]\nNormal text") {
			t.Fatalf("expected thinking close tag canonicalized before normal text, got: %q", got)
		}
		if !strings.Contains(got, "[Using tool: bash | echo one  echo two]") {
			t.Fatalf("expected malformed tool invocation canonicalized, got: %q", got)
		}
	}
}

func TestNormalizeMarkers_PreservesMarkdownCodeAliasesAndNormalizesRealAliases(t *testing.T) {
	codedTool := `[Using tool: bash"> <parameter name="command">echo coded</parameter> </invoke>`
	realTool := `[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`
	input := "Inline `\u003cthinking\u003ecoded\u003c/thinking\u003e` and `" + codedTool + "`.\n" +
		"```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n" + codedTool + "\n```\n" +
		"~~~text\n\u003cthinking\u003etilde\u003c/thinking\u003e\n" + codedTool + "\n~~~\n" +
		"\u003cthinking\u003ereal thinking\u003c/thinking\u003e\n" + realTool

	got := NormalizeMarkers(input)

	for _, literal := range []string{
		"`\u003cthinking\u003ecoded\u003c/thinking\u003e`",
		"`" + codedTool + "`",
		"```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n" + codedTool + "\n```",
		"~~~text\n\u003cthinking\u003etilde\u003c/thinking\u003e\n" + codedTool + "\n~~~",
	} {
		if !strings.Contains(got, literal) {
			t.Fatalf("expected coded alias preserved byte-for-byte %q in:\n%q", literal, got)
		}
	}
	if !strings.Contains(got, "[Thinking]\nreal thinking\n[/Thinking]") {
		t.Fatalf("expected real thinking alias canonicalized, got: %q", got)
	}
	if !strings.Contains(got, "[Using tool: bash | echo real]") {
		t.Fatalf("expected real tool alias canonicalized, got: %q", got)
	}
}

func TestNormalizeMarkers_UsesMatchingFenceCharacterAndLength(t *testing.T) {
	input := "`````text\n\u003cthinking\u003elong fence\u003c/thinking\u003e\n```\n" +
		"~~~\n\u003cthinking\u003estill long fence\u003c/thinking\u003e\n`````\n" +
		"\u003cthinking\u003ereal\u003c/thinking\u003e"

	got := NormalizeMarkers(input)

	for _, literal := range []string{
		"\u003cthinking\u003elong fence\u003c/thinking\u003e",
		"\u003cthinking\u003estill long fence\u003c/thinking\u003e",
	} {
		if !strings.Contains(got, literal) {
			t.Fatalf("mismatched or short fence exposed coded alias %q in: %q", literal, got)
		}
	}
	if !strings.Contains(got, "[Thinking]\nreal\n[/Thinking]") {
		t.Fatalf("real alias after matching closer was not normalized: %q", got)
	}
}

func TestNormalizeMarkers_RecognizesAllCommonMarkFenceLineEndings(t *testing.T) {
	tests := []struct {
		name  string
		coded string
	}{
		{
			name:  "bare CR backtick fence",
			coded: "```text\r<thinking>coded backtick</thinking>\r```",
		},
		{
			name:  "bare CR tilde fence",
			coded: "~~~text\r<thinking>coded tilde</thinking>\r~~~~\t ",
		},
		{
			name:  "bare CR long fence ignores short closer",
			coded: "`````text\r<thinking>coded long</thinking>\r```\r``````   ",
		},
		{
			name:  "CRLF fence",
			coded: "```text\r\n<thinking>coded CRLF</thinking>\r\n```",
		},
		{
			name:  "LF fence",
			coded: "```text\n<thinking>coded LF</thinking>\n```",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.coded + "\r<thinking>real</thinking>"
			got := NormalizeMarkers(input)
			if !strings.Contains(got, tt.coded) {
				t.Fatalf("coded alias changed across line-ending-aware fence:\n%q", got)
			}
			if !strings.Contains(got, "[Thinking]\nreal\n[/Thinking]") {
				t.Fatalf("real alias after valid closer was not normalized:\n%q", got)
			}
		})
	}
}

func TestNormalizeMarkers_FenceCloserAllowsOnlyASCIISpaceAndTab(t *testing.T) {
	tests := []struct {
		name  string
		input string
		coded string
	}{
		{
			name:  "backtick NBSP is not trailing fence whitespace",
			input: "```text\n<thinking>first</thinking>\n```\u00a0\n<thinking>coded</thinking>\n```\n<thinking>real</thinking>",
			coded: "<thinking>coded</thinking>",
		},
		{
			name:  "tilde em space is not trailing fence whitespace",
			input: "~~~text\n<thinking>first</thinking>\n~~~\u2003\n<thinking>coded</thinking>\n~~~~\t \n<thinking>real</thinking>",
			coded: "<thinking>coded</thinking>",
		},
		{
			name:  "long fence narrow NBSP is not trailing fence whitespace",
			input: "`````text\n<thinking>first</thinking>\n`````\u202f\n<thinking>coded</thinking>\n``````   \n<thinking>real</thinking>",
			coded: "<thinking>coded</thinking>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMarkers(tt.input)
			if !strings.Contains(got, tt.coded) {
				t.Fatalf("Unicode-whitespace pseudo-closer exposed coded alias: %q", got)
			}
			if !strings.Contains(got, "[Thinking]\nreal\n[/Thinking]") {
				t.Fatalf("valid ASCII space/tab closer did not expose later real alias: %q", got)
			}
		})
	}
}

func TestNormalizeMarkers_ProtectedPartialToolAliasCannotMaskLaterRealAlias(t *testing.T) {
	protected := "`[Using tool: bash\"> <parameter name=\"command\">coded`"
	input := "Literal " + protected + " before real alias.\n" +
		`[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`

	got := NormalizeMarkers(input)

	if !strings.Contains(got, protected) {
		t.Fatalf("expected protected partial alias preserved, got: %q", got)
	}
	if !strings.Contains(got, "[Using tool: bash | echo real]") {
		t.Fatalf("expected later real alias canonicalized, got: %q", got)
	}
}

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

func TestNormalizeMarkers_PreservesMultilineInlineCodeAliasesAndNormalizesLaterRealAliases(t *testing.T) {
	codedThinking := "`<thinking>coded\nthought</thinking>`"
	codedTool := "``[Using tool: bash\">\n<parameter name=\"command\">echo coded</parameter>\n</invoke>``"
	partialTool := "`[Using tool: bash\">\n<parameter name=\"command\">partial`"
	realTool := `[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`
	input := codedThinking + "\n" + codedTool + "\n" + partialTool + "\n" +
		"<thinking>real thinking</thinking>\n" + realTool

	got := NormalizeMarkers(input)
	for _, literal := range []string{codedThinking, codedTool, partialTool} {
		if !strings.Contains(got, literal) {
			t.Fatalf("expected multiline coded alias preserved byte-for-byte %q in:\n%q", literal, got)
		}
	}
	if !strings.Contains(got, "[Thinking]\nreal thinking\n[/Thinking]") || !strings.Contains(got, "[Using tool: bash | echo real]") {
		t.Fatalf("expected real aliases after multiline spans canonicalized: %q", got)
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

func TestNormalizeMarkers_ReconsidersLaterSpanAfterUnmatchedDelimiter(t *testing.T) {
	coded := "`<thinking>coded\nthought</thinking>`"
	input := "Unmatched `` prefix; " + coded + " then <thinking>real</thinking>."

	got := NormalizeMarkers(input)
	if !strings.Contains(got, "Unmatched `` prefix; "+coded) {
		t.Fatalf("later coded alias changed after unmatched delimiter: %q", got)
	}
	if !strings.Contains(got, "[Thinking]\nreal\n[/Thinking]") {
		t.Fatalf("real alias after coded span was not normalized: %q", got)
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

func TestNormalizeMarkers_DoesNotProtectAliasesBehindEscapedDelimiters(t *testing.T) {
	coded := "``<thinking>coded</thinking>``"
	input := `Escaped \` + "`" + `<thinking>first real</thinking> escaped \` + "`" + `. ` + coded +
		` then <thinking>third real</thinking>.`

	got := NormalizeMarkers(input)
	if !strings.Contains(got, coded) {
		t.Fatalf("valid coded alias after escaped candidates changed: %q", got)
	}
	for _, real := range []string{"first real", "third real"} {
		if !strings.Contains(got, "[Thinking]\n"+real+"\n[/Thinking]") {
			t.Fatalf("real alias %q was falsely protected: %q", real, got)
		}
	}
}

func TestNormalizeMarkers_LeavesIncompleteMalformedToolInvokeForNextDelta(t *testing.T) {
	input := "before\n[Using tool: bash\">\n<parameter name=\"command\">go test ./internal/..."
	got := NormalizeMarkers(input)

	if got != input {
		t.Fatalf("expected incomplete malformed tool invocation left intact for future stream deltas, got: %q", got)
	}
}
