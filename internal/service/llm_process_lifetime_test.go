package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
