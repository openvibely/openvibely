package service

import (
	"encoding/json"
	"errors"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// Preserve the destination's replay format without allowing hydration of the
// discarded executions. Even an empty window is a durable replacement boundary.
func recoveryWindowHistory(provider models.LLMProvider, input []json.RawMessage) []models.Execution {
	if input == nil {
		input = []json.RawMessage{}
	}
	var value any = input
	if provider == models.ProviderAnthropic {
		value = map[string]any{"messages": input}
	}
	raw, _ := json.Marshal(value)
	return []models.Execution{{Status: models.ExecCompleted, ContextKind: "provider_truncated", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
}

// Grow a chronological suffix backwards. A boundary may not split any linked
// tool calls/results, including parallel calls connected through one message.
func selectRecoveryWindow(req llmcontracts.AgentRequest, input []json.RawMessage) []json.RawMessage {
	first := map[string]int{}
	for i, raw := range input {
		for _, id := range summaryToolIDs(raw) {
			if _, ok := first[id]; !ok {
				first[id] = i
			}
		}
	}
	start := len(input)
	limit := calculateRequestBudget(req).SafeInputLimit
	if trigger := compactionLimitsForAgent(req.Agent).TriggerLimit; trigger > 0 && trigger < limit {
		limit = trigger
	}
	for start > 0 {
		next := start - 1
		for i := next; i >= next; i-- {
			for _, id := range summaryToolIDs(input[i]) {
				if first[id] < next {
					next = first[id]
				}
			}
		}
		candidate := req
		candidate.ChatHistory = recoveryWindowHistory(req.Agent.Provider, input[next:])
		if calculateRequestBudget(candidate).TotalInputTokens() >= limit {
			break
		}
		start = next
	}
	return input[start:]
}

// Provider switches use readable recent history directly, without a source-provider
// call. Destination native-compaction failures use the normal summary fallback.
func (s *LLMService) callProviderWithTruncatedRecovery(adapter ProviderAdapter, original, req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	req.NativeCompactionStateJSON = ""
	req.ProviderSessionStateJSON = ""
	req.ContextTokenEstimate = 0
	req.ForceNativeCompaction = false
	req.Agent.ForceNativeCompaction = false
	req.Ctx = llmcontracts.WithNativeCompactionStateJSON(req.Ctx, "")
	req.ChatHistory = normalizeProviderReplay(req.Agent.Provider, req.ChatHistory, false)
	input, err := summaryHistoryInput(req.Agent.Provider, req.ChatHistory)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	// Externalize oversized pending attachments before allocating history space.
	empty := req
	empty.ChatHistory = nil
	prepared, _, cleanup, err := s.preparePendingInputWithBudget(empty, calculateRequestBudget(empty))
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	defer cleanup()
	prepared.ChatHistory = req.ChatHistory
	req = prepared
	input = selectRecoveryWindow(req, input)
	limits := compactionLimitsForAgent(req.Agent)
	req.NativeCompactionTokenThreshold = limits.TriggerLimit
	req.Agent.CompactionThreshold = limits.TriggerLimit
	req = applyNativeCompactionSupport(req)
	for {
		if err := req.Ctx.Err(); err != nil {
			return llmcontracts.AgentResult{}, err
		}
		req.ChatHistory = recoveryWindowHistory(req.Agent.Provider, input)
		budget := calculateRequestBudget(req)
		if err := ensureRequestFitsWithBudget(req, budget, "provider switch"); err != nil {
			return llmcontracts.AgentResult{}, err
		}
		logContextDecisionWithBudget(original.ChatHistory, req, "provider_switch_truncation", false, nil, budget)
		res, err := adapter.Call(req)
		recordProviderRecovery(req, err)
		if err == nil {
			// Retry windows remain in memory until the call succeeds. A provider
			// may already have saved a newer checkpoint during this execution.
			if err := s.persistSuccessfulRecoveryWindow(original, req); err != nil {
				return res, err
			}
			s.persistNativeCompactionCheckpoint(req, res)
			return res, nil
		}
		err = categorizeProviderError(err)
		if !contextCompactionFallbackDisabled(req.Ctx) && providerSupportsNativeCompaction(req.Agent) && nativeCompactionFailure(err) && !recognizedContextLengthError(err) {
			// Preserve the saved boundary when the selected window has virtual IDs.
			recovery := req
			recovery.RestoredCompactionSourceExecutionID = original.RestoredCompactionSourceExecutionID
			if len(original.ChatHistory) > 0 && original.ChatHistory[len(original.ChatHistory)-1].ID != "" {
				recovery.RestoredCompactionSourceExecutionID = original.ChatHistory[len(original.ChatHistory)-1].ID
			}
			recovery = restoreCurrentSteeringForRecovery(recovery)
			return s.callNativeCompactionFallback(adapter, recovery, err)
		}
		// Only retry a rejected input, never restart a turn which produced output.
		if !recognizedContextLengthError(err) || len(input) == 0 || res.Output != "" || res.Usage.OutputTokens > 0 || recoveryExecutedTools(err, input) {
			return res, err
		}
		input = removeOldestSummaryMessage(input)
	}
}

// Provider clients may return an empty result on failure even after executing
// tools. Never replay such a turn from its original input.
func recoveryExecutedTools(err error, input []json.RawMessage) bool {
	var anthropic *anthropicclient.ConversationError
	var compatible *openaiclient.ConversationError
	var raw string
	if errors.As(err, &anthropic) {
		raw = anthropic.MessagesJSON
	} else if errors.As(err, &compatible) {
		raw = compatible.MessagesJSON
	} else {
		return false
	}
	known := map[string]bool{}
	for _, message := range input {
		for _, id := range summaryToolIDs(message) {
			known[id] = true
		}
	}
	var messages []json.RawMessage
	if json.Unmarshal([]byte(raw), &messages) != nil {
		return true
	}
	for _, message := range messages {
		for _, id := range summaryToolIDs(message) {
			if !known[id] {
				return true
			}
		}
	}
	return false
}

func (s *LLMService) persistSuccessfulRecoveryWindow(original, req llmcontracts.AgentRequest) error {
	if s != nil && s.execRepo != nil && req.ExecID != "" {
		if scope, ok := s.compactionScopeForRequest(req); ok {
			checkpoint, err := s.execRepo.GetChatCompactionCheckpoint(req.Ctx, scope.Type, scope.ID)
			if err != nil {
				return err
			}
			if checkpoint != nil && checkpoint.SourceExecutionID == req.ExecID {
				return nil
			}
		}
	}
	return s.persistCompactionCheckpoint(original, req, "", "provider_recovery")
}
