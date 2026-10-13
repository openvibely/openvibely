package persistence

import (
	"errors"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

var ErrAutomationTriggerOwned = errors.New("automation trigger schedule is already owned")

type AutomationCardListFilter struct {
	Search         string
	LifecycleState string
	HealthState    string
	AutomationType string
	Adapter        string
	Sort           string
}

// AutomationTaskResource is the persisted task configuration bound to an
// Automation node.
type AutomationTaskResource struct {
	Prompt   string
	Category models.TaskCategory
	Priority int
}

// AutomationScheduleResource is the persisted schedule configuration bound to
// an Automation node.
type AutomationScheduleResource struct {
	TaskID              string
	RunAt               time.Time
	RepeatType          models.RepeatType
	RepeatInterval      int
	ClearContextOnStart bool
}
