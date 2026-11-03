package service

import (
	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// completeReplayHistory separates the UI's bounded execution window from
// model history when no usable checkpoint supplies a boundary (or recovery needs
// the original sequence). Checkpoint restoration uses checkpointReplayHistory instead.
// Only metadata is paged here, never provider payloads or aggregate output.
func (s *LLMService) completeReplayHistory(req contracts.AgentRequest) (contracts.AgentRequest, error) {
	if s == nil || s.execRepo == nil || len(req.ChatHistory) == 0 || req.ExecID == "" || req.RawDirectPrompt || req.LifecycleHookCall {
		return req, nil
	}
	first := req.ChatHistory[0]
	scope, ok := s.compactionScopeForRequest(req)
	if !ok {
		scope = compactionScope{Type: "task", ID: first.TaskID}
	}
	if first.ID == "" || first.TaskID == "" {
		return req, nil
	}
	for _, e := range req.ChatHistory {
		// Synthetic histories and explicit context resets are already complete.
		if e.ContextKind != "" || (scope.Type == "task" && e.TaskID != first.TaskID) || e.StartsNewContext {
			return req, nil
		}
	}
	current, err := s.execRepo.GetByID(persistence.WithExecutionMetadata(req.Ctx), req.ExecID)
	if err != nil {
		return req, err
	}
	if current == nil || (scope.Type == "task" && current.TaskID != first.TaskID) || current.StartsNewContext {
		return req, nil
	}
	before := first.ID
	var older []models.Execution
	for {
		page, err := s.replayHistoryPage(req, scope, "", before)
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
		next := page[0].ID
		fullPage := len(page) == 100
		if boundary >= 0 {
			page = page[boundary:]
		}
		var selected []models.Execution
		for _, e := range page {
			if e.Status != models.ExecRunning && e.ID != req.ExecID {
				selected = append(selected, e)
			}
		}
		older = append(selected, older...)
		if boundary >= 0 || !fullPage || next == before {
			break
		}
		before = next
	}
	if len(older) > 0 {
		// Mark metadata-only records for lazy fallback hydration after replay loading.
		for i := range older {
			older[i].ReplayMetadataOnly = true
		}
		req.ChatHistory = append(older, req.ChatHistory...)
	}
	return req, nil
}

// checkpointReplayHistory fills only the gap between a checkpoint and the caller's
// bounded history window. Including the source lets restoration validate context
// resets and splice the checkpoint without reading any covered predecessors.
func (s *LLMService) checkpointReplayHistory(req contracts.AgentRequest, scope compactionScope, cp *models.ChatCompactionCheckpoint) (contracts.AgentRequest, error) {
	if (scope.Type != "task" && scope.Type != "chat_project") || cp.SourceExecutionID == "" || (len(cp.History) == 0 && !cp.HasNativeState()) || len(req.ChatHistory) == 0 || req.RawDirectPrompt || req.LifecycleHookCall {
		return req, nil
	}
	for _, e := range req.ChatHistory {
		if e.ID == cp.SourceExecutionID || e.ContextKind != "" || (scope.Type == "task" && e.TaskID != scope.ID) {
			return req, nil
		}
	}
	before := req.ChatHistory[0].ID
	var pages [][]models.Execution
	for before != "" {
		page, err := s.replayHistoryPage(req, scope, cp.SourceExecutionID, before)
		if err != nil {
			return req, err
		}
		if len(page) == 0 {
			break
		}
		next, full := page[0].ID, len(page) == 100
		selected := make([]models.Execution, 0, len(page))
		for _, e := range page {
			if e.Status != models.ExecRunning || e.ID == cp.SourceExecutionID {
				e.ReplayMetadataOnly = true
				selected = append(selected, e)
			}
		}
		pages = append(pages, selected)
		if !full || next == cp.SourceExecutionID || next == before {
			break
		}
		before = next
	}
	if len(pages) != 0 {
		var history []models.Execution
		for i := len(pages) - 1; i >= 0; i-- {
			history = append(history, pages[i]...)
		}
		req.ChatHistory = append(history, req.ChatHistory...)
	}
	return req, nil
}

// Provider-independent paging; storage scopes differ, reconstruction does not.
func (s *LLMService) replayHistoryPage(req contracts.AgentRequest, scope compactionScope, after, before string) ([]models.Execution, error) {
	ctx := persistence.WithExecutionMetadata(req.Ctx)
	if scope.Type == "chat_project" {
		return s.execRepo.ListChatModelHistoryBefore(ctx, scope.ID, req.ExecID, after, before, 100)
	}
	if after != "" {
		return s.execRepo.ListByTaskChronologicalBetween(ctx, scope.ID, after, before, 100)
	}
	return s.execRepo.ListByTaskChronologicalBefore(ctx, scope.ID, before, 100)
}
