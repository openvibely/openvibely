package models

import "time"

// ChatCompactionCheckpoint stores provider-bound compacted model context for a
// chat-like thread while leaving execution transcript history untouched.
type ChatCompactionCheckpoint struct {
	ScopeType         string
	ScopeID           string
	ModelConfigID     string
	SourceExecutionID string
	History           []Execution
	Summary           string
	Strategy          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
