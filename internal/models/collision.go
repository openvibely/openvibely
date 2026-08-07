package models

import (
	"encoding/json"
	"time"
)

// ConflictType indicates what kind of resource overlap was detected
type ConflictType string

const (
	ConflictTypeFile      ConflictType = "file"
	ConflictTypeAPI       ConflictType = "api"
	ConflictTypeSchema    ConflictType = "schema"
	ConflictTypeComponent ConflictType = "component"
	ConflictTypeSemantic  ConflictType = "semantic"
)

// ConflictSeverity indicates how serious the predicted conflict is
type ConflictSeverity string

const (
	SeverityLow      ConflictSeverity = "low"
	SeverityMedium   ConflictSeverity = "medium"
	SeverityHigh     ConflictSeverity = "high"
	SeverityCritical ConflictSeverity = "critical"
)

// ConflictStatus tracks the lifecycle of a predicted conflict
type ConflictStatus string

const (
	ConflictDetected     ConflictStatus = "detected"
	ConflictAcknowledged ConflictStatus = "acknowledged"
	ConflictResolved     ConflictStatus = "resolved"
	ConflictFalsePositive ConflictStatus = "false_positive"
)

// RecommendationStatus tracks the state of an execution order recommendation
type RecommendationStatus string

const (
	RecommendationPending  RecommendationStatus = "pending"
	RecommendationAccepted RecommendationStatus = "accepted"
	RecommendationRejected RecommendationStatus = "rejected"
	RecommendationExpired  RecommendationStatus = "expired"
)

// ImpactAnalysis stores AI-predicted scope of a task before execution
type ImpactAnalysis struct {
	ID                 string    `json:"id"`
	TaskID             string    `json:"task_id"`
	ProjectID          string    `json:"project_id"`
	FilesImpacted      string    `json:"files_impacted"`      // JSON array
	APIsImpacted       string    `json:"apis_impacted"`       // JSON array
	SchemasImpacted    string    `json:"schemas_impacted"`    // JSON array
	ComponentsImpacted string    `json:"components_impacted"` // JSON array
	ImpactSummary      string    `json:"impact_summary"`
	Confidence         float64   `json:"confidence"`
	AnalysisModel      string    `json:"analysis_model"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ParseFiles returns the list of impacted file paths
func (ia *ImpactAnalysis) ParseFiles() ([]string, error) {
	return parseStringArray(ia.FilesImpacted)
}

// ParseAPIs returns the list of impacted API endpoints
func (ia *ImpactAnalysis) ParseAPIs() ([]string, error) {
	return parseStringArray(ia.APIsImpacted)
}

// ParseSchemas returns the list of impacted DB schemas
func (ia *ImpactAnalysis) ParseSchemas() ([]string, error) {
	return parseStringArray(ia.SchemasImpacted)
}

// ParseComponents returns the list of impacted system components
func (ia *ImpactAnalysis) ParseComponents() ([]string, error) {
	return parseStringArray(ia.ComponentsImpacted)
}

// SetFiles serializes file paths to JSON
func (ia *ImpactAnalysis) SetFiles(files []string) error {
	data, err := json.Marshal(files)
	if err != nil {
		return err
	}
	ia.FilesImpacted = string(data)
	return nil
}

// SetAPIs serializes API endpoints to JSON
func (ia *ImpactAnalysis) SetAPIs(apis []string) error {
	data, err := json.Marshal(apis)
	if err != nil {
		return err
	}
	ia.APIsImpacted = string(data)
	return nil
}

// SetSchemas serializes DB schemas to JSON
func (ia *ImpactAnalysis) SetSchemas(schemas []string) error {
	data, err := json.Marshal(schemas)
	if err != nil {
		return err
	}
	ia.SchemasImpacted = string(data)
	return nil
}

// SetComponents serializes system components to JSON
func (ia *ImpactAnalysis) SetComponents(components []string) error {
	data, err := json.Marshal(components)
	if err != nil {
		return err
	}
	ia.ComponentsImpacted = string(data)
	return nil
}

// ConflictPrediction represents a predicted conflict between two tasks
type ConflictPrediction struct {
	ID                   string           `json:"id"`
	ProjectID            string           `json:"project_id"`
	TaskAID              string           `json:"task_a_id"`
	TaskBID              string           `json:"task_b_id"`
	ConflictType         ConflictType     `json:"conflict_type"`
	Severity             ConflictSeverity `json:"severity"`
	Description          string           `json:"description"`
	OverlappingResources string           `json:"overlapping_resources"` // JSON array
	ResolutionStrategy   string           `json:"resolution_strategy"`
	Status               ConflictStatus   `json:"status"`
	ResolvedAt           *time.Time       `json:"resolved_at,omitempty"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

// ParseOverlappingResources returns the list of shared resources
func (cp *ConflictPrediction) ParseOverlappingResources() ([]string, error) {
	return parseStringArray(cp.OverlappingResources)
}

// SetOverlappingResources serializes shared resources to JSON
func (cp *ConflictPrediction) SetOverlappingResources(resources []string) error {
	data, err := json.Marshal(resources)
	if err != nil {
		return err
	}
	cp.OverlappingResources = string(data)
	return nil
}

// ConflictHistory tracks actual conflicts that occurred for learning
type ConflictHistory struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	TaskAID      string    `json:"task_a_id"`
	TaskBID      string    `json:"task_b_id"`
	PredictionID *string   `json:"prediction_id,omitempty"`
	WasPredicted bool      `json:"was_predicted"`
	ConflictType string    `json:"conflict_type"`
	ActualFiles  string    `json:"actual_files"` // JSON array
	Resolution   string    `json:"resolution"`
	ImpactScore  float64   `json:"impact_score"`
	CreatedAt    time.Time `json:"created_at"`
}

// ParseActualFiles returns the list of files that actually conflicted
func (ch *ConflictHistory) ParseActualFiles() ([]string, error) {
	return parseStringArray(ch.ActualFiles)
}

// ExecutionOrderRecommendation suggests optimal task execution order
type ExecutionOrderRecommendation struct {
	ID            string               `json:"id"`
	ProjectID     string               `json:"project_id"`
	TaskIDs       string               `json:"task_ids"`       // JSON array of task IDs in order
	Reasoning     string               `json:"reasoning"`
	ConflictCount int                  `json:"conflict_count"`
	BatchGroups   string               `json:"batch_groups"`   // JSON array of arrays
	Status        RecommendationStatus `json:"status"`
	CreatedAt     time.Time            `json:"created_at"`
	ExpiresAt     time.Time            `json:"expires_at"`
}

// ParseTaskIDs returns the ordered list of task IDs
func (r *ExecutionOrderRecommendation) ParseTaskIDs() ([]string, error) {
	return parseStringArray(r.TaskIDs)
}

// ParseBatchGroups returns the batch groups for parallel execution
func (r *ExecutionOrderRecommendation) ParseBatchGroups() ([][]string, error) {
	if r.BatchGroups == "" || r.BatchGroups == "[]" {
		return nil, nil
	}
	var groups [][]string
	if err := json.Unmarshal([]byte(r.BatchGroups), &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// SetTaskIDs serializes task IDs to JSON
func (r *ExecutionOrderRecommendation) SetTaskIDs(ids []string) error {
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	r.TaskIDs = string(data)
	return nil
}

// SetBatchGroups serializes batch groups to JSON
func (r *ExecutionOrderRecommendation) SetBatchGroups(groups [][]string) error {
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	r.BatchGroups = string(data)
	return nil
}

// ImpactAnalysisRequest is the AI prompt input for impact analysis
type ImpactAnalysisRequest struct {
	TaskTitle   string `json:"task_title"`
	TaskPrompt  string `json:"task_prompt"`
	ProjectName string `json:"project_name"`
	RepoPath    string `json:"repo_path"`
}

// ImpactAnalysisResponse is the parsed AI response for impact analysis
type ImpactAnalysisResponse struct {
	Files      []string `json:"files"`
	APIs       []string `json:"apis"`
	Schemas    []string `json:"schemas"`
	Components []string `json:"components"`
	Summary    string   `json:"summary"`
	Confidence float64  `json:"confidence"`
}

// ConflictCheckResult is the result of comparing two impact analyses
type ConflictCheckResult struct {
	HasConflict          bool             `json:"has_conflict"`
	ConflictType         ConflictType     `json:"conflict_type"`
	Severity             ConflictSeverity `json:"severity"`
	Description          string           `json:"description"`
	OverlappingResources []string         `json:"overlapping_resources"`
	ResolutionStrategy   string           `json:"resolution_strategy"`
}

// CollisionReport is the full report for a set of tasks
type CollisionReport struct {
	ProjectID      string                        `json:"project_id"`
	Analyses       []ImpactAnalysis              `json:"analyses"`
	Conflicts      []ConflictPrediction          `json:"conflicts"`
	Recommendation *ExecutionOrderRecommendation `json:"recommendation,omitempty"`
	GeneratedAt    time.Time                     `json:"generated_at"`
}

// helper to parse JSON string arrays
func parseStringArray(s string) ([]string, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, err
	}
	return result, nil
}
