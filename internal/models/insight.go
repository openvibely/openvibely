package models

import (
	"encoding/json"
	"time"
)

// InsightType categorizes what kind of insight was detected
type InsightType string

const (
	InsightBugPattern          InsightType = "bug_pattern"
	InsightIncompleteFeature   InsightType = "incomplete_feature"
	InsightTechDebt            InsightType = "tech_debt"
	InsightOptimization        InsightType = "optimization"
	InsightDependency          InsightType = "dependency"
	InsightKnowledge           InsightType = "knowledge"
	InsightProactiveSuggestion InsightType = "proactive_suggestion"
)

// InsightSeverity indicates the urgency of the insight
type InsightSeverity string

const (
	InsightSeverityInfo     InsightSeverity = "info"
	InsightSeverityLow      InsightSeverity = "low"
	InsightSeverityMedium   InsightSeverity = "medium"
	InsightSeverityHigh     InsightSeverity = "high"
	InsightSeverityCritical InsightSeverity = "critical"
)

// InsightStatus tracks the lifecycle of an insight
type InsightStatus string

const (
	InsightStatusNew      InsightStatus = "new"
	InsightStatusReviewed InsightStatus = "reviewed"
	InsightStatusAccepted InsightStatus = "accepted"
	InsightStatusRejected InsightStatus = "rejected"
	InsightStatusResolved InsightStatus = "resolved"
)

// Insight represents a single AI-detected insight or suggestion
type Insight struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	Type        InsightType     `json:"type"`
	Severity    InsightSeverity `json:"severity"`
	Status      InsightStatus   `json:"status"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Evidence    string          `json:"evidence"`    // JSON: supporting data
	Suggestion  string          `json:"suggestion"`  // what to do about it
	Impact      string          `json:"impact"`      // estimated impact if addressed/ignored
	TaskID      *string         `json:"task_id,omitempty"` // linked task if accepted
	Confidence  float64         `json:"confidence"`  // 0.0-1.0
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
}

// ParseEvidence returns the parsed evidence data
func (i *Insight) ParseEvidence() (map[string]interface{}, error) {
	if i.Evidence == "" || i.Evidence == "{}" {
		return nil, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(i.Evidence), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// SetEvidence serializes evidence data to JSON
func (i *Insight) SetEvidence(data map[string]interface{}) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	i.Evidence = string(b)
	return nil
}

// InsightReport represents a periodic analysis report
type InsightReport struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	ReportDate  string    `json:"report_date"` // YYYY-MM-DD
	Summary     string    `json:"summary"`     // markdown summary
	InsightIDs  string    `json:"insight_ids"` // JSON array of insight IDs
	Stats       string    `json:"stats"`       // JSON stats summary
	AnalysisLog string    `json:"analysis_log"` // what was analyzed
	CreatedAt   time.Time `json:"created_at"`
}

// ParseInsightIDs returns the list of insight IDs in this report
func (r *InsightReport) ParseInsightIDs() ([]string, error) {
	if r.InsightIDs == "" || r.InsightIDs == "[]" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(r.InsightIDs), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// SetInsightIDs serializes insight IDs to JSON
func (r *InsightReport) SetInsightIDs(ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	r.InsightIDs = string(b)
	return nil
}

// ParseStats returns parsed stats
func (r *InsightReport) ParseStats() (map[string]interface{}, error) {
	if r.Stats == "" || r.Stats == "{}" {
		return nil, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(r.Stats), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// KnowledgeEntry represents a piece of extracted knowledge from git/task history
type KnowledgeEntry struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Topic     string    `json:"topic"`
	Content   string    `json:"content"`   // the "why" explanation
	Source    string    `json:"source"`    // where it was extracted from (commit, task, etc.)
	SourceRef string    `json:"source_ref"` // specific reference (commit hash, task ID)
	Tags      string    `json:"tags"`      // JSON array of searchable tags
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ParseTags returns the list of tags
func (k *KnowledgeEntry) ParseTags() ([]string, error) {
	if k.Tags == "" || k.Tags == "[]" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(k.Tags), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// SetTags serializes tags to JSON
func (k *KnowledgeEntry) SetTags(tags []string) error {
	b, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	k.Tags = string(b)
	return nil
}

// HealthCheck represents a project health evaluation
type HealthCheck struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	Grade            string    `json:"grade"`              // A+ to F
	Strengths        string    `json:"strengths"`          // What you're doing well
	Improvements     string    `json:"improvements"`       // Areas to improve
	Assessment       string    `json:"assessment"`         // Overall assessment
	HowToImprove     string    `json:"how_to_improve"`     // Steps to reach next grade
	TasksTotal       int       `json:"tasks_total"`
	TasksCompleted   int       `json:"tasks_completed"`
	TasksFailed      int       `json:"tasks_failed"`
	TasksPending     int       `json:"tasks_pending"`
	BacklogSize      int       `json:"backlog_size"`
	AvgCompletionPct float64   `json:"avg_completion_pct"` // 0-100
	CreatedAt        time.Time `json:"created_at"`
}

// IdeaGrade represents an AI evaluation of the user's idea/task quality
type IdeaGrade struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	Grade           string    `json:"grade"`            // A+ to F
	Summary         string    `json:"summary"`          // Overall grade explanation
	Strengths       string    `json:"strengths"`        // What they're doing well
	Improvements    string    `json:"improvements"`     // Areas for improvement
	HowToNextGrade  string    `json:"how_to_next_grade"` // How to reach next grade level
	NextGrade       string    `json:"next_grade"`       // The next grade level to aim for
	TasksEvaluated  int       `json:"tasks_evaluated"`
	ClarityScore    float64   `json:"clarity_score"`    // 0-100
	AmbitionScore   float64   `json:"ambition_score"`   // 0-100
	FollowThrough   float64   `json:"follow_through"`   // 0-100 (completion rate)
	DiversityScore  float64   `json:"diversity_score"`  // 0-100
	StrategyScore   float64   `json:"strategy_score"`   // 0-100
	CreatedAt       time.Time `json:"created_at"`
}

// InsightDashboardData aggregates all data for the insights page
type InsightDashboardData struct {
	NewInsights       []Insight        `json:"new_insights"`
	RecentInsights    []Insight        `json:"recent_insights"`
	LatestReport      *InsightReport   `json:"latest_report,omitempty"`
	KnowledgeEntries  []KnowledgeEntry `json:"knowledge_entries"`
	Stats             InsightStats     `json:"stats"`
	LatestHealthCheck *HealthCheck     `json:"latest_health_check,omitempty"`
	HealthHistory     []HealthCheck    `json:"health_history,omitempty"`
	LatestIdeaGrade   *IdeaGrade       `json:"latest_idea_grade,omitempty"`
	IdeaGradeHistory  []IdeaGrade      `json:"idea_grade_history,omitempty"`
}

// InsightStats holds summary stats for the insights page
type InsightStats struct {
	TotalInsights    int     `json:"total_insights"`
	NewCount         int     `json:"new_count"`
	AcceptedCount    int     `json:"accepted_count"`
	RejectedCount    int     `json:"rejected_count"`
	ResolvedCount    int     `json:"resolved_count"`
	AvgConfidence    float64 `json:"avg_confidence"`
	KnowledgeCount   int     `json:"knowledge_count"`
	BugPatterns      int     `json:"bug_patterns"`
	TechDebtItems    int     `json:"tech_debt_items"`
	Optimizations    int     `json:"optimizations"`
}
