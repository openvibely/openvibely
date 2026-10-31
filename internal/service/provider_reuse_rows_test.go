package service

import (
	"context"
	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestToolArgumentConversionKeepsNormalization(t *testing.T) {
	for _, raw := range []string{`{"n":9007199254740993,"x":"\u0061","dup":1,"dup":2}`, `{"z":"text","a":[{"x":"<>&"},null,1.00]}`, `null`, `{"n":1e400}`, `broken`} {
		var old any
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			old = map[string]any{"raw_arguments": raw}
		}
		expected, err := json.Marshal(old)
		require.NoError(t, err)
		call := openaiclient.CompletionsToolCall{ID: "call", Type: "function"}
		call.Function.Name = "tool"
		call.Function.Arguments = raw
		messages := completionsAnthropicSummaryMessages([]openaiclient.CompletionsHistoryMessage{{Role: "assistant", ToolCalls: []openaiclient.CompletionsToolCall{call}}})
		require.Len(t, messages, 1)
		var blocks []struct {
			Input json.RawMessage `json:"input"`
		}
		require.NoError(t, json.Unmarshal(messages[0].ContentBlocks, &blocks))
		require.Len(t, blocks, 1)
		require.Equal(t, string(expected), string(blocks[0].Input))
		converted := preparedAnthropicReplayMessages(messages, false)
		require.Len(t, converted, 1)
		require.Len(t, converted[0].ToolCalls, 1)
		require.Equal(t, string(expected), converted[0].ToolCalls[0].Function.Arguments)
	}
}

func TestRestoredNativeRowsBudgetSendAndProviderSwitch(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic} {
		t.Run(string(provider), func(t *testing.T) {
			stores := teststores.New(t).Stores
			agent := models.LLMConfig{ID: "model", Provider: provider, Model: "fixture", ContextWindow: 200000}
			raw := `[{"type":"compaction","encrypted_content":"opaque"},{"type":"function_call_output","call_id":"c","output":"saved text"}]`
			strategy := "openai_responses"
			if provider == models.ProviderAnthropic {
				strategy = "anthropic_context_management"
				raw = `{"version":1,"messages":[{"role":"assistant","content":[{"type":"compaction","content":"saved summary"},{"type":"text","text":"saved answer"}]}]}`
			}
			req := llmcontracts.AgentRequest{Ctx: preparedhistory.WithScope(context.Background()), TransportScope: "task:row-test", Agent: agent}
			cp := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "row-test", CompatibilityKey: providerCompatibilityKey(agent), ModelConfigID: agent.ID, Strategy: strategy, ProviderStateJSON: raw}
			require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(req.Ctx, cp))
			svc := &LLMService{execRepo: stores.Executions}
			got := svc.restoreCompactionCheckpoint(req)
			require.Empty(t, got.NativeCompactionStateJSON)
			require.NotNil(t, got.NativeCompactionStateRows)
			require.JSONEq(t, raw, got.NativeState().FullTranscriptJSON())
			legacy := got
			legacy.NativeCompactionStateRows = nil
			legacy.NativeCompactionStateJSON = raw
			require.Equal(t, calculateRequestBudget(legacy), calculateRequestBudget(got))
			ctx := llmcontracts.WithNativeCompactionState(got.Ctx, got.NativeState())
			require.Same(t, got.NativeCompactionStateRows, llmcontracts.NativeCompactionStateFromContext(ctx).Rows)
			cleared := llmcontracts.WithNativeCompactionStateJSON(ctx, "")
			require.Nil(t, llmcontracts.NativeCompactionStateFromContext(cleared).Rows)
			require.Empty(t, llmcontracts.NativeCompactionStateJSONFromContext(cleared))
			if provider == models.ProviderAnthropic {
				old, err := portableAnthropicCheckpoint(raw)
				require.NoError(t, err)
				next, err := portableAnthropicReplayCheckpoint(got.NativeState())
				require.NoError(t, err)
				require.Equal(t, old, next)
				switched := req
				switched.Agent.Provider = models.ProviderOpenAICompatible
				result := svc.restoreCompactionCheckpoint(switched)
				require.False(t, result.HasNativeState())
				require.NotEmpty(t, result.ChatHistory)
			} else {
				switched := req
				switched.Agent.Provider = models.ProviderAnthropic
				require.False(t, svc.restoreCompactionCheckpoint(switched).HasNativeState())
			}
		})
	}
}

func TestPortableLegacyNativeRows(t *testing.T) {
	raw := `{"type":"compaction","content":"summary"}`
	records, err := transcript.ReplayRecords([]models.ExecutionReplayMessage{{TranscriptJSON: raw}})
	require.NoError(t, err)
	rows, err := transcript.RestoreReplayRows(records)
	require.NoError(t, err)
	want, err := portableAnthropicCheckpoint(raw)
	require.NoError(t, err)
	got, err := portableAnthropicReplayCheckpoint(rows[0])
	require.NoError(t, err)
	require.Equal(t, want, got)
	// The public decoded API remains ordinary values, including tool arguments.
	native := []anthropicclient.Message{{Role: "assistant", ContentBlocks: json.RawMessage(`[{"type":"tool_use","id":"c","name":"t","input":{"n":1.00}}]`)}}
	require.Equal(t, `{"n":1}`, anthropicReplayMessages(native, false)[0].ToolCalls[0].Function.Arguments)
}
