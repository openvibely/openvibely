package output

import (
	"strings"
	"testing"
)

func TestIsImageMediaType(t *testing.T) {
	tests := []struct {
		mediaType string
		expected  bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", true},
		{"text/plain", false},
		{"application/json", false},
		{"application/octet-stream", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.mediaType, func(t *testing.T) {
			result := IsImageMediaType(tc.mediaType)
			if result != tc.expected {
				t.Errorf("IsImageMediaType(%q) = %v, want %v", tc.mediaType, result, tc.expected)
			}
		})
	}
}

func TestExtractMarker(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		prefix     string
		wantReason string
		wantFound  bool
	}{
		{
			name:       "status failed marker",
			output:     "I tried running fail.sh but it exited with code 1.\n\n[STATUS: FAILED | fail.sh exited with non-zero code 1]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "fail.sh exited with non-zero code 1",
			wantFound:  true,
		},
		{
			name:       "needs followup marker",
			output:     "Tests pass but coverage dropped.\n\n[STATUS: NEEDS_FOLLOWUP | test coverage decreased from 80% to 65%]",
			prefix:     "[STATUS: NEEDS_FOLLOWUP |",
			wantReason: "test coverage decreased from 80% to 65%",
			wantFound:  true,
		},
		{
			name:      "no marker present",
			output:    "Everything completed successfully.\n\n[STATUS: SUCCESS]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "wrong marker type",
			output:    "[STATUS: NEEDS_FOLLOWUP | check the logs]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores marker without closing bracket",
			output:    "[STATUS: FAILED | something went wrong",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores marker without a reason",
			output:    "[STATUS: FAILED | ]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores needs followup marker without closing bracket",
			output:    "[STATUS: NEEDS_FOLLOWUP | check the logs",
			prefix:    "[STATUS: NEEDS_FOLLOWUP |",
			wantFound: false,
		},
		{
			name:      "ignores marker with an extra closing bracket",
			output:    "[STATUS: FAILED | something went wrong]]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores failed marker with an extra pipe delimiter",
			output:    "[STATUS: FAILED | something went wrong | extra]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores followup marker with an extra pipe delimiter",
			output:    "[STATUS: NEEDS_FOLLOWUP | review logs | extra]",
			prefix:    "[STATUS: NEEDS_FOLLOWUP |",
			wantFound: false,
		},
		{
			name:      "ignores marker with extra spacing",
			output:    "[STATUS: FAILED  | reason]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores embedded prose marker",
			output:    "Saw [STATUS: FAILED | first error] earlier but completed successfully.",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores quoted terminal-looking marker",
			output:    "The previous response contained this literal text:\n\n> [STATUS: FAILED | ...]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores bulleted terminal-looking marker",
			output:    "Use this marker only when actually failing:\n\n- [STATUS: FAILED | reason]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores fenced marker",
			output:    "Example:\n```\n[STATUS: FAILED | example]\n```",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "ignores marker in multiline inline code",
			output:    "Example `[STATUS: FAILED | coded\nreason]`",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:       "matching multiline inline closer exposes later real marker",
			output:     "Example `[STATUS: FAILED | coded\nreason]`\n[STATUS: FAILED | real failure]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "real failure",
			wantFound:  true,
		},
		{
			name:      "unmatched delimiter does not expose later coded marker",
			output:    "Unmatched `` prefix; `[STATUS: FAILED | coded\nreason]`",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:       "unmatched delimiter and later span do not mask real marker",
			output:     "Unmatched `` prefix; `[STATUS: FAILED | coded\nreason]`\n[STATUS: FAILED | real failure]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "real failure",
			wantFound:  true,
		},
		{
			name:       "escaped backticks do not protect real marker",
			output:     "Escaped \\` prefix.\n[STATUS: FAILED | real failure]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "real failure",
			wantFound:  true,
		},
		{
			name:      "even backslashes preserve active inline code opener",
			output:    "Even \\\\`[STATUS: FAILED | coded]`",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "tilde delimiter does not close backtick fence",
			output:    "Example:\n```text\n~~~\n[STATUS: FAILED | still fenced]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "backtick delimiter does not close tilde fence",
			output:    "Example:\n~~~text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]",
			prefix:    "[STATUS: NEEDS_FOLLOWUP |",
			wantFound: false,
		},
		{
			name:      "shorter delimiter does not close longer fence",
			output:    "Example:\n`````text\n```\n[STATUS: FAILED | still fenced]",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:       "matching longer delimiter closes fence before real marker",
			output:     "Example:\n`````text\n```\n`````\n[STATUS: FAILED | real failure]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "real failure",
			wantFound:  true,
		},
		{
			name:       "bare CR matching fence closer exposes real marker",
			output:     "Example:\r`````text\r```\r``````\t \r[STATUS: FAILED | real failure]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "real failure",
			wantFound:  true,
		},
		{
			name:      "bare CR fenced marker remains inert",
			output:    "Example:\r~~~text\r[STATUS: NEEDS_FOLLOWUP | coded]\r~~~",
			prefix:    "[STATUS: NEEDS_FOLLOWUP |",
			wantFound: false,
		},
		{
			name:      "ignores final marker with trailing prose",
			output:    "[STATUS: FAILED | error] but this is explanatory text",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
		{
			name:      "empty output",
			output:    "",
			prefix:    "[STATUS: FAILED |",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, found := ExtractMarker(tt.output, tt.prefix)
			if found != tt.wantFound {
				t.Errorf("ExtractMarker found=%v, want %v", found, tt.wantFound)
			}
			if reason != tt.wantReason {
				t.Errorf("ExtractMarker reason=%q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestDetectToolFailures(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantReason string
	}{
		{name: "exit code 1", output: "Running script...\n\n[Thinking]\nThe script exited with code 1.\n", wantReason: "command exited with code 1"},
		{name: "exited with code 127", output: "Command not found. Exited with code 127", wantReason: "command exited with code 127"},
		{name: "exit status 2", output: "Error: exit status 2\nSomething went wrong", wantReason: "command exited with code 2"},
		{name: "Exit code: 1", output: "Exit code: 1", wantReason: "command exited with code 1"},
		{name: "exit code 0 is not a failure", output: "Script completed. Exit code 0", wantReason: ""},
		{name: "no exit code mentioned", output: "Everything worked great!", wantReason: ""},
		{name: "multiple failures uses last", output: "exit code 1\nthen exit code 2", wantReason: "command exited with code 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := DetectToolFailures(tt.output)
			if reason != tt.wantReason {
				t.Errorf("DetectToolFailures()=%q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestCleanChatOutput_PreservesLegacyActionMarkerText(t *testing.T) {
	tests := []string{
		"[CREATE_TASK]\n{\"title\":\"Fix bug\"}\n[/CREATE_TASK]",
		"[EDIT_TASK]\n{\"id\":\"task-1\"}\n[/EDIT_TASK]",
		"[EXECUTE_TASKS]\n{\"tags\":[\"bug\"]}\n[/EXECUTE_TASKS]",
		"[SEND_TO_TASK]\n{\"task_id\":\"task-1\",\"message\":\"hi\"}\n[/SEND_TO_TASK]",
		"[SCHEDULE_TASK]\n{\"task_id\":\"task-1\",\"time\":\"09:00\"}\n[/SCHEDULE_TASK]",
	}
	for _, input := range tests {
		if got := CleanChatOutput(input); got != input {
			t.Errorf("CleanChatOutput changed inert marker-looking prose:\n got %q\nwant %q", got, input)
		}
		if got := CleanChatOutputForDisplay(input); got != input {
			t.Errorf("CleanChatOutputForDisplay changed inert marker-looking prose:\n got %q\nwant %q", got, input)
		}
	}
}

func TestCleanChatOutput_PreservesCodedTaskResultMetadata(t *testing.T) {
	input := "Real create [TASK_ID:real-create].\n" +
		"Inline `[TASK_ID:inline-create]` and `[TASK_EDITED:inline-edit]`.\n" +
		"```text\n[TASK_ID:fenced-create]\n[TASK_EDITED:fenced-edit]\n```\n" +
		"~~~text\n[TASK_ID:tilde-create]\n[TASK_EDITED:tilde-edit]\n~~~\n" +
		"Real edit [TASK_EDITED:real-edit]."
	expected := "Real create .\n" +
		"Inline `[TASK_ID:inline-create]` and `[TASK_EDITED:inline-edit]`.\n" +
		"```text\n[TASK_ID:fenced-create]\n[TASK_EDITED:fenced-edit]\n```\n" +
		"~~~text\n[TASK_ID:tilde-create]\n[TASK_EDITED:tilde-edit]\n~~~\n" +
		"Real edit ."

	for _, cleaner := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "channel display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(cleaner.name, func(t *testing.T) {
			if got := cleaner.fn(input); got != expected {
				t.Errorf("cleaned output =\n%q\nwant:\n%q", got, expected)
			}
		})
	}
}

func TestCleanChatOutput_PreservesCodedTaskResultSummary(t *testing.T) {
	input := "Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\n" +
		"Visible answer.\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	expected := "Example:\n```text\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\n```\nVisible answer."

	if got := CleanChatOutput(input); got != expected {
		t.Errorf("CleanChatOutput() =\n%q\nwant:\n%q", got, expected)
	}
}

func TestCleanChatOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "plain text unchanged", input: "COBOL was created in 1959.", expected: "COBOL was created in 1959."},
		{name: "strips STATUS SUCCESS", input: "Here is the answer.\n\n[STATUS: SUCCESS]", expected: "Here is the answer."},
		{name: "strips STATUS FAILED with reason", input: "Could not complete.\n[STATUS: FAILED | tests failed]", expected: "Could not complete."},
		{name: "strips STATUS NEEDS_FOLLOWUP", input: "Done but check logs.\n[STATUS: NEEDS_FOLLOWUP | 3 warnings]", expected: "Done but check logs."},
		{name: "preserves incomplete STATUS FAILED", input: "Could not complete.\n[STATUS: FAILED | tests failed", expected: "Could not complete.\n[STATUS: FAILED | tests failed"},
		{name: "preserves STATUS FAILED without reason separator", input: "Could not complete.\n[STATUS: FAILED]", expected: "Could not complete.\n[STATUS: FAILED]"},
		{name: "preserves STATUS NEEDS_FOLLOWUP without reason", input: "Done but check logs.\n[STATUS: NEEDS_FOLLOWUP | ]", expected: "Done but check logs.\n[STATUS: NEEDS_FOLLOWUP | ]"},
		{name: "preserves STATUS SUCCESS with unexpected reason", input: "Done.\n[STATUS: SUCCESS | unexpected]", expected: "Done.\n[STATUS: SUCCESS | unexpected]"},
		{name: "preserves STATUS FAILED with extra pipe delimiter", input: "Could not complete.\n[STATUS: FAILED | tests failed | extra]", expected: "Could not complete.\n[STATUS: FAILED | tests failed | extra]"},
		{name: "preserves STATUS NEEDS_FOLLOWUP with extra pipe delimiter", input: "Done but check logs.\n[STATUS: NEEDS_FOLLOWUP | warnings | extra]", expected: "Done but check logs.\n[STATUS: NEEDS_FOLLOWUP | warnings | extra]"},
		{name: "preserves noncanonical status whitespace", input: "Spacing variants:\n[STATUS:  SUCCESS]", expected: "Spacing variants:\n[STATUS:  SUCCESS]"},
		{name: "preserves status followed by thinking", input: "[STATUS: SUCCESS]\n[Thinking]\nLater internal text\n[/Thinking]", expected: "[STATUS: SUCCESS]"},
		{name: "preserves status in explanatory prose", input: "The canonical completion control is [STATUS: SUCCESS] when used correctly.", expected: "The canonical completion control is [STATUS: SUCCESS] when used correctly."},
		{name: "preserves status bullet", input: "Example:\n- [STATUS: FAILED | reason]", expected: "Example:\n- [STATUS: FAILED | reason]"},
		{name: "preserves status quote", input: "Example:\n> [STATUS: NEEDS_FOLLOWUP | reason]", expected: "Example:\n> [STATUS: NEEDS_FOLLOWUP | reason]"},
		{name: "preserves status fenced example", input: "Example:\n```text\n[STATUS: SUCCESS]\n```", expected: "Example:\n```text\n[STATUS: SUCCESS]\n```"},
		{name: "preserves status after mismatched fence character", input: "Example:\n```text\n~~~\n[STATUS: FAILED | still fenced]", expected: "Example:\n```text\n~~~\n[STATUS: FAILED | still fenced]"},
		{name: "preserves status after inverse mismatched fence character", input: "Example:\n~~~text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]", expected: "Example:\n~~~text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]"},
		{name: "preserves status after shorter closing delimiter", input: "Example:\n`````text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]", expected: "Example:\n`````text\n```\n[STATUS: NEEDS_FOLLOWUP | still fenced]"},
		{name: "strips real status after matching long fence closer", input: "Example:\n`````text\n```\n`````\n[STATUS: FAILED | real failure]", expected: "Example:\n`````text\n```\n`````"},
		{name: "preserves status with trailing prose", input: "[STATUS: FAILED | reason] but this is explanatory text", expected: "[STATUS: FAILED | reason] but this is explanatory text"},
		{name: "preserves non-final standalone status line", input: "[STATUS: SUCCESS]\nMore explanation follows.", expected: "[STATUS: SUCCESS]\nMore explanation follows."},
		{name: "strips only final status when earlier inert status exists", input: "[STATUS: SUCCESS]\nMore explanation follows.\n[STATUS: FAILED | actual failure]", expected: "[STATUS: SUCCESS]\nMore explanation follows."},
		{name: "strips tool use markers", input: "Let me check.\n[Using tool: Read]\nThe file contains...", expected: "Let me check.\n\nThe file contains..."},
		{name: "strips multi_tool_use protocol artifact", input: "} to=multi_tool_use.parallel code something\nActual text.", expected: "Actual text."},
		{name: "strips bare CR multi_tool_use protocol artifact only", input: "} to=multi_tool_use.parallel code something\rActual text.", expected: "Actual text."},
		{name: "strips multi_tool_use without to= prefix", input: "multi_tool_use.parallel error here\nUseful text.", expected: "Useful text."},
		{name: "strips multi_tool_use with braces", input: "}} multi_tool_use.sequential blah\nNarrative.", expected: "Narrative."},
		{name: "strips multi_tool_use with unicode", input: "} to=multi_tool_use.parallel code 彩神争霸高json uμ? Wait malformed.\nRetrying.", expected: "Retrying."},
		{name: "preserves normal multi-tool text", input: "The multi-tool approach works well.", expected: "The multi-tool approach works well."},
		{name: "strips thinking blocks", input: "\n[Thinking]\nLet me analyze this question.\n\nCOBOL was created in 1959.", expected: "COBOL was created in 1959."},
		{name: "strips bare CR unclosed thinking block", input: "\r[Thinking]\rLet me analyze this question.\r\rCOBOL was created in 1959.\r[STATUS: SUCCESS]", expected: "COBOL was created in 1959."},
		{name: "strips legacy raw thinking close tags without eating final answer", input: "\n[Thinking]\nLet me analyze this question.\n</thinking>\nCOBOL was created in 1959.", expected: "COBOL was created in 1959."},
		{name: "empty input", input: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChatOutput(tt.input)
			if got != tt.expected {
				t.Errorf("CleanChatOutput() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestCleanChatOutputForDisplay_StatusControlsAreFinalStandaloneOnly(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips final canonical control", input: "Completed.\n[STATUS: SUCCESS]", expected: "Completed."},
		{name: "preserves failed control with extra pipe delimiter", input: "Failed text.\n[STATUS: FAILED | reason | extra]", expected: "Failed text.\n[STATUS: FAILED | reason | extra]"},
		{name: "preserves followup control with extra pipe delimiter", input: "Follow-up text.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]", expected: "Follow-up text.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]"},
		{name: "preserves explanatory prose", input: "Explain [STATUS: SUCCESS] as literal syntax.", expected: "Explain [STATUS: SUCCESS] as literal syntax."},
		{name: "preserves non-final control-shaped line", input: "[STATUS: FAILED | example]\nMore output follows.", expected: "[STATUS: FAILED | example]\nMore output follows."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanChatOutputForDisplay(tt.input); got != tt.expected {
				t.Errorf("CleanChatOutputForDisplay() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCleanChatOutput_PreservesCodedAliasesAndCleansRealAliases(t *testing.T) {
	codedTool := `[Using tool: bash"> <parameter name="command">echo coded</parameter> </invoke>`
	input := "Inline `\u003cthinking\u003ecoded\u003c/thinking\u003e` and `" + codedTool + "`.\n" +
		"```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n" + codedTool + "\n```\n" +
		"~~~text\n\u003cthinking\u003etilde\u003c/thinking\u003e\n" + codedTool + "\n~~~\n" +
		"\u003cthinking\u003ereal internal\u003c/thinking\u003e\nVisible answer.\n" +
		`[Using tool: bash">` + "\n" + `<parameter name="command">echo real</parameter>` + "\n" + `</invoke>`

	for _, clean := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(clean.name, func(t *testing.T) {
			got := clean.fn(input)
			for _, literal := range []string{
				"`\u003cthinking\u003ecoded\u003c/thinking\u003e`",
				"`" + codedTool + "`",
				"```text\n\u003cthinking\u003efenced\u003c/thinking\u003e\n" + codedTool + "\n```",
				"~~~text\n\u003cthinking\u003etilde\u003c/thinking\u003e\n" + codedTool + "\n~~~",
			} {
				if !strings.Contains(got, literal) {
					t.Fatalf("coded alias changed or disappeared %q in:\n%q", literal, got)
				}
			}
			if strings.Contains(got, "real internal") || strings.Contains(got, "echo real") || !strings.Contains(got, "Visible answer.") {
				t.Fatalf("real aliases were not cleaned normally:\n%q", got)
			}
		})
	}
}

func TestDeduplicateTaskSummaries_AllCommonMarkLineEndings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bare CR created summaries keep last",
			input: "Intro.\r\r---\rCreated 1 task(s):\r- \"Old\" (backlog)\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]",
			want:  "Intro.\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]",
		},
		{
			name:  "bare CR edited summaries keep last",
			input: "Intro.\r\r---\rEdited 1 task(s):\r- \"Old\" (updated: title)\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]",
			want:  "Intro.\r\r---\rEdited 1 task(s):\r- \"Real\" (updated: title) [TASK_EDITED:real]",
		},
		{
			name:  "CRLF edited summaries keep last",
			input: "Intro.\r\n\r\n---\r\nEdited 1 task(s):\r\n- \"Old\" (updated: title)\r\n\r\n---\r\nEdited 1 task(s):\r\n- \"Real\" (updated: title) [TASK_EDITED:real]",
			want:  "Intro.\r\n\r\n---\r\nEdited 1 task(s):\r\n- \"Real\" (updated: title) [TASK_EDITED:real]",
		},
		{
			name:  "coded bare CR summary between real duplicates remains inert",
			input: "Intro.\r\r---\rCreated 1 task(s):\r- \"Old\" (backlog)\r\rExample:\r~~~text\r---\rCreated 1 task(s):\r- \"Coded\" (backlog) [TASK_ID:coded]\r~~~\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]",
			want:  "Intro.\r\rExample:\r~~~text\r---\rCreated 1 task(s):\r- \"Coded\" (backlog) [TASK_ID:coded]\r~~~\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeduplicateTaskSummaries(tt.input); got != tt.want {
				t.Fatalf("DeduplicateTaskSummaries() =\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestCleanChatOutput_StripsBareCRRuntimeSummary(t *testing.T) {
	input := "Visible answer.\r\r---\rCreated 1 task(s):\r- \"Real\" (backlog) [TASK_ID:real]"
	if got := CleanChatOutput(input); got != "Visible answer." {
		t.Fatalf("CleanChatOutput() = %q, want visible answer only", got)
	}
}

func TestCleanChatOutput_PreservesBareCRFencedControlsAndSummaries(t *testing.T) {
	coded := "`````text\r<thinking>coded alias</thinking>\r[Thinking]coded thought[/Thinking]\r[Using tool: bash]\r[Tool bash done: coded]\r[STATUS: FAILED | coded]\r[TASK_ID:coded/create]\r[TASK_EDITED:coded/edit]\r```\r``````"
	input := coded + "\r[Thinking]real internal[/Thinking]\r[Using tool: bash]\r[Tool bash done]\ractual\r[/Tool]\r[TASK_ID:real/create]\r[TASK_EDITED:real/edit]\rVisible answer.\r[STATUS: SUCCESS]"

	for _, clean := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(clean.name, func(t *testing.T) {
			got := clean.fn(input)
			if !strings.Contains(got, coded) {
				t.Fatalf("bare-CR fenced controls changed or disappeared:\n%q", got)
			}
			for _, removed := range []string{"real internal", "actual", "[TASK_ID:real/create]", "[TASK_EDITED:real/edit]", "[STATUS: SUCCESS]"} {
				if strings.Contains(got, removed) {
					t.Fatalf("real control %q after bare-CR fence was not cleaned:\n%q", removed, got)
				}
			}
			if !strings.Contains(got, "Visible answer.") {
				t.Fatalf("visible answer disappeared:\n%q", got)
			}
		})
	}

	summary := "Intro.\n\n~~~text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r~~~\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"
	got := CleanChatOutput(summary)
	if !strings.Contains(got, "~~~text\r---\rEdited 1 task(s):\r- \"Coded\" (updated: title) [TASK_EDITED:coded]\r~~~") {
		t.Fatalf("bare-CR fenced summary changed or disappeared:\n%q", got)
	}
	if strings.Contains(got, "\"Real\"") || strings.Contains(got, "[TASK_ID:real]") {
		t.Fatalf("real summary outside bare-CR fence was not removed:\n%q", got)
	}
}

func TestCleanChatOutput_PreservesMultilineInlineCodeControlsAndCleansRealControls(t *testing.T) {
	coded := "`[Thinking]coded\n[/Thinking]\n[Using tool: bash]\n[Tool bash done: coded output]\n[TASK_ID:coded/create]\n[TASK_EDITED:coded/edit]`"
	input := coded + "\n[Thinking]real internal[/Thinking]\n[Using tool: bash]\n[Tool bash done: actual output]\n" +
		"[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer.\n[STATUS: SUCCESS]"

	for _, clean := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(clean.name, func(t *testing.T) {
			got := clean.fn(input)
			if !strings.Contains(got, coded) {
				t.Fatalf("multiline inline controls changed or disappeared:\n%q", got)
			}
			for _, removed := range []string{"real internal", "actual output", "[TASK_ID:real/create]", "[TASK_EDITED:real/edit]", "[STATUS: SUCCESS]"} {
				if strings.Contains(got, removed) {
					t.Fatalf("real control %q was not cleaned outside multiline code:\n%q", removed, got)
				}
			}
			if !strings.Contains(got, "Visible answer.") {
				t.Fatalf("visible answer disappeared:\n%q", got)
			}
		})
	}
}

func TestCleanChatOutput_EscapedBackticksDoNotProtectRealControls(t *testing.T) {
	validCoded := "``[Thinking]coded[/Thinking]\n[Using tool: bash]\n[TASK_ID:coded]``"
	input := `Escaped \` + "`" + `[Thinking]first real[/Thinking] escaped \` + "`" + "\n" +
		validCoded + "\n[TASK_ID:real]\nVisible answer.\n[STATUS: SUCCESS]"

	for _, clean := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(clean.name, func(t *testing.T) {
			got := clean.fn(input)
			if !strings.Contains(got, validCoded) {
				t.Fatalf("valid coded controls after escaped runs changed:\n%q", got)
			}
			for _, removed := range []string{"first real", "[TASK_ID:real]", "[STATUS: SUCCESS]"} {
				if strings.Contains(got, removed) {
					t.Fatalf("real control %q was falsely protected by escaped backticks:\n%q", removed, got)
				}
			}
			if strings.Count(got, "[Using tool: bash]") != 1 || !strings.Contains(got, "Visible answer.") {
				t.Fatalf("unexpected escaped-delimiter cleanup result:\n%q", got)
			}
		})
	}
}

func TestCleanChatOutput_ReconsidersLaterCodeSpanAfterUnmatchedDelimiter(t *testing.T) {
	coded := "`[Thinking]coded\n[/Thinking]\n[Using tool: bash]\n[Tool bash done: coded output]\n[TASK_ID:coded/create]\n[TASK_EDITED:coded/edit]`"
	input := "Unmatched `` prefix; " + coded + "\n[Thinking]real internal[/Thinking]\n[Using tool: bash]\n[Tool bash done: actual output]\n" +
		"[TASK_ID:real/create]\n[TASK_EDITED:real/edit]\nVisible answer.\n[STATUS: SUCCESS]"

	for _, clean := range []struct {
		name string
		fn   func(string) string
	}{
		{name: "history", fn: CleanChatOutput},
		{name: "display", fn: CleanChatOutputForDisplay},
	} {
		t.Run(clean.name, func(t *testing.T) {
			got := clean.fn(input)
			if !strings.Contains(got, "Unmatched `` prefix; "+coded) {
				t.Fatalf("later coded controls changed after unmatched delimiter:\n%q", got)
			}
			for _, removed := range []string{"real internal", "actual output", "[TASK_ID:real/create]", "[TASK_EDITED:real/edit]", "[STATUS: SUCCESS]"} {
				if strings.Contains(got, removed) {
					t.Fatalf("real control %q was not cleaned:\n%q", removed, got)
				}
			}
			if !strings.Contains(got, "Visible answer.") {
				t.Fatalf("visible answer disappeared:\n%q", got)
			}
		})
	}
}

func TestCleanChatOutput_PreservesMultilineInlineTaskSummaryBeforeRealSummary(t *testing.T) {
	coded := "`Example\n---\nCreated 1 task(s):\n- \"Coded\" (backlog) [TASK_ID:coded]\nEdited 1 task(s):\n- \"Coded edit\" (updated: title) [TASK_EDITED:coded-edit]`"
	input := "Unmatched `` prefix; " + coded + "\n\n---\nCreated 1 task(s):\n- \"Real\" (backlog) [TASK_ID:real]"

	got := CleanChatOutput(input)
	if !strings.Contains(got, "Unmatched `` prefix; "+coded) {
		t.Fatalf("multiline inline task summary changed or disappeared:\n%q", got)
	}
	if strings.Contains(got, `"Real"`) || strings.Contains(got, "[TASK_ID:real]") {
		t.Fatalf("real task summary was not removed from history:\n%q", got)
	}
}

func TestCleanChatOutput_PreservesNormalText(t *testing.T) {
	input := "Here's my analysis:\n\n---\n\nSome regular markdown content with a horizontal rule."
	got := CleanChatOutput(input)
	if got != input {
		t.Errorf("CleanChatOutput should preserve normal --- separators, got %q", got)
	}
}

func TestCleanChatOutput_PreservesMarkersInsideCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "preserves STATUS FAILED inside backticks",
			input:    "The failure marker is `[STATUS: FAILED | reason]`. Here it is:",
			expected: "The failure marker is `[STATUS: FAILED | reason]`. Here it is:",
		},
		{
			name:     "preserves STATUS SUCCESS inside backticks",
			input:    "Use `[STATUS: SUCCESS]` when the task is done.",
			expected: "Use `[STATUS: SUCCESS]` when the task is done.",
		},
		{
			name:     "preserves Using tool inside backticks",
			input:    "The marker `[Using tool: bash]` is stripped from history.",
			expected: "The marker `[Using tool: bash]` is stripped from history.",
		},
		{
			name:     "preserves Tool done result inside backticks",
			input:    "The result marker `[Tool bash done: output]` is stripped from history.",
			expected: "The result marker `[Tool bash done: output]` is stripped from history.",
		},
		{
			name:     "preserves Tool error result inside backticks",
			input:    "The result marker `[Tool bash error: command failed]` is stripped from history.",
			expected: "The result marker `[Tool bash error: command failed]` is stripped from history.",
		},
		{
			name:     "preserves inline Tool result and strips later result on same line",
			input:    "Example `[Tool bash done: output]`; actual [Tool bash done: actual output].",
			expected: "Example `[Tool bash done: output]`; actual .",
		},
		{
			name:     "preserves inline same-line Tool result block and strips later real blocks",
			input:    "Example `[Tool grep_search done]coded[/Tool]`.\n[Tool grep_search done]actual match[/Tool]\n[Tool bash error]command failed[/Tool]\nVisible answer.",
			expected: "Example `[Tool grep_search done]coded[/Tool]`.\nVisible answer.",
		},
		{
			name:     "protected partial same-line Tool result cannot mask later real block",
			input:    "Partial `[Tool grep_search done]coded`.\n[Tool grep_search done]actual match[/Tool]\nVisible answer.",
			expected: "Partial `[Tool grep_search done]coded`.\nVisible answer.",
		},
		{
			name:     "preserves fenced same-line Tool result block and strips later real block",
			input:    "```text\n[Tool grep_search done]coded[/Tool]\n```\n[Tool grep_search done]actual match[/Tool]\nVisible answer.",
			expected: "```text\n[Tool grep_search done]coded[/Tool]\n```\nVisible answer.",
		},
		{
			name:     "preserves fenced tool controls and strips later real controls",
			input:    "Examples:\n```text\n[Using tool: bash]\n[Tool bash done: output]\n[Tool read_file error]\nnot found\n[/Tool]\n```\n[Using tool: bash]\n[Tool bash done: actual]",
			expected: "Examples:\n```text\n[Using tool: bash]\n[Tool bash done: output]\n[Tool read_file error]\nnot found\n[/Tool]\n```",
		},
		{
			name:     "preserves tilde fenced tool controls",
			input:    "~~~log\n[Using tool: grep_search]\n[Tool grep_search done]\nmatch\n[/Tool]\n~~~",
			expected: "~~~log\n[Using tool: grep_search]\n[Tool grep_search done]\nmatch\n[/Tool]\n~~~",
		},
		{
			name:     "preserves unclosed fenced tool controls",
			input:    "```text\n[Using tool: bash]\n[Tool bash error: example]",
			expected: "```text\n[Using tool: bash]\n[Tool bash error: example]",
		},
		{
			name:     "preserves inline thinking controls",
			input:    "The literal control is `[Thinking]example[/Thinking]`.\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "The literal control is `[Thinking]example[/Thinking]`.\n\nVisible answer.",
		},
		{
			name:     "unclosed inline thinking example cannot mask later real control",
			input:    "The literal control is `[Thinking]example`.\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "The literal control is `[Thinking]example`.\n\nVisible answer.",
		},
		{
			name:     "preserves fenced thinking controls and strips later real control",
			input:    "Examples:\n```text\n[Thinking]\nfenced example\n[/Thinking]\n```\n[Thinking]\nreal internal text\n[/Thinking]\nVisible answer.",
			expected: "Examples:\n```text\n[Thinking]\nfenced example\n[/Thinking]\n```\n\nVisible answer.",
		},
		{
			name:     "preserves tilde fenced thinking controls",
			input:    "~~~log\n[Thinking]\nfenced example\n[/Thinking]\n~~~",
			expected: "~~~log\n[Thinking]\nfenced example\n[/Thinking]\n~~~",
		},
		{
			name:     "preserves unclosed fenced thinking controls",
			input:    "```text\n[Thinking]\nfenced example",
			expected: "```text\n[Thinking]\nfenced example",
		},
		{
			name:     "still strips Tool result outside backticks",
			input:    "The result follows.\n[Tool bash done: output]",
			expected: "The result follows.",
		},
		{
			name:     "still strips STATUS FAILED outside backticks",
			input:    "Could not complete.\n[STATUS: FAILED | tests failed]",
			expected: "Could not complete.",
		},
		{
			name:     "strips standalone marker but preserves prose reference in backticks",
			input:    "Write `[STATUS: FAILED | reason]` at the end.\n\n[STATUS: FAILED | actual failure]",
			expected: "Write `[STATUS: FAILED | reason]` at the end.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChatOutput(tt.input)
			if got != tt.expected {
				t.Errorf("CleanChatOutput() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestCleanChatOutput_StripsProposedPlanWrapperTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "strips proposed_plan wrappers and keeps body",
			input:    "Here is the plan:\n<proposed_plan>\n1. Do X\n2. Do Y\n</proposed_plan>",
			expected: "Here is the plan:\n\n1. Do X\n2. Do Y",
		},
		{
			name:     "does not strip normal angle-bracket text",
			input:    "User typed literal text: <custom_tag>keep me</custom_tag>",
			expected: "User typed literal text: <custom_tag>keep me</custom_tag>",
		},
		{
			name:     "case-insensitive wrapper stripping",
			input:    "<Proposed_Plan>Step A</Proposed_Plan>",
			expected: "Step A",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChatOutput(tt.input)
			if got != tt.expected {
				t.Errorf("CleanChatOutput() =\n%q\nwant:\n%q", got, tt.expected)
			}
		})
	}
}

func TestCleanChatOutputForDisplay_StripsProposedPlanWrapperTags(t *testing.T) {
	input := "Analysis:\n<proposed_plan>\n- Step 1\n- Step 2\n</proposed_plan>\nDone."
	expected := "Analysis:\n\n- Step 1\n- Step 2\n\nDone."
	got := CleanChatOutputForDisplay(input)
	if got != expected {
		t.Errorf("CleanChatOutputForDisplay() =\n%q\nwant:\n%q", got, expected)
	}
}

func TestCleanChatOutput_StillStripsSummaries(t *testing.T) {
	input := "Here.\n[PROJECT_INFO]\n\n---\nProject Info:\n- **Name:** openvibely"
	got := CleanChatOutput(input)
	if got != "Here.\n[PROJECT_INFO]" {
		t.Errorf("CleanChatOutput() should strip only the summary and preserve inert bracket text, got %q", got)
	}
}
