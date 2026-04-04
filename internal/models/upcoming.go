package models

import "time"

// TimeRange represents a time window for filtering
type TimeRange string

const (
	TimeRangeHour TimeRange = "hour"
	TimeRangeDay  TimeRange = "day"
	TimeRangeWeek TimeRange = "week"
)

// UpcomingTask represents a task queued for upcoming execution
type UpcomingTask struct {
	Task      Task
	AgentName string
	NextRun   *time.Time // From schedule, nil if active-pending
	Schedule  *Schedule
}

// TaskSummary provides high-level task metrics for the Pulse dashboard
type TaskSummary struct {
	// Total pending tasks (not completed/cancelled) across active+backlog+scheduled
	TotalPending int

	// Counts by priority (1=Low, 2=Normal, 3=High, 4=Urgent)
	UrgentCount int // Priority 4
	HighCount   int // Priority 3
	NormalCount int // Priority 2
	LowCount    int // Priority 1

	// Counts by status
	PendingCount   int
	RunningCount   int
	CompletedCount int
	FailedCount    int

	// Counts by category
	ActiveCount    int
	BacklogCount   int
	ScheduledCount int

	// Schedule-related
	ScheduledToday    int // Scheduled tasks due today
	ScheduledThisWeek int // Scheduled tasks due this week
	OverdueCount      int // Scheduled tasks past their next_run
}

// Upcoming represents a summary of upcoming planned work
type Upcoming struct {
	ProjectID      string
	GeneratedAt    time.Time
	RunningTasks   []UpcomingTask          // Currently executing
	PendingTasks   []UpcomingTask          // Active category, pending status
	ScheduledTasks []UpcomingTask          // Scheduled with upcoming next_run
	BacklogHealth  *BacklogHealthSnapshot  // Latest backlog health metrics
	TaskSummary    *TaskSummary            // High-level task metrics
	AISummary      string                  // AI-generated 10,000 foot view
}

// HistoryExecution represents a completed execution with its task info
type HistoryExecution struct {
	Execution Execution
	TaskTitle string
	AgentName string
}

// HistorySummary provides aggregate metrics for the history
type HistorySummary struct {
	TotalExecutions int
	SuccessCount    int
	FailureCount    int
	CancelledCount  int
	TotalDurationMs int64
	AvgDurationMs   int64
}

// GitCommit represents a single git commit
type GitCommit struct {
	Hash        string
	ShortHash   string
	Author      string
	Date        time.Time
	Subject     string
	FilesChanged int
	Insertions   int
	Deletions    int
}

// FileTypeCount tracks the number of modified files by extension
type FileTypeCount struct {
	Extension string
	Count     int
}

// ChangeSummary categorizes a commit as feature, bugfix, config, etc.
type ChangeSummary struct {
	Features      []string // Commits identified as new features
	BugFixes      []string // Commits identified as bug fixes
	ConfigChanges []string // Architecture or configuration changes
}

// ProjectChanges holds a summary of recent git activity for a project
type ProjectChanges struct {
	Available     bool            // Whether git data is available
	TotalCommits  int
	TotalInsertions int
	TotalDeletions  int
	FilesChanged    int
	Commits       []GitCommit
	FileTypes     []FileTypeCount
	Changes       ChangeSummary
}

// History represents a summary of recently completed work
type History struct {
	ProjectID      string
	GeneratedAt    time.Time
	TimeRange      TimeRange
	Since          time.Time
	Summary        HistorySummary
	Executions     []HistoryExecution
	ProjectChanges   *ProjectChanges
	AISummary        string           // AI-generated reflection summary
}
