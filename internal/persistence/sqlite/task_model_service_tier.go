package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
)

// Service tier overrides are tied to a saved configuration AND its underlying model.
// Editing a configuration to use another model must not inherit a stale service tier.
func (r *TaskRepo) SetModelServiceTier(ctx context.Context, taskID string, agent models.LLMConfig, tier string) error {
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO task_model_service_tiers (task_id, agent_config_id, model, service_tier) VALUES (?, ?, ?, ?)
 ON CONFLICT(task_id, agent_config_id) DO UPDATE SET model=excluded.model, service_tier=excluded.service_tier`, taskID, agent.ID, agent.Model, tier)
	return err
}

func (r *TaskRepo) ModelServiceTier(ctx context.Context, taskID string, agent models.LLMConfig) (string, error) {
	return modelServiceTier(ctx, r.db, taskID, agent)
}

type modelServiceTierQuerier interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func modelServiceTier(ctx context.Context, db modelServiceTierQuerier, taskID string, agent models.LLMConfig) (string, error) {
	var tier string
	err := db.QueryRowContext(ctx, "SELECT service_tier FROM task_model_service_tiers WHERE task_id = ? AND agent_config_id = ? AND model = ?", taskID, agent.ID, agent.Model).Scan(&tier)
	if errors.Is(err, sql.ErrNoRows) {
		// A swarm parent conversation runs on its planner. Other swarm roles
		// keep their own defaults. An explicit empty child row resets inheritance.
		err = db.QueryRowContext(ctx, `SELECT e.service_tier FROM tasks child
            JOIN tasks parent ON parent.id = child.parent_task_id
            JOIN task_model_service_tiers e ON e.task_id = parent.id
            WHERE child.id = ? AND child.swarm_role = 'planner'
              AND parent.swarm_role = 'swarm_parent'
              AND e.agent_config_id = ? AND e.model = ?`, taskID, agent.ID, agent.Model).Scan(&tier)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
	}
	if err != nil {
		return "", err
	}
	if !models.ValidServiceTier(agent, tier) {
		return "", nil
	}
	return tier, nil
}
