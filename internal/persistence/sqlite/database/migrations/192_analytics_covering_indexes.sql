-- +goose Up
CREATE INDEX IF NOT EXISTS idx_executions_task_analytics
    ON executions(task_id, started_at, status, history_order, id, completed_at, is_followup, agent_config_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_events_task_project_time_cost
    ON llm_usage_events(task_id, project_id, occurred_at, cost_usd, total_tokens);
CREATE INDEX IF NOT EXISTS idx_llm_usage_events_execution_project_time_cost
    ON llm_usage_events(execution_id, project_id, occurred_at, cost_usd);

-- +goose Down
DROP INDEX IF EXISTS idx_llm_usage_events_execution_project_time_cost;
DROP INDEX IF EXISTS idx_llm_usage_events_task_project_time_cost;
DROP INDEX IF EXISTS idx_executions_task_analytics;
