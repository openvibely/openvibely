package persistence

import (
	"github.com/openvibely/openvibely/internal/models"
	"time"
)

// AutomationSaveWrite is the complete, already validated state for one atomic
// Automation Save. Graph and resource rows are committed or rolled back together.
type AutomationSaveWrite struct {
	ProjectID              string
	AutomationID           string
	GraphID                string
	ExpectedCurrentGraphID string
	StableKey              string
	Source                 string
	CreatedVia             string
	TemplateRevision       *int
	Candidate              models.AutomationDraftCandidate
	ConfirmationTokenID    string
	ConfirmationPrincipal  string
	ConfirmationThreadID   string
	ConfirmingUserInputID  string
	Tasks                  []AutomationSaveTask
	Schedules              []AutomationSaveSchedule
}

type AutomationSaveTask struct {
	NodeKey           string
	ExistingTaskID    string
	Title             string
	Prompt            string
	Goal              string
	Category          models.TaskCategory
	Priority          int
	AgentID           *string
	AgentDefinitionID *string
	ApplyTopology     bool
	ParentNodeKey     string
	ChildNodeKey      string
	ChildTitle        string
	ChildPromptPrefix string
	ChildCategory     models.TaskCategory
}

type AutomationSaveSchedule struct {
	NodeKey             string
	ExistingScheduleID  string
	TaskNodeKey         string
	RunAt               time.Time
	RepeatType          models.RepeatType
	RepeatInterval      int
	Enabled             bool
	ClearContextOnStart bool
	PreserveTiming      bool
}
