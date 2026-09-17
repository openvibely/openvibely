package models

import "time"

type ExecutionStatus string

const (
	ExecQueued    ExecutionStatus = "queued"
	ExecRunning   ExecutionStatus = "running"
	ExecCompleted ExecutionStatus = "completed"
	ExecFailed    ExecutionStatus = "failed"
	ExecCancelled ExecutionStatus = "cancelled"
)

type Execution struct {
	ID               string                   `json:"id"`
	TaskID           string                   `json:"task_id"`
	AgentConfigID    string                   `json:"agent_config_id"`
	Status           ExecutionStatus          `json:"status"`
	PromptSent       string                   `json:"prompt_sent"`
	Output           string                   `json:"output"`
	ErrorMessage     string                   `json:"error_message"`
	TokensUsed       int                      `json:"tokens_used"`
	DurationMs       int64                    `json:"duration_ms"`
	IsFollowup       bool                     `json:"is_followup"`
	StartsNewContext bool                     `json:"starts_new_context"`
	DiffOutput       string                   `json:"diff_output"`
	CliSessionID     string                   `json:"cli_session_id"`
	DispatchID       string                   `json:"dispatch_id,omitempty"`
	ReasoningContent string                   `json:"-"`
	ReplayMessages   []ExecutionReplayMessage `json:"-"`
	StartedAt        time.Time                `json:"started_at"`
	CompletedAt      *time.Time               `json:"completed_at"`
}

// TaskExecutionMetrics is the compact execution projection needed by task
// detail status/metrics badges. It intentionally excludes prompt, output,
// reasoning, error, and diff text.
type TaskExecutionMetrics struct {
	LatestStartedAt  *time.Time
	LatestDurationMs int64
}

// ExecutionReplayMessage preserves one exact user/assistant exchange made
// inside an execution that continued after live steering.
type ExecutionReplayMessage struct {
	UserContent      string
	AssistantContent string
	ReasoningContent string
	TranscriptJSON   string
}
