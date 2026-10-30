package openaiclient

import "strings"

// Helpers for arranging and inspecting reasoning state in transport tests.
func (s *ResponsesTransportState) lastAstraReasoningEffort(model string) string {
	_, configured, _ := s.astraReasoningState(model)
	return configured
}

func (s *ResponsesTransportState) setAstraReasoningEffort(model, effort string) {
	if s == nil || !isGPT6WorkflowModel(model) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	effort = strings.TrimSpace(effort)
	if s.astraRequestEffort == "" {
		s.astraRequestEffort = effort
	}
	s.astraConfiguredEffort = effort
}
