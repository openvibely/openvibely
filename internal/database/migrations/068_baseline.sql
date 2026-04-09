-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys=ON;

CREATE TABLE executions (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT REFERENCES agent_configs(id),
    status          TEXT NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
    prompt_sent     TEXT NOT NULL DEFAULT '',
    output          TEXT NOT NULL DEFAULT '',
    error_message   TEXT NOT NULL DEFAULT '',
    tokens_used     INTEGER NOT NULL DEFAULT 0,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    started_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at    DATETIME
, is_followup INTEGER NOT NULL DEFAULT 0, diff_output TEXT NOT NULL DEFAULT '', cli_session_id TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_executions_task_id ON executions(task_id);
CREATE INDEX idx_executions_status ON executions(status);
CREATE TABLE worker_settings (
    id          TEXT PRIMARY KEY DEFAULT 'singleton',
    max_workers INTEGER NOT NULL DEFAULT 1,
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE task_attachments (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id     TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    media_type  TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_task_attachments_task_id ON task_attachments(task_id);
CREATE TABLE "alerts" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    execution_id TEXT REFERENCES executions(id) ON DELETE SET NULL,
    type TEXT NOT NULL DEFAULT 'task_failed' CHECK(type IN ('task_failed', 'task_needs_followup', 'custom')),
    severity TEXT NOT NULL DEFAULT 'error' CHECK(severity IN ('info', 'warning', 'error')),
    title TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    is_read INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_alerts_project_id ON alerts(project_id);
CREATE INDEX idx_alerts_is_read ON alerts(project_id, is_read);
CREATE INDEX idx_alerts_created_at ON alerts(created_at);
CREATE TABLE "schedules" (
    id              TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_at          DATETIME NOT NULL,
    repeat_type     TEXT NOT NULL DEFAULT 'once'
                    CHECK (repeat_type IN ('once', 'seconds', 'minutes', 'hours', 'daily', 'weekly', 'monthly')),
    repeat_interval INTEGER NOT NULL DEFAULT 1,
    enabled         INTEGER NOT NULL DEFAULT 1,
    next_run        DATETIME,
    last_run        DATETIME,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE chat_attachments (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    file_path   TEXT NOT NULL,
    media_type  TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_chat_attachments_execution_id ON chat_attachments(execution_id);
CREATE TABLE workflow_templates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'custom' CHECK(category IN ('feature', 'refactor', 'bugfix', 'research', 'custom')),
    definition TEXT NOT NULL DEFAULT '{}',
    is_built_in INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE workflows (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    strategy TEXT NOT NULL DEFAULT 'sequential' CHECK(strategy IN ('sequential', 'parallel', 'hybrid', 'adaptive')),
    template_id TEXT REFERENCES workflow_templates(id) ON DELETE SET NULL,
    config TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_workflows_project_id ON workflows(project_id);
CREATE TABLE workflow_steps (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    step_type TEXT NOT NULL DEFAULT 'execute' CHECK(step_type IN ('execute', 'review', 'vote', 'merge', 'gate', 'handoff')),
    step_order INTEGER NOT NULL DEFAULT 0,
    agent_id TEXT REFERENCES agent_configs(id) ON DELETE SET NULL,
    prompt TEXT NOT NULL DEFAULT '',
    depends_on TEXT NOT NULL DEFAULT '[]',
    config TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_workflow_steps_workflow_id ON workflow_steps(workflow_id);
CREATE TABLE workflow_executions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    workflow_id TEXT NOT NULL REFERENCES workflows(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled', 'paused')),
    current_step_id TEXT REFERENCES workflow_steps(id) ON DELETE SET NULL,
    total_cost_cents INTEGER NOT NULL DEFAULT 0,
    context TEXT NOT NULL DEFAULT '{}',
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at DATETIME
);
CREATE INDEX idx_workflow_executions_workflow_id ON workflow_executions(workflow_id);
CREATE INDEX idx_workflow_executions_task_id ON workflow_executions(task_id);
CREATE INDEX idx_workflow_executions_status ON workflow_executions(status);
CREATE TABLE step_executions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    workflow_execution_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES workflow_steps(id),
    agent_config_id TEXT REFERENCES agent_configs(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'running', 'completed', 'failed', 'skipped', 'rolled_back')),
    iteration INTEGER NOT NULL DEFAULT 0,
    input TEXT NOT NULL DEFAULT '',
    output TEXT NOT NULL DEFAULT '',
    score REAL,
    cost_cents INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at DATETIME
);
CREATE INDEX idx_step_executions_workflow_execution_id ON step_executions(workflow_execution_id);
CREATE INDEX idx_step_executions_step_id ON step_executions(step_id);
CREATE TABLE vote_records (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    step_execution_id TEXT NOT NULL REFERENCES step_executions(id) ON DELETE CASCADE,
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id),
    choice TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT '',
    confidence REAL NOT NULL DEFAULT 0.5,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_vote_records_step_execution_id ON vote_records(step_execution_id);
CREATE TABLE agent_performance_metrics (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    agent_config_id TEXT NOT NULL REFERENCES agent_configs(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL DEFAULT 'general',
    success_count INTEGER NOT NULL DEFAULT 0,
    failure_count INTEGER NOT NULL DEFAULT 0,
    avg_duration_ms INTEGER NOT NULL DEFAULT 0,
    avg_cost_cents INTEGER NOT NULL DEFAULT 0,
    avg_quality_score REAL NOT NULL DEFAULT 0,
    last_updated DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(agent_config_id, task_type)
);
CREATE INDEX idx_agent_performance_agent_id ON agent_performance_metrics(agent_config_id);
CREATE TABLE impact_analyses (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    files_impacted TEXT NOT NULL DEFAULT '[]',        -- JSON array of file paths
    apis_impacted TEXT NOT NULL DEFAULT '[]',         -- JSON array of API endpoints
    schemas_impacted TEXT NOT NULL DEFAULT '[]',      -- JSON array of DB table/column refs
    components_impacted TEXT NOT NULL DEFAULT '[]',   -- JSON array of system components
    impact_summary TEXT NOT NULL DEFAULT '',          -- AI-generated natural language summary
    confidence REAL NOT NULL DEFAULT 0.5,             -- 0-1 confidence score
    analysis_model TEXT NOT NULL DEFAULT '',          -- Model used for analysis
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_impact_analyses_task_id ON impact_analyses(task_id);
CREATE INDEX idx_impact_analyses_project_id ON impact_analyses(project_id);
CREATE TABLE conflict_predictions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_a_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_b_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    conflict_type TEXT NOT NULL CHECK(conflict_type IN ('file', 'api', 'schema', 'component', 'semantic')),
    severity TEXT NOT NULL CHECK(severity IN ('low', 'medium', 'high', 'critical')),
    description TEXT NOT NULL DEFAULT '',
    overlapping_resources TEXT NOT NULL DEFAULT '[]', -- JSON array of shared resources
    resolution_strategy TEXT NOT NULL DEFAULT '',     -- Suggested resolution
    status TEXT NOT NULL DEFAULT 'detected' CHECK(status IN ('detected', 'acknowledged', 'resolved', 'false_positive')),
    resolved_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_conflict_predictions_project_id ON conflict_predictions(project_id);
CREATE INDEX idx_conflict_predictions_task_a ON conflict_predictions(task_a_id);
CREATE INDEX idx_conflict_predictions_task_b ON conflict_predictions(task_b_id);
CREATE INDEX idx_conflict_predictions_status ON conflict_predictions(status);
CREATE TABLE conflict_history (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_a_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    task_b_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    prediction_id TEXT REFERENCES conflict_predictions(id) ON DELETE SET NULL,
    was_predicted INTEGER NOT NULL DEFAULT 0,     -- 1 if this was predicted, 0 if it was a surprise
    conflict_type TEXT NOT NULL,
    actual_files TEXT NOT NULL DEFAULT '[]',       -- JSON array of files that actually conflicted
    resolution TEXT NOT NULL DEFAULT '',           -- How it was resolved
    impact_score REAL NOT NULL DEFAULT 0.0,       -- 0-1 severity of actual impact
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_conflict_history_project_id ON conflict_history(project_id);
CREATE INDEX idx_conflict_history_prediction_id ON conflict_history(prediction_id);
CREATE TABLE execution_order_recommendations (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_ids TEXT NOT NULL DEFAULT '[]',            -- JSON array of task IDs in recommended order
    reasoning TEXT NOT NULL DEFAULT '',             -- AI explanation for the ordering
    conflict_count INTEGER NOT NULL DEFAULT 0,     -- Number of conflicts this ordering prevents
    batch_groups TEXT NOT NULL DEFAULT '[]',        -- JSON array of arrays: tasks that can run together
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'accepted', 'rejected', 'expired')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at DATETIME NOT NULL DEFAULT (datetime('now', '+1 hour'))
);
CREATE INDEX idx_execution_order_project_id ON execution_order_recommendations(project_id);
CREATE INDEX idx_execution_order_status ON execution_order_recommendations(status);
CREATE TABLE insights (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK(type IN ('bug_pattern', 'incomplete_feature', 'tech_debt', 'optimization', 'dependency', 'knowledge', 'proactive_suggestion')),
    severity TEXT NOT NULL DEFAULT 'medium' CHECK(severity IN ('info', 'low', 'medium', 'high', 'critical')),
    status TEXT NOT NULL DEFAULT 'new' CHECK(status IN ('new', 'reviewed', 'accepted', 'rejected', 'resolved')),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '{}',
    suggestion TEXT NOT NULL DEFAULT '',
    impact TEXT NOT NULL DEFAULT '',
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    resolved_at DATETIME
);
CREATE INDEX idx_insights_project_id ON insights(project_id);
CREATE INDEX idx_insights_type ON insights(type);
CREATE INDEX idx_insights_status ON insights(status);
CREATE INDEX idx_insights_project_status ON insights(project_id, status);
CREATE TABLE insight_reports (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    report_date TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    insight_ids TEXT NOT NULL DEFAULT '[]',
    stats TEXT NOT NULL DEFAULT '{}',
    analysis_log TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_insight_reports_project ON insight_reports(project_id);
CREATE INDEX idx_insight_reports_date ON insight_reports(project_id, report_date);
CREATE TABLE knowledge_entries (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    topic TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_ref TEXT NOT NULL DEFAULT '',
    tags TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_knowledge_project ON knowledge_entries(project_id);
CREATE INDEX idx_knowledge_topic ON knowledge_entries(topic);
CREATE TABLE "architect_sessions" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'completed', 'abandoned')),
    phase TEXT NOT NULL DEFAULT 'vision_refinement' CHECK(phase IN ('vision_refinement', 'architecture', 'risk_analysis', 'phasing', 'dependencies', 'estimation', 'review', 'complete')),
    vision_data TEXT NOT NULL DEFAULT '{}',
    arch_data TEXT NOT NULL DEFAULT '{}',
    risk_data TEXT NOT NULL DEFAULT '{}',
    phase_data TEXT NOT NULL DEFAULT '{}',
    dep_data TEXT NOT NULL DEFAULT '{}',
    est_data TEXT NOT NULL DEFAULT '{}',
    template_id TEXT REFERENCES "architect_templates"(id) ON DELETE SET NULL,
    created_at DATETIME DEFAULT (datetime('now')),
    updated_at DATETIME DEFAULT (datetime('now'))
);
CREATE TABLE "architect_messages" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    session_id TEXT NOT NULL REFERENCES "architect_sessions"(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
    content TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL DEFAULT 'vision_refinement',
    created_at DATETIME DEFAULT (datetime('now'))
);
CREATE TABLE "architect_tasks" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    session_id TEXT NOT NULL REFERENCES "architect_sessions"(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL DEFAULT 'mvp' CHECK(phase IN ('mvp', 'phase_2', 'phase_3', 'mitigation')),
    priority INTEGER NOT NULL DEFAULT 2,
    depends_on TEXT NOT NULL DEFAULT '[]',
    is_blocking INTEGER NOT NULL DEFAULT 0,
    complexity TEXT NOT NULL DEFAULT 'medium' CHECK(complexity IN ('low', 'medium', 'high')),
    est_hours REAL NOT NULL DEFAULT 0,
    task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    is_activated INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT (datetime('now'))
);
CREATE TABLE "architect_templates" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'general',
    vision_data TEXT NOT NULL DEFAULT '{}',
    arch_data TEXT NOT NULL DEFAULT '{}',
    tasks_data TEXT NOT NULL DEFAULT '[]',
    usage_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT (datetime('now')),
    updated_at DATETIME DEFAULT (datetime('now'))
);
CREATE TABLE backlog_suggestions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK(type IN ('reprioritize', 'obsolete', 'decompose', 'quick_win', 'schedule', 'stale')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'approved', 'rejected', 'applied', 'expired')),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    suggested_priority INTEGER,
    suggested_subtasks TEXT NOT NULL DEFAULT '[]', -- JSON array of subtask descriptions
    reasoning TEXT NOT NULL DEFAULT '',
    confidence REAL NOT NULL DEFAULT 0.5,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    applied_at DATETIME
);
CREATE INDEX idx_backlog_suggestions_project ON backlog_suggestions(project_id);
CREATE INDEX idx_backlog_suggestions_task ON backlog_suggestions(task_id);
CREATE INDEX idx_backlog_suggestions_status ON backlog_suggestions(project_id, status);
CREATE TABLE backlog_health_snapshots (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    total_tasks INTEGER NOT NULL DEFAULT 0,
    avg_age_days REAL NOT NULL DEFAULT 0,
    stale_count INTEGER NOT NULL DEFAULT 0,  -- tasks older than 30 days
    high_priority_count INTEGER NOT NULL DEFAULT 0,
    completion_velocity REAL NOT NULL DEFAULT 0,  -- tasks completed per day (7-day avg)
    bottleneck_tags TEXT NOT NULL DEFAULT '[]', -- JSON: tags with most stale tasks
    health_score REAL NOT NULL DEFAULT 0,  -- 0-100 overall health score
    details TEXT NOT NULL DEFAULT '{}', -- JSON: additional metrics
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_backlog_health_project ON backlog_health_snapshots(project_id);
CREATE TABLE backlog_analysis_reports (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    report_date TEXT NOT NULL, -- YYYY-MM-DD
    summary TEXT NOT NULL DEFAULT '',
    suggestion_ids TEXT NOT NULL DEFAULT '[]', -- JSON array of suggestion IDs
    stats TEXT NOT NULL DEFAULT '{}', -- JSON stats
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_backlog_reports_project ON backlog_analysis_reports(project_id);
CREATE INDEX idx_architect_sessions_project ON architect_sessions(project_id, status);
CREATE INDEX idx_architect_messages_session ON architect_messages(session_id, created_at);
CREATE INDEX idx_architect_tasks_session ON architect_tasks(session_id, phase);
CREATE INDEX idx_architect_templates_category ON architect_templates(category);
CREATE TABLE autonomous_builds (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    build_date TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'discovering', 'selecting', 'designing', 'implementing', 'testing', 'reviewing', 'quality_gates', 'ready_for_approval', 'approved', 'merged', 'rejected', 'failed')),
    selected_feature_id TEXT,
    approval_status TEXT NOT NULL DEFAULT 'pending' CHECK(approval_status IN ('pending', 'approved', 'rejected')),
    branch_name TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
, task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL);
CREATE INDEX idx_autonomous_builds_project ON autonomous_builds(project_id);
CREATE INDEX idx_autonomous_builds_date ON autonomous_builds(build_date);
CREATE INDEX idx_autonomous_builds_status ON autonomous_builds(status);
CREATE TABLE autonomous_features (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    build_id TEXT NOT NULL REFERENCES autonomous_builds(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    problem_statement TEXT NOT NULL DEFAULT '',
    implementation_approach TEXT NOT NULL DEFAULT '',
    estimated_complexity INTEGER NOT NULL DEFAULT 3 CHECK(estimated_complexity BETWEEN 1 AND 5),
    expected_impact INTEGER NOT NULL DEFAULT 3 CHECK(expected_impact BETWEEN 1 AND 5),
    risk_assessment TEXT NOT NULL DEFAULT '',
    voting_results TEXT NOT NULL DEFAULT '{}',
    final_score REAL NOT NULL DEFAULT 0,
    selected INTEGER NOT NULL DEFAULT 0,
    implementation_status TEXT NOT NULL DEFAULT 'proposed' CHECK(implementation_status IN ('proposed', 'selected', 'designing', 'implementing', 'testing', 'reviewing', 'completed', 'failed')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_autonomous_features_build ON autonomous_features(build_id);
CREATE INDEX idx_autonomous_features_project ON autonomous_features(project_id);
CREATE TABLE autonomous_build_logs (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    build_id TEXT NOT NULL REFERENCES autonomous_builds(id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    step TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    output TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_autonomous_build_logs_build ON autonomous_build_logs(build_id);
CREATE TABLE autonomous_sandboxes (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    build_id TEXT NOT NULL REFERENCES autonomous_builds(id) ON DELETE CASCADE,
    branch_name TEXT NOT NULL,
    worktree_path TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'merged', 'deleted', 'failed')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_autonomous_sandboxes_build ON autonomous_sandboxes(build_id);
CREATE TABLE autonomous_config (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL UNIQUE REFERENCES projects(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 0,
    max_execution_hours INTEGER NOT NULL DEFAULT 4,
    protected_files TEXT NOT NULL DEFAULT '[]',
    excluded_areas TEXT NOT NULL DEFAULT '[]',
    schedule_hour INTEGER NOT NULL DEFAULT 23,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE task_executions (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_config_id TEXT REFERENCES agent_configs(id) ON DELETE SET NULL,
    task_type TEXT NOT NULL DEFAULT 'general',
    status TEXT NOT NULL CHECK(status IN ('completed', 'failed', 'cancelled')),
    cost_cents INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    quality_score REAL,
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    completed_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_task_executions_task_id ON task_executions(task_id);
CREATE INDEX idx_task_executions_agent_config_id ON task_executions(agent_config_id);
CREATE INDEX idx_task_executions_task_type ON task_executions(task_type);
CREATE INDEX idx_task_executions_completed_at ON task_executions(completed_at);
CREATE TABLE agent_cost_budgets (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    agent_config_id TEXT NOT NULL UNIQUE REFERENCES agent_configs(id) ON DELETE CASCADE,
    daily_limit_cents INTEGER NOT NULL DEFAULT 0,
    monthly_limit_cents INTEGER NOT NULL DEFAULT 0,
    current_daily_cents INTEGER NOT NULL DEFAULT 0,
    current_monthly_cents INTEGER NOT NULL DEFAULT 0,
    last_daily_reset DATETIME NOT NULL DEFAULT (datetime('now')),
    last_monthly_reset DATETIME NOT NULL DEFAULT (datetime('now')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE agent_leaderboard_snapshots (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_type TEXT NOT NULL,
    snapshot_data TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_leaderboard_project_task ON agent_leaderboard_snapshots(project_id, task_type, created_at DESC);
CREATE TABLE task_templates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    default_prompt TEXT NOT NULL DEFAULT '',
    suggested_agent_id TEXT REFERENCES agent_configs(id) ON DELETE SET NULL,
    category TEXT NOT NULL DEFAULT 'backlog' CHECK(category IN ('active', 'backlog')),
    priority INTEGER NOT NULL DEFAULT 2,
    tag TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    category_filter TEXT NOT NULL DEFAULT 'all' CHECK(category_filter IN ('all', 'code_review', 'bug_fix', 'feature', 'refactor', 'documentation', 'testing', 'research', 'deployment', 'maintenance', 'planning')),
    is_built_in INTEGER NOT NULL DEFAULT 0,
    is_favorite INTEGER NOT NULL DEFAULT 0,
    usage_count INTEGER NOT NULL DEFAULT 0,
    created_by TEXT NOT NULL DEFAULT 'system',
    created_at DATETIME DEFAULT (datetime('now')),
    updated_at DATETIME DEFAULT (datetime('now'))
);
CREATE INDEX idx_task_templates_project ON task_templates(project_id, category_filter);
CREATE INDEX idx_task_templates_favorite ON task_templates(is_favorite, usage_count DESC);
CREATE INDEX idx_task_templates_category ON task_templates(category_filter);
CREATE INDEX idx_autonomous_builds_task ON autonomous_builds(task_id);
CREATE TABLE prompt_patterns (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    template_text TEXT NOT NULL,
    variables TEXT NOT NULL DEFAULT '[]', -- JSON array of variable names
    category TEXT NOT NULL DEFAULT 'custom' CHECK(category IN ('debugging', 'testing', 'refactoring', 'documentation', 'code_review', 'optimization', 'feature', 'custom')),
    is_builtin INTEGER NOT NULL DEFAULT 0,
    usage_count INTEGER NOT NULL DEFAULT 0,
    last_used_at DATETIME,
    tags TEXT NOT NULL DEFAULT '[]', -- JSON array of tags
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_prompt_patterns_project ON prompt_patterns(project_id);
CREATE INDEX idx_prompt_patterns_category ON prompt_patterns(category);
CREATE INDEX idx_prompt_patterns_usage ON prompt_patterns(usage_count DESC);
CREATE UNIQUE INDEX idx_prompt_patterns_title_project ON prompt_patterns(project_id, title);
CREATE TABLE pattern_usage_history (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    pattern_id TEXT NOT NULL REFERENCES prompt_patterns(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    variables_applied TEXT NOT NULL DEFAULT '{}', -- JSON object of variable values
    result_status TEXT NOT NULL DEFAULT 'unknown' CHECK(result_status IN ('unknown', 'success', 'failed', 'cancelled')),
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_pattern_usage_pattern ON pattern_usage_history(pattern_id);
CREATE INDEX idx_pattern_usage_task ON pattern_usage_history(task_id);
CREATE TABLE x_credentials (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    api_key TEXT NOT NULL DEFAULT '',
    api_secret TEXT NOT NULL DEFAULT '',
    access_token TEXT NOT NULL DEFAULT '',
    access_token_secret TEXT NOT NULL DEFAULT '',
    bearer_token TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(project_id)
);
CREATE TABLE trend_sources (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK(source_type IN ('hashtag', 'account', 'keyword', 'competitor')),
    value TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_trend_sources_project ON trend_sources(project_id);
CREATE TABLE trend_entries (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_id TEXT REFERENCES trend_sources(id) ON DELETE SET NULL,
    source_type TEXT NOT NULL DEFAULT 'manual',
    content TEXT NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    engagement_score INTEGER NOT NULL DEFAULT 0,
    sentiment TEXT NOT NULL DEFAULT 'neutral' CHECK(sentiment IN ('positive', 'negative', 'neutral', 'mixed')),
    collected_at DATETIME NOT NULL DEFAULT (datetime('now')),
    raw_data TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_trend_entries_project ON trend_entries(project_id);
CREATE INDEX idx_trend_entries_source ON trend_entries(source_id);
CREATE INDEX idx_trend_entries_collected ON trend_entries(collected_at);
CREATE TABLE trend_patterns (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    pattern_type TEXT NOT NULL CHECK(pattern_type IN ('feature_request', 'pain_point', 'emerging_tech', 'market_shift', 'competitor_move', 'user_sentiment')),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    evidence TEXT NOT NULL DEFAULT '[]',
    confidence REAL NOT NULL DEFAULT 0.5,
    signal_count INTEGER NOT NULL DEFAULT 1,
    first_seen DATETIME NOT NULL DEFAULT (datetime('now')),
    last_seen DATETIME NOT NULL DEFAULT (datetime('now')),
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'implemented', 'dismissed', 'stale')),
    led_to_feature TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_trend_patterns_project ON trend_patterns(project_id);
CREATE INDEX idx_trend_patterns_status ON trend_patterns(status);
CREATE INDEX idx_trend_patterns_type ON trend_patterns(pattern_type);
CREATE TABLE competitor_updates (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    competitor_name TEXT NOT NULL,
    update_type TEXT NOT NULL CHECK(update_type IN ('feature_launch', 'changelog', 'pricing_change', 'acquisition', 'partnership', 'user_feedback')),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    impact_assessment TEXT NOT NULL DEFAULT '',
    relevance_score REAL NOT NULL DEFAULT 0.5,
    detected_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_competitor_updates_project ON competitor_updates(project_id);
CREATE INDEX idx_competitor_updates_competitor ON competitor_updates(competitor_name);
CREATE TABLE app_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_executions_task_status ON executions(task_id, status);
CREATE INDEX idx_schedules_enabled_nextrun ON schedules(enabled, next_run);
CREATE TABLE health_checks (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grade TEXT NOT NULL DEFAULT '',
    strengths TEXT NOT NULL DEFAULT '',
    improvements TEXT NOT NULL DEFAULT '',
    assessment TEXT NOT NULL DEFAULT '',
    how_to_improve TEXT NOT NULL DEFAULT '',
    tasks_total INTEGER NOT NULL DEFAULT 0,
    tasks_completed INTEGER NOT NULL DEFAULT 0,
    tasks_failed INTEGER NOT NULL DEFAULT 0,
    tasks_pending INTEGER NOT NULL DEFAULT 0,
    backlog_size INTEGER NOT NULL DEFAULT 0,
    avg_completion_pct REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_health_checks_project_id ON health_checks(project_id);
CREATE INDEX idx_health_checks_created_at ON health_checks(project_id, created_at);
CREATE TABLE projects (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    repo_path TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT 0,
    default_agent_config_id TEXT REFERENCES agent_configs(id) ON DELETE SET NULL,
    max_workers INTEGER,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
, repo_url TEXT NOT NULL DEFAULT '');
CREATE TABLE review_comments (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    line_number INTEGER NOT NULL,
    line_type TEXT NOT NULL DEFAULT 'new',
    comment_text TEXT NOT NULL,
    reviewed_by TEXT NOT NULL DEFAULT 'user',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_review_comments_task ON review_comments(task_id);
CREATE TABLE "telegram_authorized_users" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    telegram_user_id INTEGER NOT NULL,
    telegram_username TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_telegram_auth_unique_user_id
    ON telegram_authorized_users(project_id, telegram_user_id)
    WHERE telegram_user_id != 0;
CREATE UNIQUE INDEX idx_telegram_auth_unique_username
    ON telegram_authorized_users(project_id, telegram_username)
    WHERE telegram_username != '';
CREATE INDEX idx_telegram_auth_project ON telegram_authorized_users(project_id);
CREATE INDEX idx_telegram_auth_user ON telegram_authorized_users(telegram_user_id);
CREATE TABLE idea_grades (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    grade TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    strengths TEXT NOT NULL DEFAULT '',
    improvements TEXT NOT NULL DEFAULT '',
    how_to_next_grade TEXT NOT NULL DEFAULT '',
    next_grade TEXT NOT NULL DEFAULT '',
    tasks_evaluated INTEGER NOT NULL DEFAULT 0,
    clarity_score REAL NOT NULL DEFAULT 0,
    ambition_score REAL NOT NULL DEFAULT 0,
    follow_through REAL NOT NULL DEFAULT 0,
    diversity_score REAL NOT NULL DEFAULT 0,
    strategy_score REAL NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_idea_grades_project_id ON idea_grades(project_id);
CREATE INDEX idx_idea_grades_created_at ON idea_grades(project_id, created_at);
CREATE INDEX idx_executions_agent_status ON executions(agent_config_id, status);
CREATE INDEX idx_executions_started_at ON executions(started_at);
CREATE INDEX idx_alerts_project_is_read ON alerts(project_id, is_read);
CREATE INDEX idx_insights_project_type ON insights(project_id, type);
CREATE INDEX idx_backlog_suggestions_project_status ON backlog_suggestions(project_id, status);
CREATE INDEX idx_backlog_suggestions_project_type ON backlog_suggestions(project_id, type);
CREATE INDEX idx_prompt_patterns_project_category ON prompt_patterns(project_id, category);
CREATE INDEX idx_prompt_patterns_project_usage ON prompt_patterns(project_id, usage_count DESC);
CREATE INDEX idx_trend_sources_project_enabled ON trend_sources(project_id, enabled);
CREATE TABLE custom_personalities (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name TEXT NOT NULL,
    key TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE "tasks" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'active'
                 CHECK (category IN ('active', 'freezer', 'completed', 'backlog', 'scheduled', 'chat')),
    priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'queued', 'running', 'completed', 'failed', 'cancelled', 'blocked')),
    prompt TEXT NOT NULL DEFAULT '',
    agent_id TEXT REFERENCES agent_configs(id),
    tag TEXT NOT NULL DEFAULT ''
                 CHECK (tag IN ('', 'feature', 'bug')),
    display_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now')),
    parent_task_id TEXT REFERENCES tasks(id) ON DELETE SET NULL,
    chain_config TEXT NOT NULL DEFAULT '{}',
    execution_cost_cents INTEGER NOT NULL DEFAULT 0,
    execution_duration_ms INTEGER NOT NULL DEFAULT 0
, worktree_path TEXT NOT NULL DEFAULT '', worktree_branch TEXT NOT NULL DEFAULT '', auto_merge INTEGER NOT NULL DEFAULT 0, merge_target_branch TEXT NOT NULL DEFAULT '', merge_status TEXT NOT NULL DEFAULT ''
    CHECK (merge_status IN ('', 'pending', 'merged', 'failed', 'conflict')), created_via TEXT NOT NULL DEFAULT '', telegram_chat_id INTEGER NOT NULL DEFAULT 0, agent_definition_id TEXT REFERENCES agents(id) ON DELETE SET NULL, base_branch TEXT NOT NULL DEFAULT '', base_commit_sha TEXT NOT NULL DEFAULT '', lineage_depth INTEGER NOT NULL DEFAULT 0);
CREATE INDEX idx_tasks_project_id ON tasks(project_id);
CREATE INDEX idx_tasks_category ON tasks(category);
CREATE INDEX idx_tasks_status ON tasks(status);
CREATE UNIQUE INDEX idx_tasks_project_title ON tasks(project_id, title);
CREATE INDEX idx_tasks_display_order ON tasks(project_id, category, display_order);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);
CREATE TABLE telegram_user_projects (
    telegram_user_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    updated_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_telegram_user_projects_project ON telegram_user_projects(project_id);
CREATE TABLE "agent_configs" (
    id                  TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name                TEXT NOT NULL,
    provider            TEXT NOT NULL DEFAULT 'anthropic'
                        CHECK (provider IN ('anthropic', 'openai', 'ollama', 'test')),
    model               TEXT NOT NULL DEFAULT 'claude-sonnet-4-5-20250929',
    api_key             TEXT NOT NULL DEFAULT '',
    max_tokens          INTEGER NOT NULL DEFAULT 4096,
    temperature         REAL NOT NULL DEFAULT 0.0,
    is_default          INTEGER NOT NULL DEFAULT 0,
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    auth_method         TEXT NOT NULL DEFAULT 'cli'
                        CHECK (auth_method IN ('cli', 'oauth', 'api_key')),
    oauth_access_token  TEXT NOT NULL DEFAULT '',
    oauth_refresh_token TEXT NOT NULL DEFAULT '',
    oauth_expires_at    INTEGER NOT NULL DEFAULT 0,
    reasoning_effort    TEXT NOT NULL DEFAULT ''
                        CHECK (reasoning_effort IN ('', 'low', 'medium', 'high', 'xhigh')),
    max_workers         INTEGER NOT NULL DEFAULT 0,
    worker_timeout      INTEGER NOT NULL DEFAULT 0,
    oauth_client_id     TEXT NOT NULL DEFAULT '',
    oauth_client_secret TEXT NOT NULL DEFAULT '',
    oauth_authorize_url TEXT NOT NULL DEFAULT '',
    oauth_token_url     TEXT NOT NULL DEFAULT '',
    oauth_scopes        TEXT NOT NULL DEFAULT '',
    ollama_base_url     TEXT NOT NULL DEFAULT ''
, oauth_account_id TEXT NOT NULL DEFAULT '', auto_start_tasks INTEGER NOT NULL DEFAULT 0);
-- +goose StatementBegin
CREATE TRIGGER update_agent_configs_timestamp
AFTER UPDATE ON agent_configs
FOR EACH ROW
BEGIN
    UPDATE agent_configs SET updated_at = datetime('now') WHERE id = OLD.id;
END;
-- +goose StatementEnd
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT 'inherit',
    tools TEXT NOT NULL DEFAULT '[]',
    mcp_servers TEXT NOT NULL DEFAULT '[]',
    skills TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, plugins TEXT NOT NULL DEFAULT '[]');
CREATE TABLE task_pull_requests (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    task_id    TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    pr_number  INTEGER NOT NULL,
    pr_url     TEXT NOT NULL,
    pr_state   TEXT NOT NULL DEFAULT 'open',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_task_pull_requests_task_id ON task_pull_requests(task_id);
CREATE INDEX idx_task_pull_requests_pr_number ON task_pull_requests(pr_number);
CREATE TABLE slack_user_projects (
    slack_team_id TEXT NOT NULL,
    slack_user_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    updated_at TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (slack_team_id, slack_user_id)
);
CREATE INDEX idx_slack_user_projects_project ON slack_user_projects(project_id);
CREATE TABLE slack_task_context (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    slack_team_id TEXT NOT NULL,
    slack_channel_id TEXT NOT NULL,
    slack_thread_ts TEXT NOT NULL,
    slack_user_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_slack_task_context_team_channel ON slack_task_context(slack_team_id, slack_channel_id);
CREATE TABLE slack_authorized_users (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slack_user_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_slack_auth_unique_user_id ON slack_authorized_users(project_id, slack_user_id);
CREATE INDEX idx_slack_auth_project ON slack_authorized_users(project_id);
CREATE INDEX idx_slack_auth_user ON slack_authorized_users(slack_user_id);

INSERT INTO app_settings (key, value) VALUES ('worktree_auto_merge', 'false');
INSERT INTO app_settings (key, value) VALUES ('worktree_merge_target', 'main');
INSERT INTO app_settings (key, value) VALUES ('worktree_cleanup', 'after_merge');
INSERT INTO architect_templates (id, name, description, category, vision_data, arch_data, tasks_data, usage_count, created_at, updated_at) VALUES ('4bfcd12946b580d2aa54d500f0f6367a', 'REST API with Auth', 'Backend REST API with user authentication, JWT tokens, and RBAC', 'api', '{"project_goals":"Build a secure REST API with authentication and authorization","target_users":"Developers consuming the API","tech_preferences":"Go or Node.js"}', '{"summary":"REST API with JWT auth, middleware-based RBAC, and PostgreSQL storage"}', '[{"title":"Setup project scaffold","phase":"mvp","priority":4,"complexity":"low","est_hours":2},{"title":"Implement user registration and login","phase":"mvp","priority":4,"complexity":"medium","est_hours":8},{"title":"Add JWT token generation and validation","phase":"mvp","priority":4,"complexity":"medium","est_hours":6},{"title":"Create RBAC middleware","phase":"mvp","priority":3,"complexity":"high","est_hours":10},{"title":"Add API rate limiting","phase":"phase_2","priority":2,"complexity":"medium","est_hours":4},{"title":"Implement OAuth2 social login","phase":"phase_2","priority":2,"complexity":"high","est_hours":12},{"title":"Add API documentation with Swagger","phase":"phase_3","priority":1,"complexity":"low","est_hours":4}]', 0, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO architect_templates (id, name, description, category, vision_data, arch_data, tasks_data, usage_count, created_at, updated_at) VALUES ('2b98dd6cc69f86da4155b82e49fe1d76', 'React Dashboard', 'Admin dashboard with charts, data tables, and real-time updates', 'web_app', '{"project_goals":"Build an admin dashboard for data visualization and management","target_users":"Internal team members and administrators","tech_preferences":"React, TypeScript, Tailwind CSS"}', '{"summary":"React SPA with TypeScript, component library, REST/WebSocket backend"}', '[{"title":"Setup React project with TypeScript and Tailwind","phase":"mvp","priority":4,"complexity":"low","est_hours":2},{"title":"Create layout with sidebar navigation","phase":"mvp","priority":3,"complexity":"medium","est_hours":6},{"title":"Build data table component with sorting and filtering","phase":"mvp","priority":3,"complexity":"high","est_hours":10},{"title":"Add chart components for data visualization","phase":"mvp","priority":3,"complexity":"medium","est_hours":8},{"title":"Implement real-time updates via WebSocket","phase":"phase_2","priority":2,"complexity":"high","est_hours":12},{"title":"Add export functionality (CSV, PDF)","phase":"phase_2","priority":2,"complexity":"medium","est_hours":6},{"title":"Performance optimization and lazy loading","phase":"phase_3","priority":1,"complexity":"medium","est_hours":8}]', 0, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO architect_templates (id, name, description, category, vision_data, arch_data, tasks_data, usage_count, created_at, updated_at) VALUES ('a4dc7c651b194ab1e6c486c3fad9be35', 'Data Pipeline', 'ETL data pipeline with scheduling, monitoring, and error handling', 'data_pipeline', '{"project_goals":"Build a reliable ETL pipeline for data ingestion and transformation","target_users":"Data engineers and analysts","tech_preferences":"Python, Apache Airflow or similar"}', '{"summary":"Python-based ETL with scheduling, monitoring, and retry logic"}', '[{"title":"Setup pipeline framework and configuration","phase":"mvp","priority":4,"complexity":"medium","est_hours":6},{"title":"Implement data source connectors","phase":"mvp","priority":4,"complexity":"medium","est_hours":8},{"title":"Build transformation engine","phase":"mvp","priority":3,"complexity":"high","est_hours":12},{"title":"Add scheduling and orchestration","phase":"mvp","priority":3,"complexity":"medium","est_hours":8},{"title":"Implement monitoring and alerting","phase":"phase_2","priority":2,"complexity":"medium","est_hours":8},{"title":"Add data validation and quality checks","phase":"phase_2","priority":2,"complexity":"high","est_hours":10},{"title":"Build web dashboard for pipeline status","phase":"phase_3","priority":1,"complexity":"medium","est_hours":12}]', 0, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO projects (id, name, description, repo_path, is_default, default_agent_config_id, max_workers, created_at, updated_at, repo_url) VALUES ('default', 'Default', 'Default project for unassigned tasks', '', 1, NULL, NULL, '2026-04-04 16:31:09', '2026-04-04 16:31:09', '');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('4cc3d00160e97852065945fe337d3350', NULL, 'Code Review', 'Review code changes for quality and best practices', 'Review the code changes in this pull request. Check for:
- Code quality and adherence to best practices
- Potential bugs or edge cases
- Security vulnerabilities
- Performance concerns
- Test coverage

Provide specific, actionable feedback.', NULL, 'active', 3, 'feature', '[]', 'code_review', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('ae6b75aeeb08a065fef3483d4921019d', NULL, 'Bug Investigation', 'Investigate and diagnose a reported bug', 'Investigate the following bug:

[Describe the bug, including:
- Expected behavior
- Actual behavior
- Steps to reproduce
- Environment details]

Analyze the codebase to identify the root cause and suggest a fix.', NULL, 'active', 4, 'bug', '[]', 'bug_fix', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('225502e9c325425acba48ff39cca13c1', NULL, 'Feature Planning', 'Plan architecture and implementation for a new feature', 'Plan the implementation for the following feature:

[Feature description]

Provide:
- Architecture overview
- Key components and their responsibilities
- API design (if applicable)
- Database schema changes (if needed)
- Testing strategy
- Potential challenges and risks', NULL, 'backlog', 3, 'feature', '[]', 'planning', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('bf48c8dce2b36efc4f700c0229adf963', NULL, 'Refactor Analysis', 'Analyze code for refactoring opportunities', 'Analyze [component/module name] for refactoring opportunities. Look for:
- Code duplication
- Complex functions that should be simplified
- Opportunities for better abstraction
- Performance improvements
- Naming improvements

Suggest specific refactorings with examples.', NULL, 'backlog', 2, '', '[]', 'refactor', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('e27844aeca8202ae89233f0d32a175c4', NULL, 'Security Audit', 'Security review of code or feature', 'Perform a security audit of [feature/component name]. Check for:
- Input validation
- SQL injection vulnerabilities
- XSS vulnerabilities
- Authentication/authorization issues
- Sensitive data exposure
- OWASP Top 10 vulnerabilities

Provide specific findings and remediation steps.', NULL, 'active', 4, '', '[]', 'code_review', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('5f7619591b40956738794685c56085f0', NULL, 'Performance Optimization', 'Analyze and optimize performance', 'Analyze performance of [feature/endpoint/query].

Current metrics:
- [Response time, throughput, etc.]

Identify bottlenecks and suggest optimizations:
- Database query optimization
- Caching strategies
- Algorithm improvements
- Resource usage reduction', NULL, 'active', 3, '', '[]', 'maintenance', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('0618763428fded10060cdb6d8d5f2bac', NULL, 'API Endpoint Design', 'Design a new REST API endpoint', 'Design a REST API endpoint for: [functionality]

Specify:
- HTTP method and path
- Request parameters and body schema
- Response format and status codes
- Error handling
- Authentication/authorization requirements
- Rate limiting considerations
- Example requests and responses', NULL, 'backlog', 3, 'feature', '[]', 'planning', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('cd773db5bac55291f0796e199d277442', NULL, 'Database Migration', 'Plan and execute a database schema change', 'Plan a database migration for: [change description]

Provide:
- Migration SQL (up and down)
- Data migration steps (if needed)
- Rollback strategy
- Impact analysis (breaking changes, downtime)
- Testing approach
- Performance considerations for large tables', NULL, 'active', 3, '', '[]', 'maintenance', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('4fd8f3f682294259102cca87d80d5c92', NULL, 'Write Unit Tests', 'Add comprehensive unit tests for a component', 'Write unit tests for [component/function name].

Coverage should include:
- Happy path scenarios
- Edge cases
- Error handling
- Boundary conditions
- Mock external dependencies

Target: 80%+ code coverage. Follow existing test patterns in the codebase.', NULL, 'active', 2, '', '[]', 'testing', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('381eb67ea6ba0828309d7fa6906bc27c', NULL, 'Documentation Update', 'Update or create documentation', 'Update documentation for [feature/module].

Include:
- Overview and purpose
- API reference (if applicable)
- Usage examples
- Configuration options
- Common troubleshooting
- Architecture diagrams (if complex)

Format: [README, API docs, inline comments, etc.]', NULL, 'backlog', 2, '', '[]', 'documentation', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('59b737dc107b95ec73b9a8532864bb1c', NULL, 'Weekly Report', 'Generate weekly progress report', 'Generate a weekly progress report covering:
- Completed tasks and deliverables
- In-progress work
- Blockers and challenges
- Upcoming priorities
- Key metrics and KPIs
- Highlights and notable achievements

Format as a concise summary for stakeholders.', NULL, 'backlog', 2, '', '[]', 'research', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('57e6b5169d09ffdae9738e4440cf0ce6', NULL, 'Dependency Update', 'Plan and execute dependency updates', 'Plan update for dependencies:

[List dependencies and target versions]

Analyze:
- Breaking changes in changelogs
- Required code modifications
- Testing strategy
- Rollback plan
- Security fixes included

Execute update incrementally if needed.', NULL, 'active', 2, '', '[]', 'maintenance', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('133dd6697aa9c3685b3e805d79d6e624', NULL, 'Error Investigation', 'Investigate production errors from logs', 'Investigate the following production error:

[Error message and stack trace]

Context:
- Frequency: [how often]
- First seen: [timestamp]
- Affected users: [count or description]

Analyze:
- Root cause
- Impact assessment
- Immediate mitigation
- Long-term fix
- Preventive measures', NULL, 'active', 4, 'bug', '[]', 'bug_fix', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('291c6fce8b87edb5b53d95f2ed79b0eb', NULL, 'Architecture Documentation', 'Document system architecture', 'Document the architecture for [system/feature].

Include:
- High-level architecture diagram
- Component descriptions and responsibilities
- Data flow
- Technology stack
- Key design decisions and trade-offs
- Scalability considerations
- Integration points

Format: Architecture decision record (ADR) or similar.', NULL, 'backlog', 2, '', '[]', 'documentation', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO task_templates (id, project_id, name, description, default_prompt, suggested_agent_id, category, priority, tag, tags_json, category_filter, is_built_in, is_favorite, usage_count, created_by, created_at, updated_at) VALUES ('c8d2de36f03bbc17d237a9bbc43a3558', NULL, 'Deployment Checklist', 'Pre-deployment verification and rollout plan', 'Create deployment plan for: [release/feature]

Checklist:
- [ ] All tests passing
- [ ] Database migrations tested
- [ ] Configuration changes documented
- [ ] Rollback procedure defined
- [ ] Monitoring and alerts configured
- [ ] Stakeholders notified
- [ ] Feature flags ready (if applicable)
- [ ] Performance benchmarks verified

Deployment steps:
[List steps in order]', NULL, 'active', 3, '', '[]', 'deployment', 1, 0, 0, 'system', '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO worker_settings (id, max_workers, updated_at) VALUES ('singleton', 1, '2026-04-04 16:31:09');
INSERT INTO workflow_templates (id, name, description, category, definition, is_built_in, created_at, updated_at) VALUES ('0abb6807c7536cc895341516de3c1df0', 'Full Feature', 'Opus plans the architecture, Sonnet implements, Haiku writes tests, Sonnet fixes issues found in testing', 'feature', '{"strategy":"sequential","config":{"max_retries":2,"quality_threshold":0.7,"auto_rollback":true,"adaptive_routing":true},"steps":[{"name":"Plan Architecture","step_type":"execute","step_order":0,"agent_role":"planner","prompt":"Analyze the following task and create a detailed implementation plan. Break down the work into specific subtasks, identify files to modify, and propose the architecture.\n\nTask: {{task_prompt}}","depends_on":[],"config":{}},{"name":"Implement","step_type":"execute","step_order":1,"agent_role":"implementer","prompt":"Implement the following plan. Follow the architecture decisions exactly.\n\nPlan:\n{{prev_output}}\n\nOriginal Task: {{task_prompt}}","depends_on":[0],"config":{}},{"name":"Write Tests","step_type":"execute","step_order":2,"agent_role":"tester","prompt":"Write comprehensive tests for the implementation. Cover edge cases and error scenarios.\n\nImplementation:\n{{prev_output}}\n\nOriginal Task: {{task_prompt}}","depends_on":[1],"config":{}},{"name":"Quality Gate","step_type":"gate","step_order":3,"agent_role":"reviewer","prompt":"Review the implementation and tests. Score the quality from 0 to 1. Check for: correctness, test coverage, code style, security issues.\n\nProvide your score as: QUALITY_SCORE: 0.X","depends_on":[2],"config":{"pass_threshold":0.7,"fail_action":"retry","max_iterations":3}},{"name":"Fix Issues","step_type":"execute","step_order":4,"agent_role":"implementer","prompt":"Fix the issues identified in the review:\n\n{{prev_output}}\n\nOriginal implementation context:\n{{context_summary}}","depends_on":[3],"config":{}}]}', 1, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO workflow_templates (id, name, description, category, definition, is_built_in, created_at, updated_at) VALUES ('9ff9cc5620a7ef5e629703c9a872050f', 'Refactor', 'Opus analyzes impact, Sonnet refactors, Haiku validates behavior is unchanged via tests', 'refactor', '{"strategy":"sequential","config":{"max_retries":1,"quality_threshold":0.8,"auto_rollback":true},"steps":[{"name":"Impact Analysis","step_type":"execute","step_order":0,"agent_role":"planner","prompt":"Analyze the codebase for the refactoring task. Identify all affected files, dependencies, and potential risks.\n\nTask: {{task_prompt}}","depends_on":[],"config":{}},{"name":"Refactor","step_type":"execute","step_order":1,"agent_role":"implementer","prompt":"Perform the refactoring based on the impact analysis. Ensure all changes maintain backward compatibility.\n\nAnalysis:\n{{prev_output}}\n\nTask: {{task_prompt}}","depends_on":[0],"config":{}},{"name":"Validate Behavior","step_type":"gate","step_order":2,"agent_role":"tester","prompt":"Run all existing tests and verify behavior is unchanged. Run: go test ./internal/... -count=1 -timeout 60s\n\nScore 1.0 if all tests pass, 0.0 if any fail.\nProvide your score as: QUALITY_SCORE: 0.X","depends_on":[1],"config":{"pass_threshold":0.9,"fail_action":"rollback","max_iterations":2}}]}', 1, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO workflow_templates (id, name, description, category, definition, is_built_in, created_at, updated_at) VALUES ('c83d642f282dd045d0a1ed95570bdc4c', 'Bug Hunt', 'Haiku reproduces the bug, Sonnet analyzes root cause, Opus proposes fix strategy, Sonnet implements', 'bugfix', '{"strategy":"sequential","config":{"max_retries":2,"quality_threshold":0.8,"auto_rollback":true},"steps":[{"name":"Reproduce Bug","step_type":"execute","step_order":0,"agent_role":"tester","prompt":"Write a failing test that reproduces this bug. Do not fix the bug yet.\n\nBug report: {{task_prompt}}","depends_on":[],"config":{}},{"name":"Root Cause Analysis","step_type":"execute","step_order":1,"agent_role":"implementer","prompt":"Analyze the failing test and codebase to identify the root cause of the bug.\n\nReproduction:\n{{prev_output}}\n\nBug report: {{task_prompt}}","depends_on":[0],"config":{}},{"name":"Fix Strategy","step_type":"execute","step_order":2,"agent_role":"planner","prompt":"Based on the root cause analysis, propose the optimal fix strategy. Consider side effects and risks.\n\nAnalysis:\n{{prev_output}}\n\nBug report: {{task_prompt}}","depends_on":[1],"config":{}},{"name":"Implement Fix","step_type":"execute","step_order":3,"agent_role":"implementer","prompt":"Implement the fix based on the proposed strategy. Ensure the reproduction test passes.\n\nStrategy:\n{{prev_output}}\n\nBug report: {{task_prompt}}","depends_on":[2],"config":{}},{"name":"Verify Fix","step_type":"gate","step_order":4,"agent_role":"tester","prompt":"Run the reproduction test and all existing tests. Verify the bug is fixed and no regressions.\n\nProvide your score as: QUALITY_SCORE: 0.X","depends_on":[3],"config":{"pass_threshold":0.9,"fail_action":"retry","max_iterations":2}}]}', 1, '2026-04-04 16:31:10', '2026-04-04 16:31:10');
INSERT INTO workflow_templates (id, name, description, category, definition, is_built_in, created_at, updated_at) VALUES ('a649704dc9d5c5b70e7df49a3bc64c8e', 'Research & Implement', 'Opus researches best practices, Sonnet implements, Opus reviews architecture quality', 'research', '{"strategy":"sequential","config":{"max_retries":1,"quality_threshold":0.7},"steps":[{"name":"Research","step_type":"execute","step_order":0,"agent_role":"planner","prompt":"Research best practices and approaches for this task. Consider multiple options and recommend the best approach with justification.\n\nTask: {{task_prompt}}","depends_on":[],"config":{}},{"name":"Implement","step_type":"execute","step_order":1,"agent_role":"implementer","prompt":"Implement the recommended approach from the research phase.\n\nResearch:\n{{prev_output}}\n\nTask: {{task_prompt}}","depends_on":[0],"config":{}},{"name":"Architecture Review","step_type":"gate","step_order":2,"agent_role":"planner","prompt":"Review the implementation against the research recommendations. Evaluate architectural quality, maintainability, and adherence to best practices.\n\nProvide your score as: QUALITY_SCORE: 0.X","depends_on":[1],"config":{"pass_threshold":0.7,"fail_action":"retry","max_iterations":2}}]}', 1, '2026-04-04 16:31:10', '2026-04-04 16:31:10');

-- +goose Down

DROP TABLE IF EXISTS slack_authorized_users;
DROP TABLE IF EXISTS slack_channels;
DROP TABLE IF EXISTS task_pull_requests;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS telegram_user_projects;
DROP TABLE IF EXISTS telegram_authorized_users;
DROP TABLE IF EXISTS app_settings;
DROP TABLE IF EXISTS health_checks;
DROP TABLE IF EXISTS trend_data;
DROP TABLE IF EXISTS trend_analysis;
DROP TABLE IF EXISTS trend_cycles;
DROP TABLE IF EXISTS idea_grades;
DROP TABLE IF EXISTS review_comments;
DROP TABLE IF EXISTS personalities;
DROP TABLE IF EXISTS collision_events;
DROP TABLE IF EXISTS collision_rules;
DROP TABLE IF EXISTS proactive_insights;
DROP TABLE IF EXISTS prompt_patterns;
DROP TABLE IF EXISTS task_templates;
DROP TABLE IF EXISTS workflow_templates;
DROP TABLE IF EXISTS visionary_templates;
DROP TABLE IF EXISTS chat_attachments;
DROP TABLE IF EXISTS task_attachments;
DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS schedules;
DROP TABLE IF EXISTS executions;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS worker_settings;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS agent_configs;
