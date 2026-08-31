package repository

import (
	"context"
	"fmt"
)

// UpdateAgentDefinition stores the effective task mode selected by lifecycle
// routing. Passing nil clears the selected agent definition.
func (r *TaskRepo) UpdateAgentDefinition(ctx context.Context, taskID string, agentDefinitionID *string) error {
	if taskID == "" {
		return fmt.Errorf("updating task agent definition: missing task id")
	}
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET agent_definition_id = ?, updated_at = datetime('now') WHERE id = ?`,
		agentDefinitionID, taskID)
	if err != nil {
		return fmt.Errorf("updating task agent definition: %w", err)
	}
	return nil
}
