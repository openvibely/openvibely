package service

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSummaryAnthropicConversionGroupsParallelResults(t *testing.T) {
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
	}
	for source, transcript := range sources {
		t.Run(source, func(t *testing.T) {
			history := []models.Execution{{Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
			input, err := summaryHistoryInput(models.ProviderAnthropic, history)
			require.NoError(t, err)
			expected := []string{
				`{"role":"assistant","content_blocks":[{"type":"tool_use","id":"a","name":"read_file","input":{}},{"type":"tool_use","id":"b","name":"read_file","input":{}}]}`,
				`{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"b","content":"file B"},{"type":"tool_result","tool_use_id":"a","content":"file A"}]}`,
				`{"role":"user","content_blocks":[{"type":"text","text":"continue"}]}`,
				`{"role":"assistant","content_blocks":[{"type":"tool_use","id":"c","name":"read_file","input":{}}]}`,
				`{"role":"user","content_blocks":[{"type":"tool_result","tool_use_id":"c","content":"file C"}]}`,
			}
			require.Len(t, input, len(expected))
			for i := range expected {
				// Empty text fields are an internal Message serialization detail.
				var message map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(input[i], &message))
				delete(message, "content")
				raw, err := json.Marshal(message)
				require.NoError(t, err)
				require.JSONEq(t, expected[i], string(raw))
			}
		})
	}
}
