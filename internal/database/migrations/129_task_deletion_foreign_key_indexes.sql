-- +goose Up
-- Index every unindexed task foreign key reached by task deletion. In particular,
-- alerts.execution_id is checked once per cascaded execution and otherwise turns
-- deletion into executions x alerts full-table scans.
CREATE INDEX idx_alerts_execution_id ON alerts(execution_id) WHERE execution_id IS NOT NULL;
CREATE INDEX idx_alerts_task_id ON alerts(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_alerts_source_task_id ON alerts(source_task_id) WHERE source_task_id IS NOT NULL;
CREATE INDEX idx_architect_tasks_task_id ON architect_tasks(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_automation_dispatch_outbox_task ON automation_dispatch_outbox(task_id);
CREATE INDEX idx_conflict_history_task_a ON conflict_history(task_a_id);
CREATE INDEX idx_conflict_history_task_b ON conflict_history(task_b_id);
CREATE INDEX idx_insights_task_id ON insights(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_llm_usage_events_task ON llm_usage_events(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX idx_schedules_task_id ON schedules(task_id);
CREATE INDEX idx_skill_analytics_events_task ON skill_analytics_events(task_id) WHERE task_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_skill_analytics_events_task;
DROP INDEX IF EXISTS idx_schedules_task_id;
DROP INDEX IF EXISTS idx_llm_usage_events_task;
DROP INDEX IF EXISTS idx_insights_task_id;
DROP INDEX IF EXISTS idx_conflict_history_task_b;
DROP INDEX IF EXISTS idx_conflict_history_task_a;
DROP INDEX IF EXISTS idx_automation_dispatch_outbox_task;
DROP INDEX IF EXISTS idx_architect_tasks_task_id;
DROP INDEX IF EXISTS idx_alerts_source_task_id;
DROP INDEX IF EXISTS idx_alerts_task_id;
DROP INDEX IF EXISTS idx_alerts_execution_id;
