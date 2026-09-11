package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai_compatible "github.com/openvibely/openvibely/internal/llm/openai_compatible"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRequestUserInputToolWebOnlyRegistryAndHandler(t *testing.T) {
	webDefs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, false)
	require.Contains(t, handlerToolDefNames(webDefs), "request_user_input")
	for _, def := range webDefs {
		if def.Name != "request_user_input" {
			continue
		}
		require.Equal(t, string(chatcontrol.AccessWrite), string(def.Access))
		require.JSONEq(t, `{"type":"object","required":["questions"],"additionalProperties":false,"properties":{"questions":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"object","required":["id","question","options"],"additionalProperties":false,"properties":{"id":{"type":"string","description":"Stable question id used in the answer result."},"question":{"type":"string","description":"Plain-text question to show to the user."},"options":{"type":"array","minItems":2,"maxItems":3,"items":{"type":"object","required":["label","description"],"additionalProperties":false,"properties":{"label":{"type":"string","description":"Clickable option label returned exactly when selected."},"description":{"type":"string","description":"Short plain-text explanation shown under the label."}}}}}}}}}`, string(def.Parameters))
	}

	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceAPI, chatcontrol.SurfaceSlack, chatcontrol.SurfaceTelegram, chatcontrol.SurfaceEmail, chatcontrol.SurfaceDiscord, chatcontrol.SurfaceX} {
		require.NotContains(t, handlerToolDefNames(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, surface, false)), "request_user_input", "surface %s", surface)
	}
	require.NotContains(t, handlerToolDefNames(chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, false)), "request_user_input")

	tc := NewTestContext(t)
	require.NoError(t, chatcontrol.ValidateHandlerCoverage(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, false, tc.handler.chatActionHandlers(streamingResponseParams{}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)))
}

func TestRequestUserInputToolPublishesSSEAndReturnsAnswer(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Interactive answers").Build()
	cb := events.NewChatBroadcaster()
	tc.handler.SetChatBroadcaster(cb)
	sub, err := cb.SubscribeProject(project.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan struct {
		out string
		err error
	}, 1)
	input := json.RawMessage(`{"questions":[{"id":"create","question":"Should I create a task for this?","options":[{"label":"Create task","description":"Create the task now."},{"label":"Not now","description":"Do not create a task."}]}]}`)
	go func() {
		out, err := tc.handler.executeRequestUserInputTool(ctx, streamingResponseParams{ProjectID: project.ID, ExecID: "exec-1", Surface: chatcontrol.SurfaceWeb}, input)
		resultCh <- struct {
			out string
			err error
		}{out: out, err: err}
	}()

	var evt events.ChatEvent
	select {
	case evt = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for input request event")
	}
	require.Equal(t, events.ChatUserInputRequested, evt.Type)
	require.Equal(t, project.ID, evt.ProjectID)
	require.Equal(t, "exec-1", evt.ExecID)
	require.NotNil(t, evt.InputRequest)
	require.Len(t, evt.InputRequest.Questions, 1)
	require.Equal(t, "Should I create a task for this?", evt.InputRequest.Questions[0].Question)
	require.Equal(t, "Create task", evt.InputRequest.Questions[0].Options[0].Label)

	postInputRequestJSON(t, tc, "/chat/input-requests/"+evt.InputRequest.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"Create task"}]}`, http.StatusOK)

	select {
	case result := <-resultCh:
		require.NoError(t, result.err)
		require.JSONEq(t, `{"ok":true,"request_id":"`+evt.InputRequest.ID+`","answers":[{"question_id":"create","label":"Create task"}]}`, result.out)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool result")
	}
}

func TestRequestUserInputAnswerResumesProviderToolLoopAndAllowsAffirmativeCreateTask(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Interactive continuation")
	cb := events.NewChatBroadcaster()
	h.SetChatBroadcaster(cb)
	sub, err := cb.SubscribeProject(project.ID)
	require.NoError(t, err)
	defer cb.Unsubscribe(sub)

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch attempt {
		case 1:
			_, _ = w.Write([]byte(
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_input\",\"type\":\"function\",\"function\":{\"name\":\"request_user_input\",\"arguments\":\"{\\\"questions\\\":[{\\\"id\\\":\\\"create\\\",\\\"question\\\":\\\"Should I create a task for this?\\\",\\\"options\\\":[{\\\"label\\\":\\\"Create task\\\",\\\"description\\\":\\\"Create the task now.\\\"},{\\\"label\\\":\\\"Not now\\\",\\\"description\\\":\\\"Do not create a task.\\\"}]}]}\"}}]}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
		case 2:
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			messages := body["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			require.Equal(t, "tool", last["role"])
			require.Contains(t, last["content"], "Create task")
			_, _ = w.Write([]byte(
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_create\",\"type\":\"function\",\"function\":{\"name\":\"create_task\",\"arguments\":\"{\\\"title\\\":\\\"Approved follow-up task\\\",\\\"prompt\\\":\\\"Do the approved work\\\",\\\"category\\\":\\\"backlog\\\",\\\"priority\\\":2}\"}}]}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
		default:
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			messages := body["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			require.Equal(t, "tool", last["role"])
			require.Contains(t, last["content"], "Approved follow-up task")
			_, _ = w.Write([]byte(
				"data: {\"choices\":[{\"delta\":{\"content\":\"Created after affirmative answer.\"}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
					"data: [DONE]\n\n",
			))
		}
	}))
	defer srv.Close()

	agent := models.LLMConfig{
		Name: "Compatible input loop", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey,
		Model: "provider/model", APIKey: "sk-test", BaseURL: srv.URL + "/v1/", PresetSlug: "vllm", Transport: "chat_completions",
		CustomAuthConfigJSON: `{"enabled":true,"allow_private_endpoints":true}`,
	}

	answerDone := make(chan struct{}, 1)
	go func() {
		defer func() { answerDone <- struct{}{} }()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case evt := <-sub:
				if evt.Type != events.ChatUserInputRequested || evt.InputRequest == nil {
					continue
				}
				postInputRequestJSONToEcho(t, e, "/chat/input-requests/"+evt.InputRequest.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"Create task"}]}`, http.StatusOK)
				return
			case <-deadline:
				return
			}
		}
	}()

	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
	rt := &llmcontracts.RuntimeTools{Definitions: defs, Executor: h.chatActionExecutor(streamingResponseParams{ProjectID: project.ID, ExecID: "exec-loop", Agent: agent, ChatMode: models.ChatModeOrchestrate, Surface: chatcontrol.SurfaceWeb}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)}
	adapter := llmopenai_compatible.New(nil, nil)
	result, err := adapter.Call(llmcontracts.WithRuntimeTools(ctx, rt), llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: "Maybe create a task", Agent: agent, ExecID: "exec-loop", ProjectID: project.ID, ChatMode: models.ChatModeOrchestrate}, "")
	require.NoError(t, err)
	require.Contains(t, result.Output, "Created after affirmative answer.")
	require.Equal(t, int32(3), requests.Load())
	select {
	case <-answerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("answer goroutine did not observe input request")
	}
	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, string(models.CategoryBacklog))
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "Approved follow-up task", tasks[0].Title)
}

func TestChatInputRequestAnswerRejectsInvalidCrossProjectAndDuplicate(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Owner").Build()
	other := tc.CreateProject().WithName("Other").Build()
	pending, err := tc.handler.chatInputRequests.create(project.ID, "exec-2", []chatInputRequestQuestion{{ID: "create", Question: "Create it?", Options: []chatInputRequestOption{{Label: "Yes", Description: "Create it"}, {Label: "No", Description: "Skip it"}}}})
	require.NoError(t, err)

	postInputRequestJSON(t, tc, "/chat/input-requests/"+pending.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"Maybe"}]}`, http.StatusBadRequest)
	postInputRequestJSON(t, tc, "/chat/input-requests/"+pending.ID+"/answer", `{"project_id":"`+other.ID+`","answers":[{"question_id":"create","label":"Yes"}]}`, http.StatusForbidden)
	postInputRequestJSON(t, tc, "/chat/input-requests/"+pending.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"Yes"}]}`, http.StatusOK)
	postInputRequestJSON(t, tc, "/chat/input-requests/"+pending.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"No"}]}`, http.StatusGone)
}

func TestRequestUserInputCancellationAndTimeout(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Cancelled").Build()
	cb := events.NewChatBroadcaster()
	tc.handler.SetChatBroadcaster(cb)
	sub, err := cb.SubscribeProject(project.ID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := tc.handler.executeRequestUserInputTool(ctx, streamingResponseParams{ProjectID: project.ID, ExecID: "exec-cancel", Surface: chatcontrol.SurfaceWeb}, json.RawMessage(`{"questions":[{"id":"q","question":"Continue?","options":[{"label":"Yes","description":"Continue"},{"label":"No","description":"Stop"}]}]}`))
		resultCh <- err
	}()
	select {
	case <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for input request event")
	}
	cancel()
	select {
	case err := <-resultCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not return after cancellation")
	}

	broker := newChatInputRequestBroker()
	broker.now = func() time.Time { return time.Unix(100, 0) }
	pending, err := broker.create(project.ID, "exec-timeout", []chatInputRequestQuestion{{ID: "q", Question: "Continue?", Options: []chatInputRequestOption{{Label: "Yes", Description: "Continue"}, {Label: "No", Description: "Stop"}}}})
	require.NoError(t, err)
	broker.mu.Lock()
	pending.ExpiresAt = broker.now().Add(-time.Second)
	broker.mu.Unlock()
	_, err = broker.wait(context.Background(), pending.ID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestChatPageRendersInputRequestClientControls(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Render").Build()
	body := tc.HTTP().Get("/chat?project_id=" + project.ID).Execute().Body.String()
	require.Contains(t, body, "chat_user_input_requested")
	require.Contains(t, body, "renderChatInputRequest")
	require.Contains(t, body, "button[data-chat-input-option]")
	require.Contains(t, body, "textContent = option.description")
	require.NotContains(t, body, "innerHTML = option.description")
	require.NotContains(t, body, "innerHTML = question.question")
}

func handlerToolDefNames(defs []llmcontracts.RuntimeToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

func postInputRequestJSON(t *testing.T, tc *TestContext, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	return postInputRequestJSONToEcho(t, tc.echo, path, body, wantStatus)
}

func postInputRequestJSONToEcho(t *testing.T, e *echo.Echo, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, wantStatus, rec.Code, rec.Body.String())
	return rec
}
