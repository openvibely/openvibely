package models

import "time"

type ExecutionStatus string

const (
	ExecRunning   ExecutionStatus = "running"
	ExecCompleted ExecutionStatus = "completed"
	ExecFailed    ExecutionStatus = "failed"
	ExecCancelled ExecutionStatus = "cancelled"
)

type Execution struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	AgentConfigID string          `json:"agent_config_id"`
	Status        ExecutionStatus `json:"status"`
	PromptSent    string          `json:"prompt_sent"`
	Output        string          `json:"output"`
	ErrorMessage  string          `json:"error_message"`
	TokensUsed    int             `json:"tokens_used"`
	DurationMs    int64           `json:"duration_ms"`
	IsFollowup    bool            `json:"is_followup"`
	DiffOutput    string          `json:"diff_output"`
	CliSessionID  string          `json:"cli_session_id"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   *time.Time      `json:"completed_at"`
}
