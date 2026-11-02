package service

import (
	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// completeTaskReplayHistory separates the UI's bounded execution window from
// model history. Every provider restores the same chronological task sequence;
// checkpoint restoration and replay headers subsequently discard covered data.
// Only metadata is paged here, never provider payloads or aggregate output.
func (s *LLMService) completeTaskReplayHistory(req contracts.AgentRequest) (contracts.AgentRequest, error) {
	if s == nil || s.execRepo == nil || len(req.ChatHistory) == 0 || req.ExecID == "" || req.RawDirectPrompt || req.LifecycleHookCall {
		return req, nil
	}
	first := req.ChatHistory[0]
	if first.ID == "" || first.TaskID == "" {
		return req, nil
	}
	for _, e := range req.ChatHistory {
		// Synthetic histories and explicit context resets are already complete.
		if e.ContextKind != "" || e.TaskID != first.TaskID || e.StartsNewContext {
			return req, nil
		}
	}
	current, err := s.execRepo.GetByID(persistence.WithExecutionMetadata(req.Ctx), req.ExecID)
	if err != nil {
		return req, err
	}
	if current == nil || current.TaskID != first.TaskID || current.StartsNewContext {
		return req, nil
	}
	before := first.ID
	var older []models.Execution
	for {
		page, err := s.execRepo.ListByTaskChronologicalBefore(persistence.WithExecutionMetadata(req.Ctx), first.TaskID, before, 100)
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
