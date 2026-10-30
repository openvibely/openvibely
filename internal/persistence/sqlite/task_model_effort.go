package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
)

func (r *TaskRepo) SetModelEffort(ctx context.Context, taskID string, agent models.LLMConfig, effort string) error {
	return r.SetModelOverrides(ctx, taskID, agent, &effort, nil)
}

// SetModelOverrides saves both controls atomically. Nil preserves the other
// control; an empty value resets only that control to the model default.
func (r *TaskRepo) SetModelOverrides(ctx context.Context, taskID string, agent models.LLMConfig, effort, tier *string) error {
	_, err := execBoundSQLite(ctx, r.db, `INSERT INTO task_model_overrides(task_id,agent_config_id,model,effort,service_tier)
 VALUES(?,?,?,?,?) ON CONFLICT(task_id,agent_config_id,model) DO UPDATE SET
 effort=COALESCE(excluded.effort,task_model_overrides.effort),
 service_tier=COALESCE(excluded.service_tier,task_model_overrides.service_tier)`, taskID, agent.ID, agent.Model, effort, tier)
	return err
}

func (r *TaskRepo) ModelEffort(ctx context.Context, taskID string, agent models.LLMConfig) (string, error) {
	effort, _, err := modelOverrides(ctx, r.db, taskID, agent)
	return effort, err
}

type modelOverrideQuerier interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// Each field inherits independently. NULL inherits; explicit empty does not.
const taskOverrideJoins = `
 LEFT JOIN tasks parent ON parent.id=t.parent_task_id
     AND t.swarm_role='planner' AND parent.swarm_role='swarm_parent'
 LEFT JOIN task_model_overrides own ON own.task_id=t.id
     AND own.agent_config_id=a.id AND own.model=a.model
 LEFT JOIN task_model_overrides inherited ON inherited.task_id=parent.id
     AND inherited.agent_config_id=a.id AND inherited.model=a.model`

func validModelOverrides(agent models.LLMConfig, effort, tier string) (string, string) {
	if !models.ValidConversationEffort(agent, effort) {
		effort = ""
	}
	if !models.ValidServiceTier(agent, tier) {
		tier = ""
	}
	return effort, tier
}

func modelOverrides(ctx context.Context, db modelOverrideQuerier, taskID string, agent models.LLMConfig) (string, string, error) {
	var effort, tier string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(own.effort,inherited.effort,''),
 COALESCE(own.service_tier,inherited.service_tier,'')
 FROM tasks t CROSS JOIN (SELECT ? AS id, ? AS model) a`+taskOverrideJoins+` WHERE t.id=?`, agent.ID, agent.Model, taskID).Scan(&effort, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	effort, tier = validModelOverrides(agent, effort, tier)
	return effort, tier, nil
}

func (r *TaskRepo) ApplyModelEffort(ctx context.Context, taskID string, agent *models.LLMConfig) error {
	if agent == nil {
		return nil
	}
	effort, tier, err := modelOverrides(ctx, r.db, taskID, *agent)
	if err != nil {
		return err
	}
	if effort != "" {
		agent.ReasoningEffort = effort
	}
	if tier != "" {
		agent.ServiceTier = tier
	}
	return nil
}

// TaskModelSettings reads task, configuration defaults, and both overrides in
// one statement. The picker can request another configuration before selecting it.
func (r *TaskRepo) TaskModelSettings(ctx context.Context, taskID, selectedID string) (*models.TaskModelSettings, error) {
	var out models.TaskModelSettings
	err := r.db.QueryRowContext(ctx, `SELECT t.project_id,COALESCE(a.id,''),COALESCE(a.provider,''),COALESCE(a.model,''),
 COALESCE(a.reasoning_effort,''),COALESCE(a.service_tier,''),
 COALESCE(own.effort,inherited.effort,''),COALESCE(own.service_tier,inherited.service_tier,'')
 FROM tasks t
 LEFT JOIN projects p ON p.id=t.project_id
 LEFT JOIN agent_configs a ON a.id=CASE
 WHEN ?='default' THEN COALESCE(p.default_agent_config_id,
     (SELECT id FROM agent_configs ORDER BY is_default DESC,name ASC,id ASC LIMIT 1))
 WHEN ?='' THEN COALESCE(t.agent_id,p.default_agent_config_id,
     (SELECT id FROM agent_configs WHERE is_default=1 ORDER BY name,id LIMIT 1)) ELSE ? END`+taskOverrideJoins+` WHERE t.id=?`, selectedID, selectedID, selectedID, taskID).Scan(
		&out.ProjectID, &out.Config.ID, &out.Config.Provider, &out.Config.Model, &out.Config.ReasoningEffort, &out.Config.ServiceTier, &out.Effort, &out.ServiceTier)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out.Effort, out.ServiceTier = validModelOverrides(out.Config, out.Effort, out.ServiceTier)
	return &out, nil
}
