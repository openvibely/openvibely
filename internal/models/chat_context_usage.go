package models

// ChatContextUsage is the last model response's context usage, not execution billing usage.
type ChatContextUsage struct {
	SourceExecutionID string
	ContextTokens     int
	OverheadTokens    int
}
