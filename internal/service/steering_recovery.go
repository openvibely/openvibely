package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

type steeringRecoveryKey struct{}
type steeringRecovery struct {
	mu               sync.Mutex
	transcript       string
	commit           func(context.Context, []any) error
	anthropicHistory []models.Execution
}

// Track successful commits from this invocation, not an older database snapshot
// that cannot prove the current prompt was consumed.
func trackSteeringRecovery(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if req.Agent.Provider != models.ProviderOpenAI && req.Agent.Provider != models.ProviderAnthropic {
		return req
	}
	if req.Ctx == nil {
		req.Ctx = context.Background()
	}
	state := &steeringRecovery{}
	if req.Agent.Provider == models.ProviderAnthropic {
		req.Ctx = context.WithValue(req.Ctx, steeringRecoveryKey{}, state)
		return req
	}
	wrap := func(commit func(context.Context, []any) error) func(context.Context, []any) error {
		if commit == nil {
			return nil
		}
		var record func(context.Context, []any) error
		record = func(ctx context.Context, items []any) error {
			raw, err := json.Marshal(struct {
				Input []any `json:"responses_input"`
			}{items})
			if err != nil {
				return err
			}
			if err := commit(ctx, items); err != nil {
				return err
			}
			state.mu.Lock()
			state.transcript = string(raw)
			state.commit = record
			state.mu.Unlock()
			return nil
		}
		return record
	}
	req.Ctx = llmcontracts.WithInitialSteeringCommit(req.Ctx, wrap(llmcontracts.InitialSteeringCommitFromContext(req.Ctx)))
	if local := llmcontracts.LocalSteeringCallbackFromContext(req.Ctx); local != nil {
		req.Ctx = llmcontracts.WithLocalSteeringCallback(req.Ctx, func(ctx context.Context) (llmcontracts.LocalSteeringInput, error) {
			input, err := local(ctx)
			input.Commit = wrap(input.Commit)
			return input, err
		})
	}
	req.Ctx = context.WithValue(req.Ctx, steeringRecoveryKey{}, state)
	return req
}

func restoreCurrentSteeringForRecovery(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if req.Ctx == nil {
		return req
	}
	state, _ := req.Ctx.Value(steeringRecoveryKey{}).(*steeringRecovery)
	if state == nil {
		return req
	}
	state.mu.Lock()
	transcript := state.transcript
	commit := state.commit
	anthropicHistory := state.anthropicHistory
	state.mu.Unlock()
	if anthropicHistory != nil {
		req.ChatHistory = append([]models.Execution(nil), anthropicHistory...)
		req.Message = ""
		req.Attachments = nil
		req.NativeCompactionStateJSON = ""
		req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, "")
		req.Ctx = llmcontracts.WithHistoryContinuation(req.Ctx)
		return req
	}
	if transcript == "" {
		return req
	}
	req.ChatHistory = []models.Execution{{ID: req.ExecID, Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: transcript}}}}
	// The recorded history already includes the prompt and its attachments.
	req.Message = ""
	req.Attachments = nil
	req.NativeCompactionStateJSON = ""
	req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, "")
	req.Ctx = llmcontracts.WithHistoryContinuation(req.Ctx)
	req.Ctx = llmcontracts.WithInitialSteeringCommit(req.Ctx, commit)
	return req
}

// Capture each failed attempt, including a failed compacted retry, so recovery
// never restarts from the pre-call request after tools or steering advanced it.
func recordAnthropicRecovery(req llmcontracts.AgentRequest, err error) {
	if req.Agent.Provider != models.ProviderAnthropic || req.Ctx == nil {
		return
	}
	state, _ := req.Ctx.Value(steeringRecoveryKey{}).(*steeringRecovery)
	var failure *anthropicclient.ConversationError
	if state == nil || !errors.As(err, &failure) {
		return
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal([]byte(failure.MessagesJSON), &messages) != nil {
		return
	}
	history := make([]models.Execution, 0, len(messages))
	for _, message := range messages {
		execution := models.Execution{Status: models.ExecCompleted}
		var plain string
		if json.Unmarshal(message.Content, &plain) == nil {
			if message.Role == "user" {
				execution.PromptSent = plain
			} else {
				execution.Output = plain
			}
		} else {
			// Preserve structured tool/compaction blocks for the summarizer,
			// and retain user instructions separately even if its summary omits them.
			execution.Output = string(message.Content)
			if message.Role == "user" {
				var blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(message.Content, &blocks) == nil {
					for _, block := range blocks {
						if block.Type == "text" {
							execution.PromptSent += block.Text + "\n"
						}
					}
				}
			}
		}
		history = append(history, execution)
	}
	state.mu.Lock()
	state.anthropicHistory = history
	state.mu.Unlock()
}
