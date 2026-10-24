package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestExecutionEnvironmentSurvivesFreshAdaptersAndDirectoryChanges(t *testing.T) {
	ctx := context.Background()
	db := teststores.New(t)
	repo := db.Executions
	task := &models.Task{ProjectID: "default", Title: "environment replay", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, db.Tasks.Create(ctx, task))
	requests := make(chan []map[string]any, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		requests <- body.Messages
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"id":"test","model":"test","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	oldHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	t.Cleanup(func() { anthropicclient.AnthropicAPIHost = oldHost })
	firstDir, secondDir := t.TempDir(), t.TempDir()
	var history []models.Execution
	var previous []map[string]any
	for turn, dir := range []string{firstDir, secondDir, secondDir} {
		id := fmt.Sprintf("environment-%d", turn)
		err := repo.Create(ctx, &models.Execution{ID: id, TaskID: task.ID, Status: models.ExecRunning, PromptSent: "question"})
		require.NoError(t, err)
		// Rehydrate exactly as the service does before budgeting. No client or
		// adapter instance survives from the previous request.
		var ids []string
		for _, execution := range history {
			ids = append(ids, execution.ID)
		}
		replay, err := db.Executions.ReplayMessagesByExecutionIDs(ctx, ids)
		require.NoError(t, err)
		for i := range history {
			history[i].ReplayMessages = replay[history[i].ID]
		}
		adapter := New(nil, db.Executions, nil)
		result, err := adapter.Call(ctx, llmcontracts.AgentRequest{Ctx: ctx, ExecID: id, Operation: llmcontracts.OperationStreaming, Message: "question", ChatHistory: history, ChatMode: models.ChatModeOrchestrate, Agent: models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: "claude-opus-5"}}, dir, nil)
		require.NoError(t, err)
		messages := <-requests
		if turn > 0 {
			require.True(t, reflect.DeepEqual(previous, messages[:len(previous)]), "earlier request prefix changed")
		}
		var environments []string
		for _, m := range messages {
			if text, ok := m["content"].(string); ok && strings.HasPrefix(text, executionenv.Marker) {
				environments = append(environments, text)
			}
		}
		require.Equal(t, executionenv.Context(firstDir), environments[0])
		if turn > 0 {
			require.Equal(t, []string{executionenv.Context(firstDir), executionenv.Context(secondDir)}, environments)
		} else {
			require.Len(t, environments, 1)
		}
		require.Equal(t, "question", messages[len(messages)-1]["content"])
		saved, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{id})
		require.NoError(t, err)
		require.Len(t, saved[id], 1)
		require.Equal(t, "question", saved[id][0].UserContent)
		require.Equal(t, "answer", saved[id][0].AssistantContent, "final metadata must be written alongside the transcript")
		var stored anthropicEnvironmentReplay
		require.NoError(t, json.Unmarshal([]byte(saved[id][0].TranscriptJSON), &stored))
		require.Equal(t, executionenv.Context(dir), stored.Environment)
		require.Contains(t, string(stored.Messages[1].ContentBlocks), "answer")
		history = append(history, models.Execution{ID: id, PromptSent: "question", Output: result.TextOnlyOutput, Status: models.ExecCompleted})
		previous = messages
	}
}

func TestReplayImageBudgetDoesNotCountBase64AsText(t *testing.T) {
	for _, size := range []int{100, 1000000} {
		blocks := json.RawMessage(fmt.Sprintf(`[{"type":"text","text":"question"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"%s"}}]`, strings.Repeat("A", size)))
		saved, err := json.Marshal(anthropicEnvironmentReplay{Messages: []anthropicclient.Message{{Role: "user", Content: "question", ContentBlocks: blocks}}})
		require.NoError(t, err)
		history := []models.Execution{{Status: models.ExecCompleted, PromptSent: "question", ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "question", TranscriptJSON: string(saved)}}}}
		tokens := EstimateHistoryTokens(history)
		require.GreaterOrEqual(t, tokens, 2000)
		require.Less(t, tokens, 2100, "image bytes must not inflate the history estimate")
	}
}

func TestEnvironmentReplayBudgetCountsChangesOnce(t *testing.T) {
	first, second := executionenv.Context(t.TempDir()), executionenv.Context(t.TempDir())
	var history []models.Execution
	for _, environment := range []string{first, second, second} {
		saved, err := json.Marshal(anthropicEnvironmentReplay{Environment: environment, Messages: []anthropicclient.Message{{Role: "user", Content: "question"}, {Role: "assistant", Content: "answer"}}})
		require.NoError(t, err)
		history = append(history, models.Execution{Status: models.ExecCompleted, PromptSent: "question", Output: "answer", ReplayMessages: []models.ExecutionReplayMessage{{UserContent: "question", AssistantContent: "answer", TranscriptJSON: string(saved)}}})
	}
	want := tokenestimate.FromText(first) + tokenestimate.FromText(second) + 3*(tokenestimate.FromText("question")+tokenestimate.FromText("answer"))
	require.Equal(t, want, EstimateHistoryTokens(history))
}
