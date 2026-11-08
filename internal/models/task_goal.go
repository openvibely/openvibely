package models

import "time"

type TaskGoalStatus string

const (
	TaskGoalStatusActive   TaskGoalStatus = "active"
	TaskGoalStatusPaused   TaskGoalStatus = "paused"
	TaskGoalStatusAchieved TaskGoalStatus = "achieved"
	TaskGoalStatusBlocked  TaskGoalStatus = "blocked"
	TaskGoalStatusCleared  TaskGoalStatus = "cleared"
	TaskGoalStatusFailed   TaskGoalStatus = "failed"
)

type TaskGoal struct {
	RunAsUserID       string         `json:"run_as_user_id"`
	TaskID            string         `json:"task_id"`
	GoalID            string         `json:"goal_id"`
	Objective         string         `json:"objective"`
	Status            TaskGoalStatus `json:"status"`
	Reason            string         `json:"reason"`
	BlockerKey        string         `json:"blocker_key"`
	BlockerCount      int            `json:"blocker_count"`
	BlockerReason     string         `json:"blocker_reason"`
	BlockerLastSeenAt *time.Time     `json:"blocker_last_seen_at,omitempty"`
	LastCheckedAt     *time.Time     `json:"last_checked_at,omitempty"`
	AchievedAt        *time.Time     `json:"achieved_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

func (g *TaskGoal) IsEvaluable() bool {
	return g != nil && g.Status == TaskGoalStatusActive
}
