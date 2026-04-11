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
			name:       "marker without closing bracket",
			output:     "[STATUS: FAILED | something went wrong",
			prefix:     "[STATUS: FAILED |",
			wantReason: "something went wrong",
			wantFound:  true,
		},
		{
			name:       "uses last occurrence",
			output:     "Saw [STATUS: FAILED | first error] earlier but then [STATUS: FAILED | final error]",
			prefix:     "[STATUS: FAILED |",
			wantReason: "final error",
			wantFound:  true,
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

func TestCleanChatOutput_StripsCreateTaskBlocks(t *testing.T) {
	input := "I'll create that task for you.\n\n[CREATE_TASK]\n{\"title\": \"Fix bug\", \"prompt\": \"Fix the login bug\", \"category\": \"backlog\"}\n[/CREATE_TASK]\n\nDone!"
	got := CleanChatOutput(input)

	got = strings.TrimSpace(got)
	if !strings.Contains(got, "I'll create that task for you.") {
		t.Errorf("CleanChatOutput should preserve text before CREATE_TASK block, got %q", got)
	}
	if strings.Contains(got, "[CREATE_TASK]") {
		t.Errorf("CleanChatOutput should strip CREATE_TASK blocks, got %q", got)
	}
	if strings.Contains(got, "Fix bug") {
		t.Errorf("CleanChatOutput should strip CREATE_TASK JSON content, got %q", got)
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
		{name: "strips tool use markers", input: "Let me check.\n[Using tool: Read]\nThe file contains...", expected: "Let me check.\n\nThe file contains..."},
		{name: "strips multi_tool_use protocol artifact", input: "} to=multi_tool_use.parallel code something\nActual text.", expected: "Actual text."},
		{name: "strips multi_tool_use without to= prefix", input: "multi_tool_use.parallel error here\nUseful text.", expected: "Useful text."},
		{name: "strips multi_tool_use with braces", input: "}} multi_tool_use.sequential blah\nNarrative.", expected: "Narrative."},
		{name: "strips multi_tool_use with unicode", input: "} to=multi_tool_use.parallel code 彩神争霸高json uμ? Wait malformed.\nRetrying.", expected: "Retrying."},
		{name: "preserves normal multi-tool text", input: "The multi-tool approach works well.", expected: "The multi-tool approach works well."},
		{name: "strips thinking blocks", input: "\n[Thinking]\nLet me analyze this question.\n\nCOBOL was created in 1959.", expected: "COBOL was created in 1959."},
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

func TestCleanChatOutput_StripsTaskSummaries(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips appended task creation summary", input: "I'll create that task for you.\n\n[CREATE_TASK]\n{\"title\": \"Fix bug\"}\n[/CREATE_TASK]\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]", expected: "I'll create that task for you."},
		{name: "strips appended task edit summary", input: "I'll update that task.\n\n[EDIT_TASK]\n{\"id\": \"abc\", \"title\": \"New\"}\n[/EDIT_TASK]\n\n---\nEdited 1 task(s):\n- \"New\" (updated: title) [TASK_EDITED:abc]", expected: "I'll update that task."},
		{name: "strips TASK_ID markers from text", input: "Task created [TASK_ID:abc123] done.", expected: "Task created  done."},
		{name: "strips TASK_EDITED markers from text", input: "Task edited [TASK_EDITED:abc123] done.", expected: "Task edited  done."},
		{name: "strips EDIT_TASK blocks", input: "Updating.\n[EDIT_TASK]\n{\"id\": \"x\"}\n[/EDIT_TASK]\nDone.", expected: "Updating.\n\nDone."},
		{name: "strips EXECUTE_TASKS blocks", input: "Running.\n[EXECUTE_TASKS]\n{\"tags\": [\"bug\"]}\n[/EXECUTE_TASKS]\nDone.", expected: "Running.\n\nDone."},
		{name: "strips task execution summary", input: "Running tasks.\n\n---\nTask Execution Results:\n- Executed 2 task(s) matching tags=[bug]", expected: "Running tasks."},
		{name: "preserves text without summaries", input: "Just a normal response with no task markers.", expected: "Just a normal response with no task markers."},
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

func TestCleanChatOutput_StripsTaskChatResults(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips VIEW_TASK_CHAT markers", input: "Let me check.\n[VIEW_TASK_CHAT]\n{\"task_id\": \"abc\"}\n[/VIEW_TASK_CHAT]\nDone.", expected: "Let me check.\n\nDone."},
		{name: "strips SEND_TO_TASK markers", input: "Sending.\n[SEND_TO_TASK]\n{\"task_id\": \"abc\", \"message\": \"hi\"}\n[/SEND_TO_TASK]\nDone.", expected: "Sending.\n\nDone."},
		{name: "strips appended thread transcript", input: "Here's the history.\n\n---\n**Thread history for task: \"Fix login\"** [TASK_ID:abc]\nStatus: completed\n\n**User:**\nFix it\n\n**Assistant:**\nDone.", expected: "Here's the history."},
		{name: "strips appended thread messages", input: "Message sent.\n\n---\nThread Messages:\n- Sent message to task \"Fix login\" [TASK_ID:abc] — the agent is now processing.", expected: "Message sent."},
		{name: "strips task not found error", input: "Let me look.\n\n---\nCould not find task: no task found matching \"nonexistent\"", expected: "Let me look."},
		{name: "strips error retrieving thread", input: "Checking.\n\n---\nError retrieving thread for task \"X\": database error", expected: "Checking."},
		{name: "combined: task creation + thread messages not eaten", input: "Done.\n\n---\nCreated 1 task(s):\n- \"New\" (backlog) [TASK_ID:xyz]\n\n---\nThread Messages:\n- Sent message to task \"Old\" [TASK_ID:abc]", expected: "Done."},
		{name: "combined: task execution + thread transcript both stripped", input: "Working.\n\n---\nTask Execution Results:\n- Executed 2 tasks\n\n---\n**Thread history for task: \"API\"** [TASK_ID:abc]\nStatus: completed", expected: "Working."},
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

func TestCleanChatOutput_PreservesNormalText(t *testing.T) {
	input := "Here's my analysis:\n\n---\n\nSome regular markdown content with a horizontal rule."
	got := CleanChatOutput(input)
	if got != input {
		t.Errorf("CleanChatOutput should preserve normal --- separators, got %q", got)
	}
}

func TestCleanChatOutput_StripsScheduleTaskMarkers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips SCHEDULE_TASK markers", input: "I'll schedule that.\n[SCHEDULE_TASK]\n{\"task_id\": \"abc\", \"time\": \"09:00\", \"repeat\": \"daily\"}\n[/SCHEDULE_TASK]\nDone.", expected: "I'll schedule that.\n\nDone."},
		{name: "strips appended schedule results", input: "Scheduled.\n\n---\nSchedule Results:\n- Scheduled task \"Backup\" [TASK_ID:abc] at 09:00 (daily)", expected: "Scheduled."},
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

func TestCleanChatOutput_StripsNewAppSettingsMarkers(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips LIST_PERSONALITIES marker", input: "Here are the personalities.\n[LIST_PERSONALITIES]\nDone.", expected: "Here are the personalities.\n\nDone."},
		{name: "strips SET_PERSONALITY markers", input: "Setting personality.\n[SET_PERSONALITY]\n{\"personality\": \"pirate_captain\"}\n[/SET_PERSONALITY]\nDone.", expected: "Setting personality.\n\nDone."},
		{name: "strips LIST_MODELS marker", input: "Here are the models.\n[LIST_MODELS]\nDone.", expected: "Here are the models.\n\nDone."},
		{name: "strips VIEW_SETTINGS marker", input: "Here are the settings.\n[VIEW_SETTINGS]\nDone.", expected: "Here are the settings.\n\nDone."},
		{name: "strips PROJECT_INFO marker", input: "Here's the project info.\n[PROJECT_INFO]\nDone.", expected: "Here's the project info.\n\nDone."},
		{name: "strips appended personality results", input: "Listed.\n\n---\nAvailable Personalities:\n- **Sarcastic Engineer** — Dry wit", expected: "Listed."},
		{name: "strips appended personality settings results", input: "Changed.\n\n---\nPersonality Settings:\n- Personality changed to **Pirate Captain**", expected: "Changed."},
		{name: "strips appended model results", input: "Here.\n\n---\nConfigured Models:\n- **Test Model** — anthropic", expected: "Here."},
		{name: "strips appended app settings results", input: "Here.\n\n---\nApp Settings:\n- **Personality:** zen_debugger", expected: "Here."},
		{name: "strips appended project info results", input: "Here.\n\n---\nProject Info:\n- **Name:** My Project", expected: "Here."},
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

func TestCleanChatOutputForDisplay_PreservesSummaries(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "preserves project info results", input: "Let me get the project information for you.\n[PROJECT_INFO]\n\n---\nProject Info:\n- **Name:** openvibely\n- **Total tasks:** 15", expected: "Let me get the project information for you.\n\n\n---\nProject Info:\n- **Name:** openvibely\n- **Total tasks:** 15"},
		{name: "preserves available personalities results", input: "Here are the personalities.\n[LIST_PERSONALITIES]\n\n---\nAvailable Personalities:\n- **Sarcastic Engineer** — Dry wit\n- **Pirate Captain** — Arr!", expected: "Here are the personalities.\n\n\n---\nAvailable Personalities:\n- **Sarcastic Engineer** — Dry wit\n- **Pirate Captain** — Arr!"},
		{name: "preserves configured models results", input: "Here.\n[LIST_MODELS]\n\n---\nConfigured Models:\n- **Test Model** — anthropic", expected: "Here.\n\n\n---\nConfigured Models:\n- **Test Model** — anthropic"},
		{name: "preserves app settings results", input: "Here.\n[VIEW_SETTINGS]\n\n---\nApp Settings:\n- **Personality:** zen_debugger", expected: "Here.\n\n\n---\nApp Settings:\n- **Personality:** zen_debugger"},
		{name: "preserves personality settings results", input: "Changed.\n[SET_PERSONALITY]\n{\"personality\": \"pirate\"}\n[/SET_PERSONALITY]\n\n---\nPersonality Settings:\n- Changed to **Pirate**", expected: "Changed.\n\n\n---\nPersonality Settings:\n- Changed to **Pirate**"},
		{name: "preserves task creation results", input: "Created.\n[CREATE_TASK]\n{\"title\": \"Fix bug\"}\n[/CREATE_TASK]\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog)", expected: "Created.\n\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog)"},
		{name: "preserves thread history results", input: "Here's the thread.\n[VIEW_TASK_CHAT]\n{\"task_id\": \"abc\"}\n[/VIEW_TASK_CHAT]\n\n---\n**Thread history for task \"My Task\":**\nSome history", expected: "Here's the thread.\n\n\n---\n**Thread history for task \"My Task\":**\nSome history"},
		{name: "preserves schedule results", input: "Scheduled.\n[SCHEDULE_TASK]\n{\"title\": \"Daily\"}\n[/SCHEDULE_TASK]\n\n---\nSchedule Results:\n- Created schedule", expected: "Scheduled.\n\n\n---\nSchedule Results:\n- Created schedule"},
		{name: "still strips markers", input: "Here.\n[PROJECT_INFO]\nDone.", expected: "Here.\n\nDone."},
		{name: "still strips thinking blocks", input: "\n[Thinking]\nSome internal thinking\n\nActual response here.", expected: "Actual response here."},
		{name: "still strips TASK_ID markers but preserves summary", input: "Created.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog) [TASK_ID:abc123]", expected: "Created.\n\n---\nCreated 1 task(s):\n- \"Fix bug\" (backlog)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanChatOutputForDisplay(tt.input)
			if got != tt.expected {
				t.Errorf("CleanChatOutputForDisplay() =\n%q\nwant:\n%q", got, tt.expected)
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
	if got != "Here." {
		t.Errorf("CleanChatOutput() should still strip summaries, got %q", got)
	}
}
