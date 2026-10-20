package service

import (
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestCompactionRetainsOutputMissingFromEnvironmentReplay(t *testing.T) {
	const partial = "Changed the migration; verification was interrupted."
	const emptySnapshot = `{"anthropic_execution_environment":"<openvibely_execution_environment>\nsaved environment","messages":[{"role":"user","content":"fix it"},{"role":"assistant","content":""}]}`
	for _, tc := range []struct{ name, output, reason, want string }{
		{"partial output", partial, "stream disconnected", partial},
		{"failure before output", "", "server unavailable", "server unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execution := models.Execution{Status: models.ExecFailed, PromptSent: "fix it", Output: tc.output, ErrorMessage: tc.reason, ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "fix it", TranscriptJSON: emptySnapshot}}}
			prompt := summaryHistoryText(t, models.ProviderAnthropic, []models.Execution{execution})
			if !strings.Contains(prompt, tc.want) {
				t.Fatalf("compaction lost saved execution content: %s", prompt)
			}
			if !strings.Contains(prompt, "saved environment") {
				t.Fatal("compaction lost environment snapshot")
			}
		})
	}
}

func TestCompactionDoesNotDuplicateCompleteReplayOutput(t *testing.T) {
	for provider, transcript := range map[models.LLMProvider]string{
		models.ProviderAnthropic: `{"anthropic_execution_environment":"<openvibely_execution_environment>\nsaved environment","messages":[{"role":"assistant","content":"unique answer"}]}`,
		models.ProviderOpenAI:    `{"responses_input":[{"type":"message","role":"assistant","content":"unique answer"}]}`,
	} {
		execution := models.Execution{Status: models.ExecCompleted, PromptSent: "question", Output: "unique answer", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}
		prompt := summaryHistoryText(t, provider, []models.Execution{execution})
		if got := strings.Count(prompt, "unique answer"); got != 1 {
			t.Fatalf("answer appears %d times: %s", got, prompt)
		}
	}
}
