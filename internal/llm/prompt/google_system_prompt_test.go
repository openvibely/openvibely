package prompt

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGooglePromptModesAndOverrides(t *testing.T) {
	for _, autonomous := range []bool{true, false} {
		base := renderGoogleBasePrompt(autonomous, []string{"read_file"}, map[string]string{"guidelines": "{{if .IsAutonomous}}TASK{{else}}CHAT{{end}} guidelines"}, nil)
		require.Contains(t, base, "OpenVibely")
		require.Contains(t, base, "Respect all approval gates")
		if autonomous {
			require.Contains(t, base, "TASK guidelines")
		} else {
			require.Contains(t, base, "CHAT guidelines")
		}
		require.Contains(t, BuildGoogleAgentSystemPrompt(base, "PROJECT_RULE", "/tmp"), "PROJECT_RULE")
		require.Contains(t, BuildGoogleChatSystemPrompt(base, false, models.ChatModeOrchestrate, "CHAT_CONTEXT", false), "CHAT_CONTEXT")
	}
}
func TestGooglePromptUnsupportedTemplateFallsBack(t *testing.T) {
	for _, override := range []string{"{{.MissingField}}", "{{broken", ""} {
		base := renderGoogleBasePrompt(false, nil, map[string]string{"guidelines": override}, nil)
		require.Contains(t, base, "Maintain documentation integrity")
		require.NotContains(t, base, "{{")
	}
}

func TestGoogleFetchedGuidelinesUseRuntimeTools(t *testing.T) {
	raw, err := googlePrompts.ReadFile("google/hydrogen_guidelines.tmpl")
	require.NoError(t, err)
	for _, tools := range [][]string{nil, {"read_file", "shell"}} {
		base := renderGoogleBasePrompt(true, tools, map[string]string{"guidelines": string(raw)}, nil)
		require.Contains(t, base, "CODE QUALITY")
		require.Contains(t, base, "<final_answer_accuracy>")
		require.NotContains(t, base, "{{")
		if len(tools) > 0 {
			require.Contains(t, base, "Call read_file with offset and limit")
		} else {
			require.NotContains(t, base, "<efficient_code_reading>")
		}
	}
}

func TestBundledGoogleModelProfiles(t *testing.T) {
	for _, tc := range []struct {
		model                            string
		guidelines, background, language bool
	}{
		{"gemini-4-hydrogen", true, true, false},
		{"gemini-4-hydrogen-high", true, true, false},
		{"gemini-4-argon", false, true, true},
		{"gemini-4-carbon", false, true, true},
		{"gemini-3.8-flash", false, true, false},
		{"gemini-4-neon", false, false, false},
		{"claude-sonnet-5-5-medium", false, false, false},
		{"claude-opus-5-5-high", false, false, false},
		{"gpt-oss-120b-medium", false, false, false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			for _, autonomous := range []bool{false, true} {
				text := GoogleBasePrompt(tc.model, autonomous, []string{"read_file", "shell"})
				require.Equal(t, tc.guidelines, strings.Contains(text, "<code_quality>"))
				require.Equal(t, tc.background, strings.Contains(text, "command is still running in the background"))
				require.Equal(t, tc.language, strings.Contains(text, "Do not mix languages in that update"))
				require.NotContains(t, text, "{{")
				require.Contains(t, text, "Respect all approval gates")
			}
		})
	}
}
