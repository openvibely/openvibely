package events

import (
	"encoding/json"
	"errors"
)

// MaxSubscribers is the maximum number of concurrent SSE subscribers.
// With multi-tab usage, each tab creates one SSE connection. This limit
// prevents resource exhaustion from excessive open connections.
const MaxSubscribers = 50

// ErrMaxSubscribers is returned when the subscriber limit is reached
var ErrMaxSubscribers = errors.New("maximum subscriber limit reached")

// TaskEventType represents the type of task event
type TaskEventType string

const (
	TaskStatusChanged           TaskEventType = "task_status_changed"
	TaskCategoryChanged         TaskEventType = "task_category_changed"
	TaskThreadExecutionStarted  TaskEventType = "task_thread_execution_started"
	TaskThreadInputApplied      TaskEventType = "task_thread_input_applied"
	TaskThreadInputQueued       TaskEventType = "task_thread_input_queued"
	TaskThreadInputSteered      TaskEventType = "task_thread_input_steered"
	TaskThreadInputCancelled    TaskEventType = "task_thread_input_cancelled"
	TaskGoalUpdated             TaskEventType = "task_goal_updated"
	TaskGoalPaused              TaskEventType = "task_goal_paused"
	TaskGoalResumed             TaskEventType = "task_goal_resumed"
	TaskGoalCleared             TaskEventType = "task_goal_cleared"
	TaskGoalEvaluated           TaskEventType = "task_goal_evaluated"
	TaskGoalFollowupQueued      TaskEventType = "task_goal_followup_queued"
	MixtureProgress             TaskEventType = "mixture_progress"
	AlertCreated                TaskEventType = "alert_created"
	AutomationDefinitionUpdated TaskEventType = "automation_definition_updated"
	AutomationInvocationStarted TaskEventType = "automation_invocation_started"
	AutomationInvocationUpdated TaskEventType = "automation_invocation_updated"
	AutomationWorkItemUpdated   TaskEventType = "automation_work_item_updated"
	AutomationTransitionCreated TaskEventType = "automation_transition_created"
	AutomationResourceLinked    TaskEventType = "automation_resource_linked"
)

// TaskEvent represents a task state change event
type TaskEvent struct {
	Type                TaskEventType `json:"type"`
	TaskID              string        `json:"task_id"`
	TaskName            string        `json:"task_name,omitempty"`
	ProjectID           string        `json:"project_id,omitempty"`
	Status              string        `json:"status,omitempty"`
	Category            string        `json:"category,omitempty"`
	OldStatus           string        `json:"old_status,omitempty"`
	OldCategory         string        `json:"old_category,omitempty"`
	AlertID             string        `json:"alert_id,omitempty"`
	ExecID              string        `json:"exec_id,omitempty"`
	Message             string        `json:"message,omitempty"`
	PendingInputID      string        `json:"pending_input_id,omitempty"`
	HasAttachments      bool          `json:"has_attachments,omitempty"`
	Phase               string        `json:"phase,omitempty"`
	TotalReferences     int           `json:"total_references,omitempty"`
	CompletedReferences int           `json:"completed_references,omitempty"`
	GoalID              string        `json:"goal_id,omitempty"`
	GoalStatus          string        `json:"goal_status,omitempty"`
	GoalObjective       string        `json:"goal_objective,omitempty"`
	GoalReason          string        `json:"goal_reason,omitempty"`
	BlockerKey          string        `json:"blocker_key,omitempty"`
	BlockerCount        int           `json:"blocker_count,omitempty"`
	AutomationID        string        `json:"automation_id,omitempty"`
	VersionID           string        `json:"version_id,omitempty"`
	InvocationID        string        `json:"invocation_id,omitempty"`
	WorkItemID          string        `json:"work_item_id,omitempty"`
	NodeID              string        `json:"node_id,omitempty"`
}

// Subscriber is a channel that receives task events
type Subscriber chan TaskEvent

// Broadcaster manages event subscribers and publishes events to them
type Broadcaster struct {
	core broadcaster[TaskEvent, Subscriber]
}

// NewBroadcaster creates a new event broadcaster
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{core: newBroadcaster[TaskEvent, Subscriber](10)}
}

// Subscribe adds a new subscriber and returns a channel for receiving events.
// Returns ErrMaxSubscribers if the subscriber limit has been reached.
func (b *Broadcaster) Subscribe() (Subscriber, error) {
	return b.core.Subscribe()
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Broadcaster) Unsubscribe(sub Subscriber) {
	b.core.Unsubscribe(sub)
}

// Publish sends an event to all subscribers.
func (b *Broadcaster) Publish(event TaskEvent) {
	b.core.Publish(event)
}

// SubscriberCount returns the current number of subscribers.
func (b *Broadcaster) SubscriberCount() int {
	return b.core.SubscriberCount()
}

// ToSSE converts a TaskEvent to SSE format
func (e TaskEvent) ToSSE() string {
	data, _ := json.Marshal(e)
	return "data: " + string(data) + "\n\n"
}
