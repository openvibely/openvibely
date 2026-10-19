package service

import (
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmopenai "github.com/openvibely/openvibely/internal/llm/openai"
	"github.com/openvibely/openvibely/internal/models"
)

// Archives are loaded only when opaque native state cannot be expanded from the
// already hydrated history. They are never inserted as extra conversation turns.
func (s *LLMService) recoverPortableProviderHistory(req llmcontracts.AgentRequest) (llmcontracts.AgentRequest, error) {
	sources := llmopenai.CompactionSources(req.ChatHistory)
	expanded, recoveryErr := llmopenai.ExpandCompactedHistory(req.ChatHistory, sources)
	if recoveryErr == nil {
		req.ChatHistory = expanded
		return req, nil
	}
	if s == nil || s.execRepo == nil {
		return req, recoveryErr
	}
	scope, ok := s.compactionScopeForRequest(req)
	if !ok {
		return req, recoveryErr
	}
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
			return req, recoveryErr
		}
		if err != nil {
			return req, err
		}
		if len(page) == 0 {
			return req, recoveryErr
		}
		ids := make([]string, 0, len(page))
		for _, exec := range page {
			ids = append(ids, exec.ID)
		}
		replays, err := s.execRepo.ReplayMessagesByExecutionIDs(req.Ctx, ids)
		if err != nil {
			return req, err
		}
		for i := range page {
			page[i].ReplayMessages = replays[page[i].ID]
		}
		for key, source := range llmopenai.CompactionSources(page) {
			sources[key] = source
		}
		expanded, recoveryErr = llmopenai.ExpandCompactedHistory(req.ChatHistory, sources)
		if recoveryErr == nil {
			req.ChatHistory = expanded
			return req, nil
		}
		if page[0].ID == before || len(page) < 100 {
			return req, recoveryErr
		}
		before = page[0].ID
	}
}
