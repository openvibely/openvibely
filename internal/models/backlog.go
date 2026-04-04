package models

import (
	"encoding/json"
	"time"
)

// BacklogSuggestionType categorizes the kind of backlog suggestion
type BacklogSuggestionType string

const (
	SuggestionReprioritize BacklogSuggestionType = "reprioritize"
	SuggestionObsolete     BacklogSuggestionType = "obsolete"
	SuggestionDecompose    BacklogSuggestionType = "decompose"
	SuggestionQuickWin     BacklogSuggestionType = "quick_win"
	SuggestionSchedule     BacklogSuggestionType = "schedule"
	SuggestionStale        BacklogSuggestionType = "stale"
)

// BacklogSuggestionStatus tracks the lifecycle of a suggestion
type BacklogSuggestionStatus string

const (
	SuggestionPending  BacklogSuggestionStatus = "pending"
	SuggestionApproved BacklogSuggestionStatus = "approved"
	SuggestionRejected BacklogSuggestionStatus = "rejected"
	SuggestionApplied  BacklogSuggestionStatus = "applied"
	SuggestionExpired  BacklogSuggestionStatus = "expired"
)

// BacklogSuggestion represents an AI-generated recommendation for backlog management
type BacklogSuggestion struct {
	ID                string                  `json:"id"`
	ProjectID         string                  `json:"project_id"`
	Type              BacklogSuggestionType   `json:"type"`
	Status            BacklogSuggestionStatus `json:"status"`
	Title             string                  `json:"title"`
	Description       string                  `json:"description"`
	TaskID            *string                 `json:"task_id,omitempty"`
	SuggestedPriority *int                    `json:"suggested_priority,omitempty"`
	SuggestedSubtasks string                  `json:"suggested_subtasks"` // JSON array
	Reasoning         string                  `json:"reasoning"`
	Confidence        float64                 `json:"confidence"`
	CreatedAt         time.Time               `json:"created_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	AppliedAt         *time.Time              `json:"applied_at,omitempty"`
}

// ParseSubtasks returns the suggested subtask descriptions
func (s *BacklogSuggestion) ParseSubtasks() ([]string, error) {
	if s.SuggestedSubtasks == "" || s.SuggestedSubtasks == "[]" {
		return nil, nil
	}
	var subtasks []string
	if err := json.Unmarshal([]byte(s.SuggestedSubtasks), &subtasks); err != nil {
		return nil, err
	}
	return subtasks, nil
}

// SetSubtasks serializes subtask descriptions to JSON
func (s *BacklogSuggestion) SetSubtasks(subtasks []string) error {
	b, err := json.Marshal(subtasks)
	if err != nil {
		return err
	}
	s.SuggestedSubtasks = string(b)
	return nil
}

// BacklogHealthSnapshot represents a point-in-time health measurement of the backlog
type BacklogHealthSnapshot struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"project_id"`
	TotalTasks         int       `json:"total_tasks"`
	AvgAgeDays         float64   `json:"avg_age_days"`
	StaleCount         int       `json:"stale_count"`
	HighPriorityCount  int       `json:"high_priority_count"`
	CompletionVelocity float64   `json:"completion_velocity"`
	BottleneckTags     string    `json:"bottleneck_tags"` // JSON array
	HealthScore        float64   `json:"health_score"`
	Details            string    `json:"details"` // JSON
	CreatedAt          time.Time `json:"created_at"`
}

// ParseBottleneckTags returns the bottleneck tag list
func (h *BacklogHealthSnapshot) ParseBottleneckTags() ([]string, error) {
	if h.BottleneckTags == "" || h.BottleneckTags == "[]" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(h.BottleneckTags), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// BacklogAnalysisReport represents a summary of each analysis run
type BacklogAnalysisReport struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ReportDate    string    `json:"report_date"`
	Summary       string    `json:"summary"`
	SuggestionIDs string    `json:"suggestion_ids"` // JSON array
	Stats         string    `json:"stats"`          // JSON
	CreatedAt     time.Time `json:"created_at"`
}

// ParseSuggestionIDs returns the list of suggestion IDs in this report
func (r *BacklogAnalysisReport) ParseSuggestionIDs() ([]string, error) {
	if r.SuggestionIDs == "" || r.SuggestionIDs == "[]" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(r.SuggestionIDs), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// SetSuggestionIDs serializes suggestion IDs to JSON
func (r *BacklogAnalysisReport) SetSuggestionIDs(ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	r.SuggestionIDs = string(b)
	return nil
}

// BacklogDashboardData aggregates all data for the backlog management page
type BacklogDashboardData struct {
	PendingSuggestions []BacklogSuggestion    `json:"pending_suggestions"`
	RecentSuggestions  []BacklogSuggestion    `json:"recent_suggestions"`
	LatestHealth       *BacklogHealthSnapshot `json:"latest_health,omitempty"`
	LatestReport       *BacklogAnalysisReport `json:"latest_report,omitempty"`
	Stats              BacklogStats           `json:"stats"`
}

// BacklogStats holds summary statistics for the dashboard
type BacklogStats struct {
	TotalSuggestions  int     `json:"total_suggestions"`
	PendingCount      int     `json:"pending_count"`
	ApprovedCount     int     `json:"approved_count"`
	AppliedCount      int     `json:"applied_count"`
	RejectedCount     int     `json:"rejected_count"`
	AvgConfidence     float64 `json:"avg_confidence"`
	HealthScore       float64 `json:"health_score"`
	BacklogSize       int     `json:"backlog_size"`
	StaleTaskCount    int     `json:"stale_task_count"`
	QuickWinCount     int     `json:"quick_win_count"`
	ObsoleteCount     int     `json:"obsolete_count"`
	DecomposeCount    int     `json:"decompose_count"`
}
