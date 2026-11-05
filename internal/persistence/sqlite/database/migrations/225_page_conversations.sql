-- +goose Up
CREATE TABLE page_conversations (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 instance_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
 PRIMARY KEY(project_id, kind, instance_id, page_id)
);
CREATE TABLE page_turn_contexts (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 snapshot_json TEXT NOT NULL
);
INSERT INTO page_conversations(project_id, kind, instance_id, page_id, task_id)
 SELECT i.project_id, 'plugin', i.id, i.page_id, c.task_id
 FROM page_plugin_conversations c JOIN page_plugin_instances i ON i.id = c.instance_id;
INSERT INTO page_turn_contexts SELECT execution_id, snapshot_json FROM page_plugin_turn_contexts;
DROP TABLE page_plugin_turn_contexts;
DROP TABLE page_plugin_conversations;

-- +goose Down
CREATE TABLE page_plugin_conversations (
 instance_id TEXT PRIMARY KEY REFERENCES page_plugin_instances(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
 created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE page_plugin_turn_contexts (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL REFERENCES page_plugin_instances(id) ON DELETE CASCADE,
 snapshot_json TEXT NOT NULL,
 created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
INSERT INTO page_plugin_conversations(instance_id, task_id)
 SELECT c.instance_id, c.task_id FROM page_conversations c
 JOIN page_plugin_instances i ON i.id = c.instance_id WHERE c.kind = 'plugin';
INSERT INTO page_plugin_turn_contexts(execution_id, instance_id, snapshot_json)
 SELECT s.execution_id, c.instance_id, s.snapshot_json FROM page_turn_contexts s
 JOIN executions e ON e.id = s.execution_id
 JOIN page_plugin_conversations c ON c.task_id = e.task_id;
DROP TABLE page_turn_contexts;
DROP TABLE page_conversations;
