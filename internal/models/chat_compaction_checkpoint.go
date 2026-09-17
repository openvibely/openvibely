package models

import "time"

// ChatCompactionCheckpoint stores provider-bound compacted model context for a
// chat-like thread while leaving execution transcript history untouched.
type ChatCompactionCheckpoint struct {
	ScopeType         string
	ScopeID           string
	ModelConfigID     string
	CompatibilityKey  string
	SourceExecutionID string
	History           []Execution
	Summary           string
	Strategy          string
	ProviderStateJSON string
	// ProviderSessionStateJSON is independent from provider-native compaction
	// state and survives transport cache eviction and process restarts.
	ProviderSessionStateJSON string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}
