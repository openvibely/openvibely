package service

import (
	"context"

	"errors"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCompletionsRecoveryRetainsImagesThroughSummary(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAICompatible, models.ProviderOllama} {
		req := trackSteeringRecovery(llmcontracts.AgentRequest{Ctx: context.Background(), Agent: models.LLMConfig{Provider: provider}})
		raw := `[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,UPLOADED"}}]},{"role":"tool","content":"EDIT_COMPLETED","tool_call_id":"edit"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,RENDERED"}}]}]`
		recordProviderRecovery(req, &openaiclient.ConversationError{Err: errors.New("overflow"), MessagesJSON: raw})
		recovered := restoreCurrentSteeringForRecovery(req)
		summaryPrompt := buildLocalSummaryCompactionPrompt(recovered.ChatHistory)
		require.Contains(t, summaryPrompt, "EDIT_COMPLETED")
		require.NotContains(t, summaryPrompt, "base64,")
		compacted := buildCompactedReplacementHistory(recovered.ChatHistory, "edit done")
		images := retainedCompletionsImages(compacted)
		require.Len(t, images, 2)
		stores := teststores.New(t).Stores
		require.NoError(t, stores.Executions.UpsertChatCompactionCheckpoint(context.Background(), models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "images", History: compacted}))
		saved, err := stores.Executions.GetChatCompactionCheckpoint(context.Background(), "task", "images")
		require.NoError(t, err)
		images = retainedCompletionsImages(saved.History)
		require.Len(t, images, 2)
		require.Contains(t, images[0].ReplayMessages[0].TranscriptJSON, "UPLOADED")
		require.Contains(t, images[1].ReplayMessages[0].TranscriptJSON, "RENDERED")
	}
}
func TestFollowupAfterInflightSummaryRetainsRetryWork(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	svc := &LLMService{execRepo: stores.Executions}
	req := llmcontracts.AgentRequest{Ctx: ctx, ExecID: "active", TransportScope: "task:test", Agent: models.LLMConfig{Provider: models.ProviderOpenAI}, ChatHistory: []models.Execution{{ID: "active"}}}
	compacted := req
	compacted.ChatHistory = []models.Execution{{Output: "A completed", Status: models.ExecCompleted}}
	svc.persistCompactionCheckpoint(req, compacted, "A completed", "local_summary")
	checkpoint, err := stores.Executions.GetChatCompactionCheckpoint(ctx, "task", "test")
	require.NoError(t, err)
	require.Equal(t, "local_summary_inflight", checkpoint.Strategy)
	req.ExecID = "followup"
	req.ChatHistory = []models.Execution{{ID: "old", Output: "old"}, {ID: "active", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"assistant","content":"A completed"},{"type":"function_call_output","call_id":"B","output":"B completed after summary"}]}`}}}}
	restored := svc.restoreCompactionCheckpoint(req)
	require.Len(t, restored.ChatHistory, 1)
	require.Contains(t, buildLocalSummaryCompactionPrompt(restored.ChatHistory), "B completed after summary")
}
