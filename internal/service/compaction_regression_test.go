package service

import (
	"context"
	"encoding/json"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestSummaryRegressionAnthropicPartialOutputSurvivesStructuredReplay(t *testing.T) {
	history := []models.Execution{{Status: models.ExecFailed, PromptSent: "inspect", Output: "PARTIAL_PROGRESS_ONLY_IN_PERSISTED_OUTPUT", ErrorMessage: "stream disconnected", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"anthropic_execution_environment":"<openvibely_execution_environment>\nsaved","messages":[{"role":"user","content":"inspect"},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a","content":"file"}]}]}`}}}}
	_, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		raw, _ := json.Marshal(r.SummaryInput)
		raw = append([]byte(r.Message), raw...)
		if !strings.Contains(string(raw), "PARTIAL_PROGRESS_ONLY_IN_PERSISTED_OUTPUT") {
			t.Errorf("summary input lost persisted partial output: %s", raw)
		}
		return llmcontracts.AgentResult{Output: "summary"}, nil
	}), llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: models.ProviderAnthropic, ContextWindow: 200000}}, history)
	if err != nil {
		t.Fatal(err)
	}
}
func TestSummaryRegressionProviderSwitchRetainsToolResultsForSummary(t *testing.T) {
	history := []models.Execution{{Status: models.ExecCompleted, PromptSent: "inspect", Output: "finished", ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "inspect", AssistantContent: "finished", TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"inspect"},{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"IMPORTANT_TOOL_RESULT"},{"type":"message","role":"assistant","content":"finished"}]}`}}}}
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			_, err := (&LLMService{}).localSummaryCompaction(providerAdapterFunc(func(r llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				raw, _ := json.Marshal(r.SummaryInput)
				raw = append([]byte(r.Message), raw...)
				if !strings.Contains(string(raw), "IMPORTANT_TOOL_RESULT") {
					t.Errorf("summary lost historical tool results after provider switch: %s", raw)
				}
				return llmcontracts.AgentResult{Output: "summary"}, nil
			}), llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: provider, ContextWindow: 200000}}, history)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSummaryProviderSwitchGroupsParallelResponsesCalls(t *testing.T) {
	history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file A"},{"type":"function_call_output","call_id":"b","output":"file B"}]}`}}}}
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			input, err := summaryHistoryInput(provider, history)
			if err != nil {
				t.Fatal(err)
			}
			if len(input) != 3 {
				t.Fatalf("expected one assistant message and two tool results, got %s", input)
			}
			var assistant struct {
				Role      string
				ToolCalls []struct{ ID string } `json:"tool_calls"`
			}
			if err := json.Unmarshal(input[0], &assistant); err != nil {
				t.Fatal(err)
			}
			if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 || assistant.ToolCalls[0].ID != "a" || assistant.ToolCalls[1].ID != "b" {
				t.Fatalf("parallel calls were not grouped: %s", input[0])
			}
			for i, id := range []string{"a", "b"} {
				var result struct {
					Role       string
					ToolCallID string `json:"tool_call_id"`
				}
				if err := json.Unmarshal(input[i+1], &result); err != nil {
					t.Fatal(err)
				}
				if result.Role != "tool" || result.ToolCallID != id {
					t.Fatalf("incorrect tool result: %s", input[i+1])
				}
			}
		})
	}
}

func TestSummaryProviderSwitchPreservesAllReplayTurns(t *testing.T) {
	sources := map[string][]models.ExecutionReplayMessage{
		"responses": {{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"initial request"},{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"first result"},{"type":"message","role":"user","content":"later request"},{"type":"function_call","call_id":"b","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"second result"}]}`}},
		"completions": {
			{TranscriptJSON: `[{"role":"user","content":"initial request"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"first result"}]`},
			{TranscriptJSON: `[{"role":"user","content":"later request"},{"role":"assistant","tool_calls":[{"id":"b","type":"function","function":{"name":"read_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"b","content":"second result"}]`},
		},
		"anthropic": {{TranscriptJSON: `{"messages":[{"role":"user","content":"initial request"},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a","content":"first result"},{"type":"text","text":"later request"}]},{"role":"assistant","content_blocks":[{"type":"tool_use","id":"b","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"b","content":"second result"}]}]}`}},
	}
	for source, replay := range sources {
		for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
			t.Run(source+"_to_"+string(provider), func(t *testing.T) {
				history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: replay}}
				input, err := summaryHistoryInput(provider, history)
				if err != nil {
					t.Fatal(err)
				}
				text := summaryInputText(input)
				for _, expected := range []string{"initial request", "later request", "first result", "second result"} {
					if !strings.Contains(text, expected) {
						t.Fatalf("lost %q: %s", expected, text)
					}
				}
				counts := map[string]int{}
				for _, raw := range input {
					for _, id := range summaryToolIDs(raw) {
						counts[id]++
					}
				}
				if counts["a"] != 2 || counts["b"] != 2 {
					t.Fatalf("unpaired tool IDs: %v", counts)
				}
			})
		}
	}
}

func TestSummaryKeepsCompleteExchangeInNewerCheckpoint(t *testing.T) {
	pair := `{"type":"function_call","call_id":"a","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file content"}`
	old := models.Execution{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[` + pair + `]}`}}}
	latest := models.Execution{ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[` + pair + `,{"type":"message","role":"user","content":"continue"}]}`}}}
	input, err := summaryHistoryInput(models.ProviderOpenAI, []models.Execution{old, latest})
	if err != nil {
		t.Fatal(err)
	}
	if len(input) != 3 {
		t.Fatalf("expected the latest complete checkpoint, got %s", summaryInputText(input))
	}
	if !strings.Contains(summaryInputText(input), "file content") {
		t.Fatal("lost tool result")
	}
}

func TestSummaryProviderSwitchPreservesPartialAnthropicAnswer(t *testing.T) {
	history := []models.Execution{{Status: models.ExecFailed, Output: "partial answer after tools", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"messages":[{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}}]},{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"a","content":"file"}]}]}`}}}}
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		text := summaryHistoryText(t, provider, history)
		if strings.Count(text, "partial answer after tools") != 1 {
			t.Fatalf("%s lost or duplicated partial answer: %s", provider, text)
		}
	}
}
