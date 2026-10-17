package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/stretchr/testify/require"
)

const done = "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"

func config(endpoint string) models.LLMConfig {
	return models.LLMConfig{Provider: models.ProviderOllama, Model: "test", OllamaBaseURL: endpoint, ContextWindow: 32768, DefaultMaxTokens: 6000}
}

func TestSharedOperations(t *testing.T) {
	for _, name := range []string{"direct", "task", "chat", "followup-nil", "followup-empty", "task-unset", "followup-unset"} {
		t.Run(name, func(t *testing.T) {
			var wire map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/chat/completions", r.URL.Path)
				require.NoError(t, json.NewDecoder(r.Body).Decode(&wire))
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, done)
			}))
			defer srv.Close()
			req := contracts.AgentRequest{Operation: contracts.OperationStreaming, Message: "hello", Agent: config(srv.URL), ChatSystemContext: "selected-skill-sentinel"}
			switch name {
			case "direct":
				req.Operation = contracts.OperationDirect
			case "task", "task-unset":
				req.Operation = contracts.OperationTask
			case "chat":
				req.ChatMode = models.ChatModeOrchestrate
			default:
				req.Followup = true
				if name == "followup-empty" {
					req.ChatHistory = []models.Execution{}
				}
			}
			if strings.HasSuffix(name, "-unset") {
				req.Agent.ContextWindow = 0
				req.Agent.DefaultMaxTokens = 0
			}
			saved := req.Agent
			result, err := New(nil, nil).Call(context.Background(), req, t.TempDir(), nil)
			require.NoError(t, err)
			require.Contains(t, result.Output, "done")
			require.Equal(t, 10, result.Usage.InputTokens)
			require.Equal(t, saved, req.Agent)
			expectedOutput := 6000
			if strings.HasSuffix(name, "-unset") {
				expectedOutput = 2048
			}
			require.Equal(t, float64(expectedOutput), wire["max_tokens"])
			require.NotContains(t, wire, "options")
			require.NotContains(t, wire, "num_ctx")
			raw, _ := json.Marshal(wire["messages"])
			if req.Followup || name == "chat" {
				require.Contains(t, string(raw), "selected-skill-sentinel")
			}
		})
	}
}

func TestSkillAndImageToolsPersistAndReplay(t *testing.T) {
	env := teststores.New(t)
	ctx := attachment.WithToolImages(context.Background())
	task := &models.Task{ProjectID: "default", Title: "fixture", Prompt: "inspect", Category: models.CategoryActive, Status: models.StatusPending}
	require.NoError(t, env.Tasks.Create(ctx, task))
	agent, err := env.LLMConfigs.GetDefault(ctx)
	require.NoError(t, err)
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "inspect"}
	require.NoError(t, env.Executions.Create(ctx, exec))
	var imageBuffer bytes.Buffer
	require.NoError(t, png.Encode(&imageBuffer, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	imageBytes := imageBuffer.Bytes()
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	calls := 0
	var wires []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		wires = append(wires, string(raw))
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls <= 2 {
			name := "skill_view"
			if calls == 2 {
				name = "view_image"
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", calls, name)
		} else {
			io.WriteString(w, done)
		}
	}))
	defer srv.Close()
	executed := []string{}
	ctx = contracts.WithRuntimeTools(ctx, &contracts.RuntimeTools{
		SkipDefaultTools: true,
		Definitions: []contracts.RuntimeToolDefinition{
			{Name: "skill_view", Access: contracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "view_image", Access: contracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Executor: func(ctx context.Context, name string, args json.RawMessage) (string, bool, bool, error) {
			executed = append(executed, name)
			if name == "view_image" {
				out, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: "fixture.png", MediaType: "image/png", Data: imageBytes})
				return out, true, false, err
			}
			return "loaded skill instructions sentinel", true, false, nil
		},
	})
	adapter := New(env.Executions, nil)
	req := contracts.AgentRequest{Ctx: ctx, Operation: contracts.OperationTask, Message: "inspect", ExecID: exec.ID, Agent: config(srv.URL)}
	_, err = adapter.Call(ctx, req, t.TempDir(), nil)
	require.NoError(t, err)
	require.Equal(t, []string{"skill_view", "view_image"}, executed)
	require.Len(t, wires, 3)
	require.Contains(t, wires[1], `"tool_call_id":"call_1"`)
	require.Contains(t, wires[1], "loaded skill instructions sentinel")
	require.Contains(t, wires[2], "data:image/png;base64,"+encoded)
	require.NoError(t, env.Executions.Complete(ctx, exec.ID, models.ExecCompleted, "done", "", 0, 0))
	history, err := env.Executions.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	req.ExecID = ""
	req.Operation = contracts.OperationStreaming
	req.Followup = true
	req.Message = "follow up"
	req.ChatHistory = history
	_, err = adapter.Call(ctx, req, t.TempDir(), nil)
	require.NoError(t, err)
	require.Len(t, wires, 4)
	require.Contains(t, wires[3], `"tool_call_id":"call_1"`)
	require.Contains(t, wires[3], "loaded skill instructions sentinel")
	require.Contains(t, wires[3], "data:image/png;base64,"+encoded)
	require.Equal(t, 1, strings.Count(wires[3], "follow up"))
}

func TestSharedErrorsCancellationAndBudget(t *testing.T) {
	for _, name := range []string{"cancel", "error", "overflow", "output-limit"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if name == "error" {
					http.Error(w, `{"error":{"message":"unsupported model"}}`, 400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := contracts.AgentRequest{Operation: contracts.OperationDirect, Message: "hello", Agent: config(srv.URL), DisableTools: true, RawDirectPrompt: true}
			if name == "cancel" {
				cancel()
			}
			if name == "overflow" {
				req.Message = strings.Repeat("x", 200000)
			}
			_, err := New(nil, nil).Call(ctx, req, t.TempDir(), nil)
			require.Error(t, err)
			if name == "cancel" || name == "overflow" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			if name == "output-limit" {
				require.True(t, contracts.ErrorIs(err, contracts.ErrorOutputTokenLimitReached))
			}
		})
	}
}

func TestCancellationDuringStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	go func() { <-entered; cancel() }()
	_, err := New(nil, nil).Call(ctx, contracts.AgentRequest{Operation: contracts.OperationDirect, Message: "hello", Agent: config(srv.URL)}, t.TempDir(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}
