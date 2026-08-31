package viewmodels

import "time"

// LifecycleExecutionView is the prompt-safe UI shape for lifecycle hook output.
// It intentionally excludes raw input/output JSON and prompt text.
type LifecycleExecutionView struct {
	ID               string               `json:"id"`
	When             string               `json:"when"`
	AgentID          string               `json:"agent_id"`
	SkillKey         string               `json:"skill_key"`
	Status           string               `json:"status"`
	OutputContract   string               `json:"output_contract"`
	Summary          string               `json:"summary,omitempty"`
	SelectedSkills   []string             `json:"selected_skills,omitempty"`
	SelectedMemories []SelectedMemoryView `json:"selected_memories,omitempty"`
	Error            string               `json:"error,omitempty"`
	StartedAt        time.Time            `json:"started_at"`
	CompletedAt      *time.Time           `json:"completed_at,omitempty"`
}

// LifecycleExecutionPageView is the bounded prompt-safe lifecycle activity
// response. The handler maps each item through the same evidence-safe view
// projection used by the original unpaged response.
type LifecycleExecutionPageView struct {
	Items      []LifecycleExecutionView `json:"items"`
	HasMore    bool                     `json:"has_more"`
	NextCursor string                   `json:"next_cursor,omitempty"`
}

// SelectedMemoryView is the compact prompt-safe memory recall detail shown in
// task lifecycle activity. It contains identifiers plus brief summaries/snippets,
// never raw memory files.
type SelectedMemoryView struct {
	File    string `json:"file,omitempty"`
	Topic   string `json:"topic,omitempty"`
	Summary string `json:"summary,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// LifecycleExecutionEventView is the prompt-safe trace shape returned by the
// lifecycle trace API. Payload is already sanitized/truncated before storage.
type LifecycleExecutionEventView struct {
	ID        string         `json:"id"`
	Seq       int            `json:"seq"`
	EventType string         `json:"event_type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"created_at"`
}
