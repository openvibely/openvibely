package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/recovery"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
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
	if providerHistory == nil {
		if !recorded {
			return req
		}
		// Atomic steering commits supply Responses-shaped rows. Normalize that
		// committed snapshot into the same restoration path as client failures.
		snapshot, err := models.EncodeReplayRows("responses_input", items)
		if err != nil {
			return req
		}
		providerHistory = []models.Execution{{ID: req.ExecID, Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{snapshot}}}
		req.Ctx = llmcontracts.WithInitialSteeringCommit(req.Ctx, commit)
	}
	req.ChatHistory = append([]models.Execution(nil), providerHistory...)
	// The recorded history already includes the prompt and its attachments.
	req.Message = ""
	req.Attachments = nil
	req.NativeCompactionStateJSON = ""
	req.NativeCompactionStateRows = nil
	req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, "")
	req.Ctx = llmcontracts.WithHistoryContinuation(req.Ctx)
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
	var failure recovery.Failure
	if !errors.As(err, &failure) {
		return
	}
	original, decodeErr = failure.RecoveryRows()
	format := failure.RecoveryFormat()
	if decodeErr != nil {
		return
	}
	// Anthropic's wire content arrays use a different field in stored replay.
	// Convert that shape only; all providers retain one complete snapshot.
	if format == "messages" {
		converted := make([]json.RawMessage, len(original))
		for i, raw := range original {
			var wire struct {
				Role    string
				Content json.RawMessage
			}
			if json.Unmarshal(raw, &wire) != nil {
				return
			}
			message := anthropicclient.Message{Role: wire.Role}
			if json.Unmarshal(wire.Content, &message.Content) != nil {
				message.ContentBlocks = wire.Content
			}
			row, err := anthropicclient.EncodeReplayMessage(message)
			if err != nil {
				return
			}
			converted[i] = row
		}
		original = converted
	}
	state.mu.Lock()
	state.providerHistory = []models.Execution{{ID: req.ExecID, Status: models.ExecCompleted, ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows(format, original)}}}
	state.mu.Unlock()
}
