package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func TestForeignToolConversionsRetainBorrowedText(t *testing.T) {
	content, _ := json.Marshal(strings.Repeat("large tool result 世界\n", 100))
	native := models.ReplayFromRows("responses_input", []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"call","name":"read","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call","output":` + string(content) + `}`),
	})
	initial := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{native}}}
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		converted := normalizeProviderReplay(provider, initial, false)
		require.Len(t, converted[0].ReplayMessages, 1)
		var rows []json.RawMessage
		if provider == models.ProviderAnthropic {
			var envelope struct{ Messages []json.RawMessage }
			require.NoError(t, converted[0].ReplayMessages[0].DecodeTranscript(&envelope))
			require.Len(t, envelope.Messages, 2)
			fields, err := jsonview.Fields(envelope.Messages[1])
			require.NoError(t, err)
			blocks, err := jsonview.Array(fields["content_blocks"])
			require.NoError(t, err)
			result, err := jsonview.Fields(blocks[0])
			require.NoError(t, err)
			require.Equal(t, string(content), string(result["content"]))
			converted = normalizeProviderReplay(models.ProviderOpenAICompatible, converted, false)
		}
		require.NoError(t, converted[0].ReplayMessages[0].DecodeTranscript(&rows))
		prepared, err := openaiclient.PrepareCompletionsHistory(converted[0].ReplayMessages[0])
		require.NoError(t, err)
		found := false
		prepared.Visit(func(m openaiclient.CompletionsHistoryMessage, value any) {
			if m.Role == "tool" {
				found = true
				require.Empty(t, m.Content)
				view, ok := value.(jsonview.Value)
				require.True(t, ok)
				require.Equal(t, string(content), string(view.Raw()))
			}
		})
		require.True(t, found)
	}
	require.Equal(t, string(content), func() string { fields, _ := jsonview.Fields(native.Rows.Items[1]); return string(fields["output"]) }())
}

func TestUserSummaryProjectionMatchesFullProjection(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		history := []models.Execution{
			{Status: models.ExecCompleted, PromptSent: "first", Output: "assistant"},
			{Status: models.ExecCompleted, PromptSent: "second", ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("", []json.RawMessage{
				json.RawMessage(`{"role":"user","content":[{"type":"text","text":"saved context"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}`),
				json.RawMessage(`{"role":"tool","tool_call_id":"c","content":"result"}`),
			})}},
		}
		full, err := summaryHistoryInput(provider, history)
		require.NoError(t, err)
		var want []json.RawMessage
		for _, row := range full {
			fields, err := jsonview.FoldedFields(row)
			require.NoError(t, err)
			var role string
			_ = json.Unmarshal(fields["role"], &role)
			if role == "user" {
				want = append(want, row)
			}
		}
		got, err := summaryUserHistoryInput(provider, history)
		require.NoError(t, err)
		require.Len(t, got, len(want), string(provider))
		for i := range want {
			require.JSONEq(t, string(want[i]), string(got[i]))
		}
	}
}

func TestForeignAnthropicNullBlockPreservesFollowingText(t *testing.T) {
	messages := []anthropicclient.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[null,{"type":"text","text":"keep this"}]`)}}
	got := anthropicReplayMessages(messages, false)
	require.Len(t, got, 1)
	require.Len(t, got[0].ImageContent, 2)
	require.Equal(t, "Historical provider block: null", got[0].ImageContent[0]["text"])
	require.Equal(t, "keep this", got[0].ImageContent[1]["text"])
}
