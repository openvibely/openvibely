package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

// All providers enter the same failure-snapshot restoration path. The only
// distinction is the wire shape carried by the snapshot.
func TestSharedRecoverySnapshotPreservesCurrentWork(t *testing.T) {
	for _, tc := range []struct {
		provider        models.LLMProvider
		format, payload string
	}{
		{models.ProviderOpenAI, "responses_input", `[{"type":"message","role":"user","content":"NEW_STEERING"},{"type":"function_call","call_id":"done","name":"edit_file","arguments":"{}"},{"type":"function_call_output","call_id":"done","output":"ALREADY_EDITED"}]`},
		{models.ProviderAnthropic, "messages", `[{"role":"user","content":"NEW_STEERING"},{"role":"assistant","content":[{"type":"tool_use","id":"done","name":"edit_file","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"done","content":"ALREADY_EDITED"}]}]`},
		{models.ProviderOpenAICompatible, "", `[{"role":"user","content":"NEW_STEERING"},{"role":"assistant","tool_calls":[{"id":"done","type":"function","function":{"name":"edit_file","arguments":"{}"}}]},{"role":"tool","tool_call_id":"done","content":"ALREADY_EDITED"}]`},
		{models.ProviderOllama, "", `[{"role":"user","content":"NEW_STEERING"},{"role":"tool","tool_call_id":"done","content":"ALREADY_EDITED"}]`},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			req := trackSteeringRecovery(llmcontracts.AgentRequest{Ctx: context.Background(), ExecID: "current", Message: "original", Agent: models.LLMConfig{Provider: tc.provider}, NativeCompactionStateJSON: "stale"})
			var rows []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(tc.payload), &rows))
			cause := errors.New("context length exceeded")
			err := recovery.WithSnapshot(cause, tc.format, rows, func(row json.RawMessage) ([]byte, error) { return row, nil })
			require.ErrorIs(t, err, cause)
			recordProviderRecovery(req, err)
			got := restoreCurrentSteeringForRecovery(req)
			require.Empty(t, got.Message)
			require.Empty(t, got.NativeCompactionStateJSON)
			require.True(t, llmcontracts.HistoryContinuationFromContext(got.Ctx))
			text := summaryHistoryText(t, tc.provider, got.ChatHistory)
			require.Contains(t, text, "NEW_STEERING")
			require.Contains(t, text, "ALREADY_EDITED")
		})
	}
}
