package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type chatPagingStore struct {
	persistence.ExecutionStore
	pages int
}

func (s *chatPagingStore) ListChatModelHistoryBefore(ctx context.Context, project, current, after, before string, limit int) ([]models.Execution, error) {
	s.pages++
	return s.ExecutionStore.ListChatModelHistoryBefore(ctx, project, current, after, before, limit)
}

func TestChatReplayHistorySharedAcrossProviders(t *testing.T) {
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		for _, mode := range []string{"complete", "checkpoint-covered", "checkpoint-outside-window", "reset", "deleted"} {
			t.Run(string(provider)+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				stores := teststores.New(t).Stores
				config := models.LLMConfig{ID: "config", Provider: provider, Model: "fixture", ContextWindow: 100000000, ProviderContextWindow: 100000000}
				var history []models.Execution
				for i := 0; i < 66; i++ {
					task := models.Task{ProjectID: "default", Title: fmt.Sprintf("chat-%d", i), Category: models.CategoryChat, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(ctx, &task))
					e := models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("CHAT_TURN_%02d", i), StartsNewContext: mode == "reset" && i == 10}
					if i == 65 {
						e.Status = models.ExecRunning
					}
					require.NoError(t, stores.Executions.Create(ctx, &e))
					row := map[string]any{"role": "user", "content": e.PromptSent}
					var replay any = []any{row}
					if provider == models.ProviderOpenAI {
						replay = map[string]any{"responses_delta": []any{row}}
					}
					if provider == models.ProviderAnthropic {
						replay = map[string]any{"messages": []any{row}}
					}
					raw, err := json.Marshal(replay)
					require.NoError(t, err)
					require.NoError(t, stores.Executions.ReplaceReasoningReplay(ctx, e.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}))
					history = append(history, e)
				}
				if mode == "deleted" {
					require.NoError(t, stores.Tasks.Delete(ctx, history[2].TaskID))
				}
				store := &chatPagingStore{ExecutionStore: stores.Executions}
				svc := &LLMService{execRepo: store}
				req := contracts.AgentRequest{Ctx: withoutContextCompactionFallback(ctx), Agent: config, ProjectID: "default", ExecID: history[65].ID, TransportScope: "chat:project:default", Operation: contracts.OperationStreaming, Message: "next", ChatHistory: history[15:65]}
				start := 0
				if mode == "reset" {
					start = 10
				}
				if strings.HasPrefix(mode, "checkpoint-") {
					boundary := 64
					if mode == "checkpoint-outside-window" {
						boundary = 5
					}
					start = boundary + 1
					require.NoError(t, store.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{ScopeType: "chat_project", ScopeID: "default", ModelConfigID: config.ID, CompatibilityKey: providerCompatibilityKey(config), SourceExecutionID: history[boundary].ID, Strategy: "local_summary", History: []models.Execution{{ContextKind: "summary", PromptSent: "saved summary"}}}))
				}
				called := false
				_, err := svc.callProviderWithCompaction(providerAdapterFunc(func(got contracts.AgentRequest) (contracts.AgentResult, error) {
					called = true
					var text strings.Builder
					for _, e := range got.ChatHistory {
						for _, r := range e.ReplayMessages {
							text.WriteString(r.FullTranscriptJSON())
						}
					}
					for i := 0; i < 65; i++ {
						marker := fmt.Sprintf("CHAT_TURN_%02d", i)
						if i < start || (mode == "deleted" && i == 2) {
							require.NotContains(t, text.String(), marker)
						} else {
							require.Equal(t, 1, strings.Count(text.String(), marker), marker)
						}
					}
					return contracts.AgentResult{}, nil
				}), req)
				require.NoError(t, err)
				require.True(t, called)
				if mode == "checkpoint-covered" {
					require.Zero(t, store.pages)
				} else {
					require.Positive(t, store.pages)
				}
			})
		}
	}
}

func TestChatModelPagingRespectsEmailSession(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	var own, all []models.Execution
	for i := 0; i < 8; i++ {
		task := models.Task{ProjectID: "default", Title: fmt.Sprintf("email-%d", i), Category: models.CategoryChat, Status: models.StatusCompleted}
		require.NoError(t, stores.Tasks.Create(ctx, &task))
		session := "other"
		if i%2 == 1 {
			session = "own"
		}
		require.NoError(t, stores.EmailTaskContexts.Upsert(ctx, &models.EmailTaskContext{TaskID: task.ID, EmailSessionKey: session, EmailFrom: "test@example.invalid", EmailMessageID: fmt.Sprintf("message-%d", i)}))
		e := models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("email-%d", i)}
		require.NoError(t, stores.Executions.Create(ctx, &e))
		all = append(all, e)
		if session == "own" {
			own = append(own, e)
		}
	}
	rows, err := stores.Executions.ListChatModelHistoryBefore(persistence.WithExecutionMetadata(ctx), "default", own[3].ID, own[1].ID, own[3].ID, 100)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, own[1].ID, rows[0].ID)
	require.Equal(t, own[2].ID, rows[1].ID)
	require.Empty(t, rows[0].PromptSent, "paging reads metadata only")
	rows, err = stores.Executions.ListChatModelHistoryBefore(ctx, "different-project", all[7].ID, "", all[7].ID, 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}
