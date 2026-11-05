package persistence

import "github.com/openvibely/openvibely/internal/models"

// SwarmTaskWrite changes one swarm task. Empty Status, Category, Title and
// Prompt leave those columns unchanged.
type SwarmTaskWrite struct {
	ID          string
	SwarmStatus string
	SwarmConfig string
	Status      models.TaskStatus
	Category    models.TaskCategory
	Title       string
	Prompt      string
}

// SwarmPlanCommit is applied in one transaction. Parent and Planner are the
// observed rows; any concurrent change to either rejects the whole commit.
type SwarmPlanCommit struct {
	Parent  models.Task
	Planner models.Task
	Writes  []SwarmTaskWrite
	Creates []*models.Task
	// Title returns the candidate title for a duplicate-title retry attempt.
	Title func(sequence int, base string, attempt int) string
	// Worktree names a created child's worktree once its ID is known.
	Worktree func(child *models.Task) (path, branch string)
	Action   string
}
