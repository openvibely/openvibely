package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"strings"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

type steeringRecoveryKey struct{}
type steeringRecovery struct {
	mu              sync.Mutex
	responses       []any
	responsesSet    bool
	commit          func(context.Context, []any) error
	providerHistory []models.Execution
}

// Track successful commits from this invocation, not an older database snapshot
// that cannot prove the current prompt was consumed.
func trackSteeringRecovery(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if req.Agent.Provider != models.ProviderOpenAI && req.Agent.Provider != models.ProviderAnthropic && req.Agent.Provider != models.ProviderOllama && req.Agent.Provider != models.ProviderOpenAICompatible {
		return req
	}
	if req.Ctx == nil {
		req.Ctx = context.Background()
	}
	state := &steeringRecovery{}
	req.Ctx = llmcontracts.WithResponsesRecovery(req.Ctx, func(items []any) {
		state.mu.Lock()
		state.responses = items
		state.responsesSet = true
		state.mu.Unlock()
	})
	if req.Agent.Provider == models.ProviderAnthropic || req.Agent.Provider == models.ProviderOllama || req.Agent.Provider == models.ProviderOpenAICompatible {
		req.Ctx = context.WithValue(req.Ctx, steeringRecoveryKey{}, state)
		return req
	}
	wrap := func(commit func(context.Context, []any) error) func(context.Context, []any) error {
		if commit == nil {
			return nil
		}
		var record func(context.Context, []any) error
		record = func(ctx context.Context, items []any) error {
			if err := commit(ctx, items); err != nil {
				return err
			}
			state.mu.Lock()
			state.responses = transcript.FreezeItems(items)
			state.responsesSet = true
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
	items := state.responses
	recorded := state.responsesSet
	commit := state.commit
	providerHistory := state.providerHistory
	state.mu.Unlock()
	if providerHistory != nil {
		req.ChatHistory = append([]models.Execution(nil), providerHistory...)
		req.Message = ""
		req.Attachments = nil
		req.NativeCompactionStateJSON = ""
		req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, "")
		req.Ctx = llmcontracts.WithHistoryContinuation(req.Ctx)
		return req
	}
	if !recorded {
		return req
	}
	snapshot, err := models.EncodeReplayRows("responses_input", items)
	if err != nil {
		return req
	}
	req.ChatHistory = []models.Execution{{ID: req.ExecID, Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{snapshot}}}
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
func recordProviderRecovery(req llmcontracts.AgentRequest, err error) {
	if req.Ctx == nil {
		return
	}
	state, _ := req.Ctx.Value(steeringRecoveryKey{}).(*steeringRecovery)
	if state == nil {
		return
	}
	var original []json.RawMessage
	var decodeErr error
	var anthropicFailure *anthropicclient.ConversationError
	var completionsFailure *openaiclient.ConversationError
	if errors.As(err, &anthropicFailure) {
		original, decodeErr = anthropicFailure.RecoveryRows()
	} else if errors.As(err, &completionsFailure) {
		original, decodeErr = completionsFailure.RecoveryRows()
	} else {
		return
	}
	if decodeErr != nil {
		return
	}
	type recoveryMessage struct {
		Role               string
		Content, ToolCalls json.RawMessage
	}
	messages := make([]recoveryMessage, 0, len(original))
	for _, raw := range original {
		var m recoveryMessage
		if err := jsonview.Object(raw, func(k string, v json.RawMessage) error {
			switch strings.ToLower(k) {
			case "role":
				return json.Unmarshal(v, &m.Role)
			case "content":
				m.Content = v
			case "tool_calls":
				m.ToolCalls = v
			}
			return nil
		}); err != nil {
			return
		}
		messages = append(messages, m)
	}
	history := make([]models.Execution, 0, len(messages))
	for i, message := range messages {
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
			if completionsFailure != nil {
				var blocks []struct{ Type, Text string }
				if json.Unmarshal(message.Content, &blocks) == nil {
					var text strings.Builder
					for _, block := range blocks {
						if block.Type == "text" {
							text.WriteString(block.Text)
							text.WriteByte('\n')
						}
					}
					execution.Output = text.String()
				}
			}
			if message.Role == "user" {
				var blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(message.Content, &blocks) == nil {
					var text strings.Builder
					for _, block := range blocks {
						if block.Type == "text" {
							text.WriteString(block.Text)
							text.WriteByte('\n')
						}
					}
					execution.PromptSent = text.String()
				}
			}
		}
		if len(message.ToolCalls) > 0 && string(message.ToolCalls) != "null" {
			execution.Output += "\nTool calls: " + string(message.ToolCalls)
		}
		// Keep the provider's message shape for summary requests. The execution's
		// text fields retain user instructions and support text-only fallback paths.
		snapshot := models.ReplayFromRows("", []json.RawMessage{original[i]})
		if anthropicFailure != nil {
			replay := anthropicclient.Message{Role: message.Role}
			if json.Unmarshal(message.Content, &replay.Content) != nil {
				replay.ContentBlocks = message.Content
			}
			var encodeErr error
			var row []byte
			row, encodeErr = anthropicclient.EncodeReplayMessage(replay)
			snapshot = models.ReplayFromRows("messages", []json.RawMessage{row})
			if encodeErr != nil {
				return
			}
		}
		execution.ReplayMessages = []models.ExecutionReplayMessage{snapshot}
		history = append(history, execution)
	}
	// Recovery contains the conversation through the active execution. Record
	// that boundary so restoration does not append the covered executions again.
	if len(history) > 0 {
		history[len(history)-1].ID = req.ExecID
	}
	state.mu.Lock()
	state.providerHistory = history
	state.mu.Unlock()
}
