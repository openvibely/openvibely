package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

type recordingServer struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	keys   []string
	server *httptest.Server
}

func newRecordingServer(t *testing.T, respond func(n int) []string) *recordingServer {
	rs := &recordingServer{}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rs.mu.Lock()
		rs.bodies = append(rs.bodies, body)
		rs.paths = append(rs.paths, r.URL.Path)
		rs.keys = append(rs.keys, r.Header.Get("x-goog-api-key"))
		n := len(rs.bodies)
		rs.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range respond(n) {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	t.Cleanup(rs.server.Close)
	return rs
}

func (rs *recordingServer) adapter() *Adapter {
	a := New(nil, nil, nil)
	a.SetClientConfigurer(func(c *geminiclient.Client) { c.SetBaseURL(rs.server.URL) })
	return a
}

func geminiAgent() models.LLMConfig {
	return models.LLMConfig{Name: "Gemini", Provider: models.ProviderGemini, AuthMethod: models.AuthMethodAPIKey, APIKey: "g-key", Model: "gemini-3.8-flash"}
}

const doneChunk = `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2,"totalTokenCount":10}}`

func systemText(body map[string]any) string {
	si, _ := body["systemInstruction"].(map[string]any)
	raw, _ := json.Marshal(si)
	return string(raw)
}

func toolNames(body map[string]any) []string {
	var names []string
	tools, _ := body["tools"].([]any)
	for _, tool := range tools {
		decls, _ := tool.(map[string]any)["functionDeclarations"].([]any)
		for _, d := range decls {
			names = append(names, d.(map[string]any)["name"].(string))
		}
	}
	return names
}

func TestAdapterDirectAPIKey(t *testing.T) {
	rs := newRecordingServer(t, func(int) []string { return []string{doneChunk} })
	res, err := rs.adapter().Call(context.Background(), llmcontracts.AgentRequest{Operation: llmcontracts.OperationDirect, Message: "Say hi", Agent: geminiAgent()}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "Hello" || res.Usage.InputTokens != 8 || res.Usage.OutputTokens != 2 || res.Usage.TotalTokens != 10 {
		t.Fatalf("result %+v", res)
	}
	if rs.paths[0] != "/models/gemini-3.8-flash:streamGenerateContent" || rs.keys[0] != "g-key" {
		t.Fatalf("path=%q key=%q", rs.paths[0], rs.keys[0])
	}
	if len(toolNames(rs.bodies[0])) == 0 {
		t.Fatalf("expected default tools")
	}
}

func TestAdapterTaskUsesStatusInstructionsAndMaxTokens(t *testing.T) {
	rs := newRecordingServer(t, func(int) []string {
		return []string{`{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}]}`}
	})
	res, err := rs.adapter().Call(context.Background(), llmcontracts.AgentRequest{Operation: llmcontracts.OperationTask, Message: "Do work", Agent: geminiAgent()}, ".")
	if !llmcontracts.ErrorIs(err, llmcontracts.ErrorOutputTokenLimitReached) || res.StopReason != "max_tokens" {
		t.Fatalf("err=%v stop=%q", err, res.StopReason)
	}
	if !strings.Contains(systemText(rs.bodies[0]), "STATUS") {
		t.Fatalf("task system prompt lacks status instructions: %s", systemText(rs.bodies[0]))
	}
}

func TestAdapterZeroHistoryFollowupUsesChatAssembly(t *testing.T) {
	for _, history := range [][]models.Execution{nil, {}} {
		rs := newRecordingServer(t, func(int) []string { return []string{doneChunk} })
		_, err := rs.adapter().Call(context.Background(), llmcontracts.AgentRequest{
			Operation: llmcontracts.OperationStreaming, Message: "continue", Followup: true, ChatHistory: history, Agent: geminiAgent(),
		}, ".")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(systemText(rs.bodies[0]), "[STATUS: SUCCESS]") {
			t.Fatalf("follow-up used initial task framing")
		}
	}
}

func TestAdapterAgentDefinitionFiltersBuiltIns(t *testing.T) {
	rs := newRecordingServer(t, func(int) []string { return []string{doneChunk} })
	agentDef := &models.Agent{Tools: []string{"Read"}}
	_, err := rs.adapter().Call(context.Background(), llmcontracts.AgentRequest{Operation: llmcontracts.OperationTask, Message: "x", Agent: geminiAgent(), AgentDefinition: agentDef}, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Join(toolNames(rs.bodies[0]), ",")
	if !strings.Contains(names, "read_file") || strings.Contains(names, "bash") || strings.Contains(names, "write_file") {
		t.Fatalf("tools=%s", names)
	}
}

func TestAdapterOrchestrateRuntimeTools(t *testing.T) {
	rs := newRecordingServer(t, func(n int) []string {
		if n == 1 {
			return []string{`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"create_task","args":{"title":"t"}}}]},"finishReason":"STOP"}]}`}
		}
		return []string{doneChunk}
	})
	var called json.RawMessage
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task", Description: "create", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`), Access: llmcontracts.RuntimeToolAccessWrite}},
		Executor: func(_ context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if name != "create_task" {
				return "", false, false, nil
			}
			called = input
			return "created", true, false, nil
		},
	})
	_, err := rs.adapter().Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: "make a task", ChatMode: models.ChatModeOrchestrate, Agent: geminiAgent()}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(rs.bodies[0]); len(names) != 1 || names[0] != "create_task" {
		t.Fatalf("tools=%v", names)
	}
	if !strings.Contains(string(called), `"title"`) {
		t.Fatalf("runtime executor input=%s", called)
	}
	if !strings.Contains(systemText(rs.bodies[0]), "create_task") {
		t.Fatalf("system prompt lacks runtime tool guidance")
	}
}

func TestBuildHistoryReplaysNativeAndForeignRows(t *testing.T) {
	native, _ := ContentRows([]geminiclient.Content{
		geminiclient.TextContent("user", "hi"),
		{Role: "model", Parts: []geminiclient.Part{{FunctionCall: &geminiclient.FunctionCall{Name: "read_file", Args: json.RawMessage(`{}`)}, ThoughtSignature: "sig"}}},
	})
	foreignMessages := []openaiclient.CompletionsHistoryMessage{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}
	foreign, _ := openaiclient.PreparedCompletionsMessages(foreignMessages).SnapshotRows()
	history := BuildHistory([]models.Execution{
		{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows(ReplayItemsKey, native)}},
		{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("", foreign)}},
		{PromptSent: "legacy", Output: "answer", Status: models.ExecCompleted},
	})
	if len(history) != 6 {
		t.Fatalf("history=%+v", history)
	}
	if history[1].Parts[0].ThoughtSignature != "sig" || history[3].Role != "model" || history[5].Role != "model" {
		t.Fatalf("history=%+v", history)
	}
}

func TestCompletionsRoundTripPreservesToolPairs(t *testing.T) {
	contents := []geminiclient.Content{
		{Role: "model", Parts: []geminiclient.Part{{Text: "x"}, {FunctionCall: &geminiclient.FunctionCall{Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}}}},
		{Role: "user", Parts: []geminiclient.Part{{FunctionResponse: &geminiclient.FunctionResponse{Name: "bash", Response: map[string]any{"output": "ok"}}}}},
	}
	messages := CompletionsFromContents(contents)
	if len(messages) != 2 || messages[1].Role != "tool" || messages[1].ToolCallID != messages[0].ToolCalls[0].ID || messages[1].Content != "ok" {
		t.Fatalf("messages=%+v", messages)
	}
	back := ContentsFromCompletions(messages)
	if len(back) != 2 || back[1].Parts[0].FunctionResponse.Name != "bash" {
		t.Fatalf("back=%+v", back)
	}
}

func TestWebSearchAdapterPolicy(t *testing.T) {
	for _, mode := range []string{"task", "plan", "orchestrate", "disabled", "raw", "hook", "restricted", "summary"} {
		t.Run(mode, func(t *testing.T) {
			rs := newRecordingServer(t, func(int) []string { return []string{doneChunk} })
			req := llmcontracts.AgentRequest{Operation: llmcontracts.OperationDirect, Agent: geminiAgent(), Message: "hello"}
			switch mode {
			case "plan":
				req.Operation = llmcontracts.OperationStreaming
				req.ChatMode = models.ChatModePlan
			case "orchestrate":
				req.Operation = llmcontracts.OperationStreaming
				req.ChatMode = models.ChatModeOrchestrate
			case "disabled":
				req.DisableTools = true
			case "raw":
				req.RawDirectPrompt = true
			case "hook":
				req.LifecycleHookCall = true
			case "restricted":
				req.AgentDefinition = &models.Agent{Tools: []string{"Read"}}
			case "summary":
				req.SummaryInput = []json.RawMessage{json.RawMessage(`{"role":"user","content":"summarize"}`)}
			}
			_, err := rs.adapter().Call(context.Background(), req, ".")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, name := range toolNames(rs.bodies[0]) {
				found = found || name == "web_search"
			}
			want := mode == "task" || mode == "plan"
			if found != want {
				t.Fatalf("web_search present=%v want=%v", found, want)
			}
		})
	}
}
