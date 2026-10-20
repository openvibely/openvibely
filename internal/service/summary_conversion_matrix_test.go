package service

import (
	"context"
	"encoding/json"
	llmanthropic "github.com/openvibely/openvibely/internal/llm/anthropic"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSummaryParallelToolConversionMatrix(t *testing.T) {
	sources := map[string]string{
		"responses": `{"responses_input":[
			{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},
			{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"b","output":"file B"},
			{"type":"function_call_output","call_id":"a","output":"file A"},
			{"type":"message","role":"user","content":"continue"},
			{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"},
			{"type":"function_call_output","call_id":"c","output":"file C"}
		]}`,
		"completions": `[
			{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"b","content":"file B"},
			{"role":"tool","tool_call_id":"a","content":"file A"},
			{"role":"user","content":"continue"},
			{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c","content":"file C"}
		]`,
		"anthropic": `{"messages":[
			{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}},{"type":"tool_use","id":"b","name":"read_file","input":{}}]},
			{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"b","content":"file B"},{"type":"tool_result","tool_use_id":"a","content":"file A"}]},
			{"role":"user","content_blocks":[{"type":"text","text":"continue"}]},
			{"role":"assistant","content_blocks":[{"type":"tool_use","id":"c","name":"read_file","input":{}}]},
			{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"c","content":"file C"}]}
		]}`,
	}
	for source, transcript := range sources {
		for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
			for _, mode := range []string{"summary", "followup"} {
				t.Run(source+"_to_"+string(provider)+"/"+mode, func(t *testing.T) {
					history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
					var input []json.RawMessage
					if mode == "summary" {
						var err error
						input, err = summaryHistoryInput(provider, history)
						require.NoError(t, err)
					} else {
						calls := 0
						_, err := (&LLMService{}).callProviderWithCompaction(providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
							calls++
							require.Nil(t, req.SummaryInput, "normal follow-up must not compact")
							require.Equal(t, "new follow-up", req.Message)
							var outgoing any
							switch provider {
							case models.ProviderOpenAI:
								outgoing = llmopenai.HistoryInputItems(req.ChatHistory)
							case models.ProviderAnthropic:
								outgoing = llmanthropic.SummaryHistory(req.ChatHistory)
							default:
								outgoing = compatible.SummaryHistory(req.ChatHistory)
							}
							raw, err := json.Marshal(outgoing)
							require.NoError(t, err)
							require.NoError(t, json.Unmarshal(raw, &input))
							return llmcontracts.AgentResult{Output: "done"}, nil
						}), llmcontracts.AgentRequest{Ctx: context.Background(), Operation: llmcontracts.OperationStreaming, Followup: true, Message: "new follow-up", ChatHistory: history, Agent: models.LLMConfig{Provider: provider, Model: "test", ContextWindow: 200000}})
						require.NoError(t, err)
						require.Equal(t, 1, calls)
					}
					target := "completions"
					if provider == models.ProviderOpenAI {
						target = "responses"
					}
					if provider == models.ProviderAnthropic {
						target = "anthropic"
					}
					// The independently specified destination fixture defines exact
					// message boundaries, call arguments, IDs, and result ordering.
					var expected []json.RawMessage
					if target == "completions" {
						require.NoError(t, json.Unmarshal([]byte(sources[target]), &expected))
					} else {
						var envelope struct {
							Input    []json.RawMessage `json:"responses_input"`
							Messages []json.RawMessage `json:"messages"`
						}
						require.NoError(t, json.Unmarshal([]byte(sources[target]), &envelope))
						expected = envelope.Input
						if target == "anthropic" {
							expected = envelope.Messages
						}
					}
					require.Len(t, input, len(expected))
					for i := range expected {
						require.Equal(t, canonicalSummaryTestMessage(t, expected[i]), canonicalSummaryTestMessage(t, input[i]), "message %d", i)
					}
					require.Equal(t, transcript, history[0].ReplayMessages[0].TranscriptJSON, "conversion must not mutate source history")
				})
			}
		}
	}
}

// Ignore only equivalent text encodings and empty optional text fields.
// Tool arrays and message boundaries remain intact for comparison.
func canonicalSummaryTestMessage(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var message map[string]any
	require.NoError(t, json.Unmarshal(raw, &message))
	if message["content"] == "" {
		delete(message, "content")
	}
	if blocks, ok := message["content"].([]any); ok && len(blocks) == 1 {
		if block, ok := blocks[0].(map[string]any); ok && len(block) == 2 && (block["type"] == "text" || block["type"] == "input_text") {
			message["content"] = block["text"]
		}
	}
	return message
}
