package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func TestCodexSteeringInitialInputAndFailureRecovery(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, configs)
	agent.Provider = models.ProviderOpenAI
	agent.APIKey, agent.AuthMethod, agent.Model = "test-key", models.AuthMethodAPIKey, "gpt-6-astra"
	project := createProject(t, h, "Codex steering")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
		task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
	})
	execution := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status, ex.PromptSent, ex.IsFollowup = models.ExecRunning, "original request", true
	})
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: execution.ID, Content: "new direction"}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, execution.ID))
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(64 << 20)
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		requests.Add(1)
		require.Contains(t, string(raw), "original request")
		require.NotContains(t, string(raw), "new direction")
		stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
		require.NoError(t, err)
		require.Equal(t, execution.ID, stored.ExpectedTurnID, "early steer must remain queued until the model boundary")
		require.NoError(t, conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"first","status":"completed","output":[]}}`)))
		_, raw, err = conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		requests.Add(1)
		var request map[string]any
		require.NoError(t, json.Unmarshal(raw, &request))
		require.Equal(t, "response.create", request["type"])
		require.Contains(t, string(raw), "new direction")
		stored, err = h.threadInputRepo.GetByID(ctx, input.ID)
		require.NoError(t, err)
		require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
		require.NoError(t, conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"bad_request","message":"model failed after consuming steering"}}`)))
	}))
	defer srv.Close()
	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()
	h.processStreamingResponse(streamingResponseParams{ExecID: execution.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent, Message: "original request", IsTaskFollowup: true, suppressQueuedTurnPromotion: true})
	require.EqualValues(t, 2, requests.Load())
	stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	require.Equal(t, models.ThreadInputModeSteering, stored.InputMode)
	replay, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
	require.NoError(t, err)
	require.Len(t, replay[execution.ID], 1)
	require.Contains(t, replay[execution.ID][0].TranscriptJSON, "new direction")
}
