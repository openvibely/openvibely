package service

import (
	"context"
	"encoding/json"
	"sync"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

type steeringRecoveryKey struct{}
type steeringRecovery struct {
	mu         sync.Mutex
	transcript string
	commit     func(context.Context, []any) error
}

// Track successful commits from this invocation, not an older database snapshot
// that cannot prove the current prompt was consumed.
func trackSteeringRecovery(req llmcontracts.AgentRequest) llmcontracts.AgentRequest {
	if req.Agent.Provider != models.ProviderOpenAI {
		return req
	}
	if req.Ctx == nil {
		req.Ctx = context.Background()
	}
	state := &steeringRecovery{}
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
	state.mu.Unlock()
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
