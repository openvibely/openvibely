package models

import (
	"encoding/json"
	"time"
)

// Workflow execution strategies
type WorkflowStrategy string

const (
	StrategySequential WorkflowStrategy = "sequential"
	StrategyParallel   WorkflowStrategy = "parallel"
	StrategyHybrid     WorkflowStrategy = "hybrid"
	StrategyAdaptive   WorkflowStrategy = "adaptive" // System chooses based on task analysis
)

// Workflow status
type WorkflowStatus string

const (
	WorkflowPending   WorkflowStatus = "pending"
	WorkflowRunning   WorkflowStatus = "running"
	WorkflowCompleted WorkflowStatus = "completed"
	WorkflowFailed    WorkflowStatus = "failed"
	WorkflowCancelled WorkflowStatus = "cancelled"
	WorkflowPaused    WorkflowStatus = "paused" // Waiting for user input (voting, intervention)
)

// Step status
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepRolledBack StepStatus = "rolled_back"
)

// Step types define the behavior of each step
type StepType string

const (
	StepTypeExecute    StepType = "execute"     // Execute a task with an agent
	StepTypeReview     StepType = "review"      // Review output of a previous step
	StepTypeVote       StepType = "vote"        // Multiple agents vote on a decision
	StepTypeMerge      StepType = "merge"       // Merge outputs from parallel steps
	StepTypeGate       StepType = "gate"        // Quality gate - pass/fail check
	StepTypeHandoff    StepType = "handoff"     // Generate context summary for next agent
)

// Workflow template categories
type WorkflowTemplateCategory string

const (
	TemplateCategoryFeature   WorkflowTemplateCategory = "feature"
	TemplateCategoryRefactor  WorkflowTemplateCategory = "refactor"
	TemplateCategoryBugfix    WorkflowTemplateCategory = "bugfix"
	TemplateCategoryResearch  WorkflowTemplateCategory = "research"
	TemplateCategoryCustom    WorkflowTemplateCategory = "custom"
)

// Workflow represents a multi-agent workflow definition
type Workflow struct {
	ID          string           `json:"id"`
	ProjectID   string           `json:"project_id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Strategy    WorkflowStrategy `json:"strategy"`
	TemplateID  *string          `json:"template_id,omitempty"` // Source template if created from one
	Config      string           `json:"config"`                // JSON WorkflowConfig
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// WorkflowConfig holds the full workflow configuration
type WorkflowConfig struct {
	MaxRetries      int     `json:"max_retries"`       // Max retries per step on failure
	QualityThreshold float64 `json:"quality_threshold"` // 0-1, threshold for quality gates
	MaxCostCents    int     `json:"max_cost_cents"`    // Cost budget for the workflow (0 = unlimited)
	TimeoutMinutes  int     `json:"timeout_minutes"`   // Overall timeout (0 = unlimited)
	AutoRollback    bool    `json:"auto_rollback"`     // Auto-rollback on step failure
	AdaptiveRouting bool    `json:"adaptive_routing"`  // Use performance data for agent selection
}

// ParseWorkflowConfig parses the Config JSON field
func (w *Workflow) ParseWorkflowConfig() (*WorkflowConfig, error) {
	if w.Config == "" || w.Config == "{}" {
		return &WorkflowConfig{MaxRetries: 1, QualityThreshold: 0.7}, nil
	}
	var cfg WorkflowConfig
	if err := json.Unmarshal([]byte(w.Config), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetWorkflowConfig serializes workflow config to JSON
func (w *Workflow) SetWorkflowConfig(cfg *WorkflowConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	w.Config = string(data)
	return nil
}

// WorkflowStep represents a single step in a workflow
type WorkflowStep struct {
	ID           string   `json:"id"`
	WorkflowID   string   `json:"workflow_id"`
	Name         string   `json:"name"`
	StepType     StepType `json:"step_type"`
	StepOrder    int      `json:"step_order"`    // Execution order (steps with same order run in parallel)
	AgentID      *string  `json:"agent_id,omitempty"`
	Prompt       string   `json:"prompt"`        // Prompt template (supports {{prev_output}}, {{task_prompt}} vars)
	DependsOn    string   `json:"depends_on"`    // JSON array of step IDs this step depends on
	Config       string   `json:"config"`        // JSON StepConfig
	CreatedAt    time.Time `json:"created_at"`
}

// StepConfig holds step-specific configuration
type StepConfig struct {
	// For review/gate steps
	PassThreshold    float64  `json:"pass_threshold,omitempty"`    // Score threshold to pass
	FailAction       string   `json:"fail_action,omitempty"`      // "retry", "rollback", "skip", "pause"
	MaxIterations    int      `json:"max_iterations,omitempty"`   // Max refinement iterations

	// For vote steps
	VoterAgentIDs    []string `json:"voter_agent_ids,omitempty"`  // Agents that participate in voting
	VoteStrategy     string   `json:"vote_strategy,omitempty"`    // "majority", "unanimous", "weighted"

	// For merge steps
	MergeStrategy    string   `json:"merge_strategy,omitempty"`   // "concatenate", "coordinate", "select_best"
	CoordinatorAgent string   `json:"coordinator_agent,omitempty"` // Agent that handles merge coordination

	// For handoff steps
	SummaryMaxTokens int      `json:"summary_max_tokens,omitempty"` // Max tokens for context summary

	// For adaptive routing
	PreferredTaskTypes []string `json:"preferred_task_types,omitempty"` // Task types this step handles well
}

// ParseStepConfig parses the Config JSON field
func (s *WorkflowStep) ParseStepConfig() (*StepConfig, error) {
	if s.Config == "" || s.Config == "{}" {
		return &StepConfig{}, nil
	}
	var cfg StepConfig
	if err := json.Unmarshal([]byte(s.Config), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetStepConfig serializes step config to JSON
func (s *WorkflowStep) SetStepConfig(cfg *StepConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	s.Config = string(data)
	return nil
}

// ParseDependsOn returns the list of step IDs this step depends on
func (s *WorkflowStep) ParseDependsOn() ([]string, error) {
	if s.DependsOn == "" || s.DependsOn == "[]" {
		return nil, nil
	}
	var deps []string
	if err := json.Unmarshal([]byte(s.DependsOn), &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

// SetDependsOn serializes dependency list
func (s *WorkflowStep) SetDependsOn(deps []string) error {
	if len(deps) == 0 {
		s.DependsOn = "[]"
		return nil
	}
	data, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	s.DependsOn = string(data)
	return nil
}

// WorkflowExecution tracks a running workflow instance
type WorkflowExecution struct {
	ID             string         `json:"id"`
	WorkflowID     string         `json:"workflow_id"`
	TaskID         string         `json:"task_id"`        // The triggering task
	Status         WorkflowStatus `json:"status"`
	CurrentStepID  *string        `json:"current_step_id,omitempty"`
	TotalCostCents int            `json:"total_cost_cents"`
	Context        string         `json:"context"`        // JSON: accumulated context passed between steps
	ErrorMessage   string         `json:"error_message"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    *time.Time     `json:"completed_at,omitempty"`
}

// StepExecution tracks a single step execution within a workflow
type StepExecution struct {
	ID                  string     `json:"id"`
	WorkflowExecutionID string     `json:"workflow_execution_id"`
	StepID              string     `json:"step_id"`
	AgentConfigID       *string    `json:"agent_config_id,omitempty"`
	Status              StepStatus `json:"status"`
	Iteration           int        `json:"iteration"`    // For refinement loops (0-based)
	Input               string     `json:"input"`        // The prompt/input sent to agent
	Output              string     `json:"output"`       // Agent's output
	Score               *float64   `json:"score,omitempty"`  // Quality score (0-1) from review/gate
	CostCents           int        `json:"cost_cents"`
	DurationMs          int64      `json:"duration_ms"`
	ErrorMessage        string     `json:"error_message"`
	StartedAt           time.Time  `json:"started_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
}

// VoteRecord tracks individual agent votes in a voting step
type VoteRecord struct {
	ID              string    `json:"id"`
	StepExecutionID string    `json:"step_execution_id"`
	AgentConfigID   string    `json:"agent_config_id"`
	Choice          string    `json:"choice"`      // The option voted for
	Reasoning       string    `json:"reasoning"`   // Agent's reasoning
	Confidence      float64   `json:"confidence"`  // 0-1 confidence in the vote
	CreatedAt       time.Time `json:"created_at"`
}

// AgentPerformanceMetric tracks agent performance by task type
type AgentPerformanceMetric struct {
	ID              string    `json:"id"`
	AgentConfigID   string    `json:"agent_config_id"`
	TaskType        string    `json:"task_type"`       // "frontend", "backend", "testing", "refactor", "bugfix", "architecture"
	SuccessCount    int       `json:"success_count"`
	FailureCount    int       `json:"failure_count"`
	AvgDurationMs   int64     `json:"avg_duration_ms"`
	AvgCostCents    int       `json:"avg_cost_cents"`
	AvgQualityScore float64   `json:"avg_quality_score"` // 0-1
	LastUpdated     time.Time `json:"last_updated"`
}

// SuccessRate returns the success rate as a percentage
func (m *AgentPerformanceMetric) SuccessRate() float64 {
	total := m.SuccessCount + m.FailureCount
	if total == 0 {
		return 0
	}
	return float64(m.SuccessCount) / float64(total) * 100
}

// WorkflowTemplate represents a pre-defined workflow pattern
type WorkflowTemplate struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Category    WorkflowTemplateCategory `json:"category"`
	Definition  string                   `json:"definition"` // JSON: full workflow definition (steps, config)
	IsBuiltIn   bool                     `json:"is_built_in"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}

// TemplateDefinition is the parsed form of WorkflowTemplate.Definition
type TemplateDefinition struct {
	Strategy WorkflowStrategy     `json:"strategy"`
	Config   WorkflowConfig       `json:"config"`
	Steps    []TemplateStep       `json:"steps"`
}

// TemplateStep is a step definition within a template
type TemplateStep struct {
	Name       string     `json:"name"`
	StepType   StepType   `json:"step_type"`
	StepOrder  int        `json:"step_order"`
	AgentRole  string     `json:"agent_role"`   // "planner", "implementer", "reviewer", "tester" - resolved to actual agent at runtime
	Prompt     string     `json:"prompt"`
	DependsOn  []int      `json:"depends_on"`   // Indexes of steps this depends on
	Config     StepConfig `json:"config"`
}

// ParseTemplateDefinition parses the Definition JSON
func (t *WorkflowTemplate) ParseTemplateDefinition() (*TemplateDefinition, error) {
	if t.Definition == "" || t.Definition == "{}" {
		return &TemplateDefinition{}, nil
	}
	var def TemplateDefinition
	if err := json.Unmarshal([]byte(t.Definition), &def); err != nil {
		return nil, err
	}
	return &def, nil
}

// SetTemplateDefinition serializes a template definition to JSON
func (t *WorkflowTemplate) SetTemplateDefinition(def *TemplateDefinition) error {
	data, err := json.Marshal(def)
	if err != nil {
		return err
	}
	t.Definition = string(data)
	return nil
}

// HandoffContext represents the context passed between workflow steps
type HandoffContext struct {
	StepOutputs map[string]string `json:"step_outputs"` // stepID -> output
	Summary     string            `json:"summary"`      // AI-generated summary for next step
	Decisions   []string          `json:"decisions"`    // Key decisions made so far
	Issues      []string          `json:"issues"`       // Known issues to address
	TaskPrompt  string            `json:"task_prompt"`  // Original task prompt
}

// ParseHandoffContext parses a JSON context string
func ParseHandoffContext(contextJSON string) (*HandoffContext, error) {
	if contextJSON == "" || contextJSON == "{}" {
		return &HandoffContext{StepOutputs: make(map[string]string)}, nil
	}
	var ctx HandoffContext
	if err := json.Unmarshal([]byte(contextJSON), &ctx); err != nil {
		return nil, err
	}
	if ctx.StepOutputs == nil {
		ctx.StepOutputs = make(map[string]string)
	}
	return &ctx, nil
}

// ToJSON serializes the handoff context
func (h *HandoffContext) ToJSON() (string, error) {
	data, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// AgentRole maps template roles to actual agent selection
type AgentRole string

const (
	RolePlanner     AgentRole = "planner"
	RoleImplementer AgentRole = "implementer"
	RoleReviewer    AgentRole = "reviewer"
	RoleTester      AgentRole = "tester"
	RoleCoordinator AgentRole = "coordinator"
)

// TaskTypes for agent specialization tracking
const (
	TaskTypeFrontend     = "frontend"
	TaskTypeBackend      = "backend"
	TaskTypeTesting      = "testing"
	TaskTypeRefactor     = "refactor"
	TaskTypeBugfix       = "bugfix"
	TaskTypeArchitecture = "architecture"
	TaskTypeDocumentation = "documentation"
	TaskTypeGeneral      = "general"
)

var AllTaskTypes = []string{
	TaskTypeFrontend, TaskTypeBackend, TaskTypeTesting,
	TaskTypeRefactor, TaskTypeBugfix, TaskTypeArchitecture,
	TaskTypeDocumentation, TaskTypeGeneral,
}
