package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

func TestRuntimeDocumentFollowupReplaysSkillsAndReferences(t *testing.T) {
	ctx := attachmentcontext.With(context.Background(), "report.docx at /uploads/report.docx")
	ctx = managedcontext.With(ctx, "documents: summarize and edit Word documents. Reuse loaded instructions.")
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "document", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	phase, step, skillCalls, edits := 0, 0, 0, 0
	var indexPrefix []chatMessage
	workDir := t.TempDir()
	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view", Access: llmcontracts.RuntimeToolAccessRead}, {Name: "edit_file", Access: llmcontracts.RuntimeToolAccessWrite}}, Executor: func(_ context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		switch name {
		case "skill_view":
			skillCalls++
			return "Use the Word editor on the existing document reference", true, false, nil
		case "edit_file":
			require.Contains(t, string(input), "/uploads/report.docx")
			edits++
			return "Edited report.docx", true, false, nil
		}
		return "", false, false, nil
	}}
	ctx = llmcontracts.WithRuntimeTools(ctx, rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		data, _ := json.Marshal(req.Messages)
		require.Contains(t, string(data), "/uploads/report.docx")
		indexCount, indexAt := 0, -1
		for i, message := range req.Messages {
			if strings.HasPrefix(message.Content, managedcontext.Marker+"\n") {
				indexCount++
				indexAt = i
			}
		}
		require.Equal(t, 1, indexCount)
		if indexCount == 1 {
			if indexPrefix == nil {
				indexPrefix = append([]chatMessage(nil), req.Messages[1:indexAt+1]...)
			}
			require.Equal(t, indexPrefix, req.Messages[1:indexAt+1], "unchanged index prefix must survive tool calls and fresh-adapter followups")
		}
		result := chatMessage{Role: "assistant", Content: "Summary"}
		if phase == 0 && step == 0 {
			result.Content = ""
			result.ToolCalls = []ToolCall{{Function: ToolCallFunction{Name: "skill_view", Arguments: json.RawMessage(`{"handle":"documents"}`)}}}
		}
		if phase == 1 {
			require.Contains(t, string(data), "Use the Word editor")
			if step == 0 {
				result.Content = ""
				result.ToolCalls = []ToolCall{{Function: ToolCallFunction{Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"/uploads/report.docx","old_string":"old","new_string":"new"}`)}}}
			} else {
				result.Content = "Edited"
			}
		}
		if phase == 2 {
			result.Content = "2"
		}
		step++
		require.NoError(t, json.NewEncoder(w).Encode(chatResponse{Message: result, Done: true, EvalCount: 1}))
	}))
	defer server.Close()
	var history []models.Execution
	for phase = 0; phase < 3; phase++ {
		step = 0
		prompt := []string{"Summarize report.docx", "Edit that document", "1+1?"}[phase]
		exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt}
		require.NoError(t, stores.Executions.Create(ctx, exec))
		adapter := New(stores.Executions, nil)
		result, err := adapter.Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Agent: models.LLMConfig{Provider: models.ProviderOllama, Model: "test", OllamaBaseURL: server.URL, ContextWindow: 32768}, ExecID: exec.ID, Message: prompt, ChatHistory: history, ChatMode: models.ChatModeOrchestrate}, workDir, nil)
		require.NoError(t, err)
		replay, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
		require.NoError(t, err)
		exec.ReplayMessages = replay[exec.ID]
		exec.Output = result.Output
		history = append(history, *exec)
	}
	require.Equal(t, 1, skillCalls)
	require.Equal(t, 1, edits)
}

func TestRuntimeOverflowRetainsCompletedToolsForCompaction(t *testing.T) {
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "memory_view", Access: llmcontracts.RuntimeToolAccessRead}}, Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
		return "completed memory read " + strings.Repeat("large content ", 5000), true, false, nil
	}})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(chatResponse{Message: chatMessage{Role: "assistant", ToolCalls: []ToolCall{{Function: ToolCallFunction{Name: "memory_view", Arguments: json.RawMessage(`{"handle":"topic.md"}`)}}}}, Done: true})
	}))
	defer server.Close()
	_, err := New(nil, nil).Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Message: "Read the topic", Agent: models.LLMConfig{Provider: models.ProviderOllama, Model: "test", OllamaBaseURL: server.URL, ContextWindow: 4096}}, t.TempDir(), nil)
	require.Error(t, err)
	var failure *ConversationError
	require.ErrorAs(t, err, &failure)
	require.True(t, llmcontracts.ErrorIs(err, llmcontracts.ErrorContextWindowExceeded))
	require.Contains(t, failure.MessagesJSON, "completed memory read")
	require.Contains(t, failure.MessagesJSON, "tool_calls")
	require.Equal(t, 1, calls, "overflow must be caught before a second request")
}

func TestRuntimeIndexChangesDuringToolLoop(t *testing.T) {
	ctx := attachmentcontext.With(context.Background(), "report.docx at /uploads/report.docx")
	index := "documents: initial index"
	ctx = managedcontext.WithSource(ctx, func(context.Context) string { return index })
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "document", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	phase, step, skillCalls, edits := 0, 0, 0, 0
	var indexPrefix []chatMessage
	workDir := t.TempDir()
	rt := &llmcontracts.RuntimeTools{Filter: func(name string) (bool, bool) {
		if name == "edit_file" {
			return skillCalls > 0, true
		}
		return false, false
	}, Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "skill_view", Access: llmcontracts.RuntimeToolAccessRead}, {Name: "edit_file", Access: llmcontracts.RuntimeToolAccessWrite}}, Executor: func(_ context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		switch name {
		case "skill_view":
			skillCalls++
			index = "documents: updated index"
			return "Use the Word editor on the existing document reference", true, false, nil
		case "edit_file":
			require.Contains(t, string(input), "/uploads/report.docx")
			edits++
			return "Edited report.docx", true, false, nil
		}
		return "", false, false, nil
	}}
	ctx = llmcontracts.WithRuntimeTools(ctx, rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		toolsJSON, _ := json.Marshal(req.Tools)
		if skillCalls == 0 {
			require.NotContains(t, string(toolsJSON), "edit_file")
		} else {
			require.Contains(t, string(toolsJSON), "edit_file")
		}
		data, _ := json.Marshal(req.Messages)
		require.Contains(t, string(data), "/uploads/report.docx")
		indexCount, indexAt := 0, -1
		for i, message := range req.Messages {
			if strings.HasPrefix(message.Content, managedcontext.Marker+"\n") {
				indexCount++
				indexAt = i
			}
		}
		require.Equal(t, 1, indexCount)
		if indexCount == 1 {
			if indexPrefix == nil {
				indexPrefix = append([]chatMessage(nil), req.Messages[1:indexAt+1]...)
			}
			require.Equal(t, indexPrefix[:len(indexPrefix)-1], req.Messages[1:indexAt])
			require.Equal(t, managedcontext.Text(ctx), req.Messages[indexAt].Content)
		}
		result := chatMessage{Role: "assistant", Content: "Summary"}
		if phase == 0 && step == 0 {
			result.Content = ""
			result.ToolCalls = []ToolCall{{Function: ToolCallFunction{Name: "skill_view", Arguments: json.RawMessage(`{"handle":"documents"}`)}}}
		}
		if phase == 1 {
			require.Contains(t, string(data), "Use the Word editor")
			if step == 0 {
				result.Content = ""
				result.ToolCalls = []ToolCall{{Function: ToolCallFunction{Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"/uploads/report.docx","old_string":"old","new_string":"new"}`)}}}
			} else {
				result.Content = "Edited"
			}
		}
		if phase == 2 {
			result.Content = "2"
		}
		step++
		require.NoError(t, json.NewEncoder(w).Encode(chatResponse{Message: result, Done: true, EvalCount: 1}))
	}))
	defer server.Close()
	var history []models.Execution
	for phase = 0; phase < 3; phase++ {
		step = 0
		prompt := []string{"Summarize report.docx", "Edit that document", "1+1?"}[phase]
		exec := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: prompt}
		require.NoError(t, stores.Executions.Create(ctx, exec))
		adapter := New(stores.Executions, nil)
		result, err := adapter.Call(ctx, llmcontracts.AgentRequest{Operation: llmcontracts.OperationStreaming, Agent: models.LLMConfig{Provider: models.ProviderOllama, Model: "test", OllamaBaseURL: server.URL, ContextWindow: 32768}, ExecID: exec.ID, Message: prompt, ChatHistory: history, ChatMode: models.ChatModeOrchestrate}, workDir, nil)
		require.NoError(t, err)
		replay, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
		require.NoError(t, err)
		exec.ReplayMessages = replay[exec.ID]
		exec.Output = result.Output
		history = append(history, *exec)
	}
	require.Equal(t, 1, skillCalls)
	require.Equal(t, 1, edits)
}
