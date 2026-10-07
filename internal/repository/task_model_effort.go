package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
)

// Effort overrides are tied to a saved configuration AND its underlying model.
// Editing a configuration to use another model must not inherit stale effort.
func (r *TaskRepo) SetModelEffort(ctx context.Context, taskID string, agent models.LLMConfig, effort string) error {

	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO task_model_efforts (task_id, agent_config_id, model, effort) VALUES (?, ?, ?, ?)
 ON CONFLICT(task_id, agent_config_id) DO UPDATE SET model=excluded.model, effort=excluded.effort`, taskID, agent.ID, agent.Model, effort)
	return err
}

func (r *TaskRepo) ModelEffort(ctx context.Context, taskID string, agent models.LLMConfig) (string, error) {
	return modelEffort(ctx, r.db, taskID, agent)
}

type modelEffortQuerier interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func modelEffort(ctx context.Context, db modelEffortQuerier, taskID string, agent models.LLMConfig) (string, error) {
	var effort string
	err := db.QueryRowContext(ctx, "SELECT effort FROM task_model_efforts WHERE task_id = ? AND agent_config_id = ? AND model = ?", taskID, agent.ID, agent.Model).Scan(&effort)
	if errors.Is(err, sql.ErrNoRows) {
		// A swarm parent conversation runs on its planner. Other swarm roles
		// keep their own defaults. An explicit empty child row resets inheritance.
		err = db.QueryRowContext(ctx, `SELECT e.effort FROM tasks child
            JOIN tasks parent ON parent.id = child.parent_task_id
            JOIN task_model_efforts e ON e.task_id = parent.id
            WHERE child.id = ? AND child.swarm_role = 'planner'
              AND parent.swarm_role = 'swarm_parent'
              AND e.agent_config_id = ? AND e.model = ?`, taskID, agent.ID, agent.Model).Scan(&effort)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
	}
	if err != nil {
		return "", err
	}
	if !models.ValidConversationEffort(agent, effort) {
		return "", nil
	}
	return effort, nil
}

func (r *TaskRepo) ApplyModelEffort(ctx context.Context, taskID string, agent *models.LLMConfig) error {
	if agent == nil {
		return nil
	}
	effort, err := r.ModelEffort(ctx, taskID, *agent)
	if err == nil && effort != "" {
		agent.ReasoningEffort = effort
	}
	return err
}
