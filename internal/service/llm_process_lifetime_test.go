package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/executionenv"
	"github.com/stretchr/testify/require"
)

func TestLLMExecutionCancellationStopsShell(t *testing.T) {
	for _, followup := range []bool{false, true} {
		name := "task"
		if followup {
			name = "followup"
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t)
			svc := NewLLMService(env.LLMConfigs, env.Executions, env.Tasks, env.Projects, env.Schedules, env.Attachments)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			shell, err := executionenv.Resolve()
			require.NoError(t, err)
			command := "sleep 30"
			if shell.Name == "powershell" {
				command = "Start-Sleep -Seconds 30"
			}
			if shell.Name == "cmd" {
				command = "ping -n 31 127.0.0.1 >nul"
			}
			var session string
			svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderTest: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
				input, _ := json.Marshal(map[string]any{"command": command, "yield_time_ms": 250})
				out, err := agenttools.Execute(req.Ctx, dir, "shell", input)
				require.NoError(t, err)
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "session_id: ") {
						session = strings.TrimPrefix(line, "session_id: ")
					}
				}
				require.NotEmpty(t, session)
				return llmcontracts.AgentResult{Output: "done", TextOnlyOutput: "done"}, nil
			})}
			agent := ensureDefaultAgent(t, env.LLMConfigs)
			agent.Provider = models.ProviderTest
			if followup {
				_, err = svc.CallAgentDirectStreamingDetailed(ctx, "hello", nil, *agent, "exec-process", nil, "", dir, nil, true)
			} else {
				task := &models.Task{ProjectID: "default", Title: "process", Prompt: "test", Category: models.CategoryActive, Status: models.StatusPending}
				require.NoError(t, env.Tasks.Create(ctx, task))
				_, err = svc.ExecuteTaskWithAgent(ctx, *task, *agent)
			}
			require.NoError(t, err)
			cancel()
			pollCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			input, _ := json.Marshal(map[string]any{"session_id": session})
			out, err := agenttools.Execute(pollCtx, dir, "process", input)
			require.NoError(t, err)
			require.Contains(t, out, "status: completed")
		})
	}
}

func TestLifecycleHookProcessCleanup(t *testing.T) {
	for _, after := range []bool{false, true} {
		name := "before_run"
		when := models.LifecycleBeforeRun
		if after {
			name = "after_complete"
			when = models.LifecycleAfterComplete
		}
		t.Run(name, func(t *testing.T) {
			owner, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			shell, err := executionenv.Resolve()
			require.NoError(t, err)
			command := "sleep 30"
			if shell.Name == "powershell" {
				command = "Start-Sleep -Seconds 30"
			}
			if shell.Name == "cmd" {
				command = "ping -n 31 127.0.0.1 >nul"
			}
			sessions := make(chan string, 1)
			store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "process-hook", When: when, SkillKey: name, OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true}}}
			worker := NewWorkerService(nil, 0, nil)
			worker.SetLifecycleRunner(lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, _ models.AgentLifecycleHook, _ lifecycle.HookInput) (json.RawMessage, error) {
				input, _ := json.Marshal(map[string]any{"command": command, "yield_time_ms": 250})
				out, err := agenttools.Execute(ctx, dir, "shell", input)
				if err != nil {
					return nil, err
				}
				sessions <- out
				return contextBlockPayload("prepared"), nil
			}), nil))
			turn := worker.PrepareLifecycleTurn(owner, models.Task{ID: "process-task"})
			if after {
				turn.AfterComplete(nil, llmcontracts.ChatContext{})
			}
			var out string
			select {
			case out = <-sessions:
			case <-time.After(5 * time.Second):
				t.Fatal("hook did not launch process")
			}
			var session string
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "session_id: ") {
					session = strings.TrimPrefix(line, "session_id: ")
				}
			}
			require.NotEmpty(t, session)
			// Before-run sessions survive the hook returning, until the task stops.
			// After-complete sessions stop when their detached hook work finishes.
			if !after {
				cancel()
			}
			poll, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			input, _ := json.Marshal(map[string]any{"session_id": session})
			out, err = agenttools.Execute(poll, dir, "process", input)
			require.NoError(t, err)
			require.Contains(t, out, "status: completed")
		})
	}
}
