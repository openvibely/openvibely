package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/agenttools"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
	"github.com/stretchr/testify/require"
)

// Scripted HTTP responses exercise actual serialization, tool execution and durable
// replay; they are not evidence of a live model's skill-selection quality.
func TestKnowledgeReplayAfterCancellationAndCompaction(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "Document", Category: models.CategoryChat, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	dir := t.TempDir()
	document := filepath.Join(dir, "report.md")
	require.NoError(t, os.WriteFile(document, []byte("Paver report"), 0600))
	skill := filepath.Join(dir, "SKILL.md")
	require.NoError(t, os.WriteFile(skill, []byte("Document skill: reuse the attached report path and edit the existing file."), 0600))
	catalog := agentskills.NewCatalog("test", []agentskills.Entry{{Handle: "documents", Skill: "documents", Source: agentskills.SourceProject, AbsolutePath: skill}})
	skills := agentskills.SelectedSkillRuntimeTools(catalog)
	ctx = attachmentcontext.With(ctx, "report.md at "+document)
	ctx = managedcontext.With(ctx, "documents: edit documents")
	phase, step, loads := 0, 0, 0
	var cancel context.CancelFunc
	rt := &llmcontracts.RuntimeTools{Definitions: append(skills.Definitions, llmcontracts.RuntimeToolDefinition{Name: "edit_file", Access: llmcontracts.RuntimeToolAccessWrite}), Executor: func(callCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if name == "skill_view" {
			out, handled, isErr, err := skills.Executor(callCtx, name, input)
			loads++
			if phase == 0 {
				cancel()
			}
			return out, handled, isErr, err
		}
		if name == "edit_file" {
			out, err := agenttools.Execute(callCtx, dir, name, input)
			return out, true, err != nil, err
		}
		return "", false, false, nil
	}}
	ctx = llmcontracts.WithRuntimeTools(ctx, rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		all, _ := json.Marshal(body.Messages)
		// Every fresh request retains the attachment locator even when compaction drops replay.
		require.Contains(t, string(all), document)
		if phase == 1 {
			require.Contains(t, string(all), "Document skill: reuse")
			require.Contains(t, string(all), `tool_result`)
			require.Contains(t, string(all), "skill-call")
		}
		if phase == 3 && step == 0 {
			require.NotContains(t, string(all), "Document skill: reuse")
			require.Contains(t, string(all), "updated index")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"reply\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":10}}}\n\n")
		name, id, input := "", "", json.RawMessage(`{}`)
		if (phase == 0 || phase == 3) && step == 0 {
			name, id, input = "skill_view", "skill-call", json.RawMessage(`{"handle":"documents"}`)
		}
		if phase == 1 && step == 0 {
			name, id, input = "edit_file", "edit-call", json.RawMessage(`{"file_path":"report.md","old_string":"Paver","new_string":"Fin"}`)
		}
		stop := "end_turn"
		if name != "" {
			block, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}})
			fmt.Fprintf(w, "data: %s\n\n", block)
			stop = "tool_use"
		} else {
			fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Done\"}}\n\n")
		}
		fmt.Fprintf(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":3}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
		step++
	}))
	defer server.Close()
	old := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	t.Cleanup(func() { anthropicclient.AnthropicAPIHost = old })
	var history []models.Execution
	for phase = 0; phase < 4; phase++ {
		step = 0
		if phase == 3 {
			history = []models.Execution{{PromptSent: "Compacted conversation", Output: "Report summarized and edited", Status: models.ExecCompleted}}
			ctx = managedcontext.With(ctx, "documents: updated index")
		}
		prompt := []string{"Summarize the attached document", "Replace Paver with Fin in that document", "1+1?", "Edit the report again"}[phase]
		execution := &models.Execution{TaskID: task.ID, PromptSent: prompt, Status: models.ExecRunning}
		require.NoError(t, stores.Executions.Create(ctx, execution))
		callCtx, stop := context.WithCancel(ctx)
		cancel = stop
		result, err := New(nil, stores.Executions, nil).Call(callCtx, llmcontracts.AgentRequest{ExecID: execution.ID, Operation: llmcontracts.OperationStreaming, Message: prompt, ChatHistory: history, Followup: phase > 0, ChatMode: models.ChatModeOrchestrate, Agent: models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", Model: "claude-opus-5"}}, dir, nil)
		stop()
		if phase == 0 {
			require.ErrorIs(t, err, context.Canceled)
			execution.Status = models.ExecCancelled
		} else {
			require.NoError(t, err)
			execution.Status = models.ExecCompleted
		}
		saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{execution.ID})
		require.NoError(t, err)
		execution.ReplayMessages = saved[execution.ID]
		execution.Output = result.Output
		history = append(history, *execution)
		if phase == 2 {
			require.Equal(t, 1, loads, "arithmetic and followup reuse must not reload the skill")
		}
	}
	data, err := os.ReadFile(document)
	require.NoError(t, err)
	require.Equal(t, "Fin report", strings.TrimSpace(string(data)))
	require.Equal(t, 2, loads, "reload the individual skill only after compaction removed it")
}
