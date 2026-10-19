package service

import (
	"encoding/json"
	"fmt"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func hasOpaqueCompaction(history []models.Execution) bool {
	for _, exec := range llmopenai.EffectiveReplayHistory(history) {
		for _, replay := range exec.ReplayMessages {
			var saved struct {
				Input []struct{ Type string } `json:"responses_input"`
			}
			if json.Unmarshal([]byte(replay.TranscriptJSON), &saved) != nil {
				continue
			}
			for _, item := range saved.Input {
				if item.Type == "compaction" {
					return true
				}
			}
		}
	}
	return false
}

// Use the existing execution transcript when another provider cannot read an
// OpenAI compaction item. Explicit messages preserve tool-result text that the
// ordinary prompt/answer fallback cleans out of execution output.
func readableCompactedHistory(history []models.Execution) ([]models.Execution, error) {
	if !hasOpaqueCompaction(history) {
		return history, nil
	}
	out := append([]models.Execution(nil), history...)
	found := false
	// Only the prefix covered by opaque state needs text recovery. Later turns
	// can still use their existing structured replay (including images/tools).
	lastOpaque := 0
	for i := range out {
		if hasOpaqueCompaction(out[i : i+1]) {
			lastOpaque = i
		}
	}
	for i := range out {
		if i > lastOpaque {
			continue
		}
		exec := &out[i]
		var messages []openaiclient.CompletionsHistoryMessage
		if exec.PromptSent != "" {
			messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "user", Content: exec.PromptSent})
		}
		if exec.Output != "" {
			messages = append(messages, openaiclient.CompletionsHistoryMessage{Role: "assistant", Content: exec.Output})
		}
		if len(messages) > 0 {
			found = true
		}
		raw, err := json.Marshal(messages)
		if err != nil {
			return nil, err
		}
		exec.ReplayMessages = []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}
	}
	if !found {
		return nil, fmt.Errorf("cannot switch providers: readable history for native OpenAI compaction is unavailable")
	}
	return out, nil
}

// Load older execution text only for opaque OpenAI history. Stop at an explicit
// new-context boundary; never create another durable copy of this history.
func (s *LLMService) recoverPortableProviderHistory(req llmcontracts.AgentRequest) (llmcontracts.AgentRequest, error) {
	if !hasOpaqueCompaction(req.ChatHistory) {
		return req, nil
	}
	history := req.ChatHistory
	if s != nil && s.execRepo != nil {
		if scope, ok := s.compactionScopeForRequest(req); ok {
			var loaded []models.Execution
			before := req.ExecID
			for {
				var page []models.Execution
				var err error
				switch scope.Type {
				case "task":
					page, err = s.execRepo.ListByTaskChronologicalBefore(req.Ctx, scope.ID, before, 100)
				case "chat_project":
					page, err = s.execRepo.ListChatHistoryBefore(req.Ctx, scope.ID, before, 100)
				default:
					page = nil
				}
				if err != nil {
					return req, err
				}
				if len(page) == 0 {
					break
				}
				boundary := -1
				for i := len(page) - 1; i >= 0; i-- {
					if page[i].StartsNewContext {
						boundary = i
						break
					}
				}
				if boundary >= 0 {
					page = page[boundary:]
				}
				loaded = append(page, loaded...)
				if boundary >= 0 || len(page) < 100 || page[0].ID == before {
					break
				}
				before = page[0].ID
			}
			// Keep supplied current/virtual records, while filling missing older turns.
			byID := make(map[string]models.Execution)
			for _, exec := range history {
				if exec.ID != "" {
					byID[exec.ID] = exec
				}
			}
			var merged []models.Execution
			seen := make(map[string]bool)
			for _, exec := range loaded {
				if supplied, ok := byID[exec.ID]; ok {
					supplied.Output = exec.Output
					supplied.StartsNewContext = exec.StartsNewContext
					if supplied.PromptSent == "" {
						supplied.PromptSent = exec.PromptSent
					}
					exec = supplied
				} else if exec.Status == models.ExecRunning {
					continue
				}
				merged = append(merged, exec)
				seen[exec.ID] = true
			}
			for _, exec := range history {
				if exec.ID == "" || !seen[exec.ID] {
					merged = append(merged, exec)
				}
			}
			// A supplied new-context boundary also excludes older database records.
			for i := len(merged) - 1; i >= 0; i-- {
				if merged[i].StartsNewContext {
					merged = merged[i:]
					break
				}
			}
			history = merged
		}
	}
	var err error
	req.ChatHistory, err = readableCompactedHistory(history)
	return req, err
}
