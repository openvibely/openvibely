package persistence

type CompleteSuccessOutcome string

const (
	CompleteSuccessCompleted       CompleteSuccessOutcome = "completed"
	CompleteSuccessPendingSteering CompleteSuccessOutcome = "pending_steering"
	CompleteSuccessAlreadyTerminal CompleteSuccessOutcome = "already_terminal"
)

// SuccessFailureRate represents success/failure rates for a time period
type SuccessFailureRate struct {
	Period         string
	TotalCount     int
	SuccessCount   int
	FailureCount   int
	CancelledCount int
	SuccessRate    float64
}

// AvgExecutionTime represents average execution time
type AvgExecutionTime struct {
	ID    string
	Name  string
	AvgMs float64
	Count int
	MinMs int64
	MaxMs int64
}

// ExecutionTrend represents execution frequency data
type ExecutionTrend struct {
	Hour  int
	Count int
}

// AgentUsage represents agent usage statistics
type AgentUsage struct {
	AgentID        string
	AgentName      string
	ProjectID      string
	ProjectName    string
	ExecutionCount int
	SuccessCount   int
	FailureCount   int
}

// TaskFrequency represents task execution frequency
type TaskFrequency struct {
	TaskID         string
	TaskTitle      string
	ExecutionCount int
	LastExecutedAt string
}

// FailedTaskPattern represents failed task pattern
// returned by the Analytics API.
type FailedTaskPattern struct {
	TaskID       string
	TaskTitle    string
	FailureCount int
	LastError    string
	LastFailedAt string
}
