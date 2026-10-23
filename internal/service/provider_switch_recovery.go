package service

import (
	"encoding/json"
	"fmt"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
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

const recoveryBackgroundKind = "provider_recovery_background"

// Split the latest complete OpenAI snapshot at its last compaction item.
// Preserve readable messages on both sides of the encrypted compaction item.
func portableRecoveryParts(history []models.Execution) (int, []models.Execution) {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].ReplayMessages) - 1; j >= 0; j-- {
			var saved struct {
				Input []json.RawMessage `json:"responses_input"`
			}
			if json.Unmarshal([]byte(history[i].ReplayMessages[j].TranscriptJSON), &saved) != nil {
				continue
			}
			for k := len(saved.Input) - 1; k >= 0; k-- {
				var item struct{ Type string }
				_ = json.Unmarshal(saved.Input[k], &item)
				if item.Type != "compaction" {
					continue
				}
				// Use portable messages so the old Responses snapshot cannot replace the
				// recovered background during EffectiveReplayHistory normalization.
				messages := repairConvertedToolResults(llmopenai.ResponsesSummaryMessages(saved.Input[k+1:]))
				var tail []models.Execution
				prefix := repairConvertedToolResults(llmopenai.ResponsesSummaryMessages(saved.Input[:k]))
				if len(prefix) > 0 {
					raw, _ := json.Marshal(prefix)
					tail = append(tail, models.Execution{Status: models.ExecCompleted, ContextKind: "provider_recovery_prefix", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}})
				}
				if len(messages) > 0 {
					raw, _ := json.Marshal(messages)
					tail = append(tail, models.Execution{Status: models.ExecCompleted, ContextKind: "provider_recovery_tail", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}})
				}
				if j+1 < len(history[i].ReplayMessages) {
					remaining := history[i]
					remaining.ReplayMessages = remaining.ReplayMessages[j+1:]
					tail = append(tail, remaining)
				}
				tail = append(tail, history[i+1:]...)
				return i, tail
			}
		}
	}
	return -1, nil
}

// Recovered records are background evidence, not fabricated alternating chat
// messages. Their output can overlap the readable tail; no exact boundary is known.
func readableCompactedHistory(history []models.Execution) ([]models.Execution, error) {
	if !hasOpaqueCompaction(history) {
		return history, nil
	}
	last, tail := portableRecoveryParts(history)
	var out []models.Execution
	for _, exec := range history[:last+1] {
		if exec.PromptSent == "" && exec.Output == "" {
			continue
		}
		text := "Recovered execution background (may overlap later messages; output does not locate steering within the run).\nStarting user message:\n" + exec.PromptSent + "\nAssistant and tool output:\n" + exec.Output
		raw, _ := json.Marshal([]openaiclient.CompletionsHistoryMessage{{Role: "user", Content: text}})
		out = append(out, models.Execution{ID: exec.ID, Status: models.ExecCompleted, ContextKind: recoveryBackgroundKind, ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cannot switch providers: readable history for native OpenAI compaction is unavailable")
	}
	return append(out, tail...), nil
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
	// Recover applied steering as execution-scoped background. Do not insert it
	// at a guessed point in the assistant's output string.
	history = append([]models.Execution(nil), history...)
	last, tail := portableRecoveryParts(history)
	// Exact text matches in readable user messages already have a known place.
	// Do not repeat those steering messages in the recovered background.
	visibleSteering := make(map[string]bool)
	for _, exec := range tail {
		for _, replay := range exec.ReplayMessages {
			var messages []openaiclient.CompletionsHistoryMessage
			if json.Unmarshal([]byte(replay.TranscriptJSON), &messages) != nil {
				continue
			}
			for _, m := range messages {
				if m.Role != "user" {
					continue
				}
				if m.Content != "" {
					visibleSteering[m.Content] = true
				}
				for _, part := range m.ImageContent {
					if part["type"] == "text" {
						if text, ok := part["text"].(string); ok {
							visibleSteering[text] = true
						}
					}
				}
			}
		}
	}
	if s != nil && s.threadInputRepo != nil {
		for i := 0; i <= last; i++ {
			if history[i].ID == "" {
				continue
			}
			inputs, err := s.threadInputRepo.ListAppliedSteeringByExecution(req.Ctx, history[i].ID)
			if err != nil {
				return req, err
			}
			var notes strings.Builder
			for _, input := range inputs {
				if input.Content != "" && !visibleSteering[input.Content] {
					notes.WriteString("\nApplied user steering during this run (exact position within output unknown):\n" + input.Content + "\n")
				}
			}
			history[i].Output += notes.String()
		}
	}
	var err error
	req.ChatHistory, err = readableCompactedHistory(history)
	return req, err
}

// portableAnthropicCheckpoint preserves the native summary and continuation
// without carrying compaction blocks or signed reasoning into another session.
func portableAnthropicCheckpoint(raw string) ([]models.Execution, error) {
	var envelope struct {
		Version  int `json:"version"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != 1 {
		var block struct {
			Type    string
			Content *string
		}
		if err := json.Unmarshal([]byte(raw), &block); err != nil || block.Type != "compaction" || block.Content == nil {
			return nil, fmt.Errorf("invalid Anthropic checkpoint")
		}
		envelope.Messages = append(envelope.Messages, struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}{Role: "assistant", Content: json.RawMessage("[" + raw + "]")})
	}
	var messages []anthropicclient.Message
	foundSummary := false
	for _, message := range envelope.Messages {
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			messages = append(messages, anthropicclient.Message{Role: message.Role, Content: text})
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(message.Content, &blocks); err != nil {
			return nil, err
		}
		var portable []map[string]any
		for _, block := range blocks {
			switch block["type"] {
			case "compaction":
				summary, ok := block["content"].(string)
				if ok && strings.TrimSpace(summary) != "" {
					foundSummary = true
					portable = append(portable, map[string]any{"type": "text", "text": "Conversation summary:\n" + summary})
				}
			case "thinking", "redacted_thinking":
				// These signatures belong to the original provider session.
			default:
				portable = append(portable, block)
			}
		}
		if len(portable) > 0 {
			encoded, err := json.Marshal(portable)
			if err != nil {
				return nil, err
			}
			messages = append(messages, anthropicclient.Message{Role: message.Role, ContentBlocks: encoded})
		}
	}
	if !foundSummary {
		return nil, fmt.Errorf("Anthropic checkpoint has no readable summary")
	}
	encoded, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return nil, err
	}
	return []models.Execution{{Status: models.ExecCompleted, ContextKind: "provider_recovery", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(encoded)}}}}, nil
}
