package database

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
)

func TestMigration214MovesTaskThreadAttachmentsPreservingIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "migration-214.db"))
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 213); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO projects (id, name, description, repo_path) VALUES ('p214', 'Migration 214', '', '');
		INSERT INTO tasks (id, project_id, title, category, priority, status, prompt) VALUES
			('chat-task-214', 'p214', 'Chat', 'chat', 0, 'completed', 'p'),
			('thread-task-214', 'p214', 'Thread', 'active', 0, 'completed', 'p');
		INSERT INTO executions (id, task_id, task_project_id, status, started_at) VALUES
			('chat-exec-214', 'chat-task-214', 'p214', 'completed', datetime('now')),
			('thread-exec-214', 'thread-task-214', 'p214', 'completed', datetime('now'));
		INSERT INTO task_attachments (id, task_id, file_name, file_path, media_type, file_size, created_at) VALUES
			('panel-214', 'thread-task-214', 'spec.pdf', 'uploads/thread-task-214/spec.pdf', 'application/pdf', 3, '2026-01-01 00:00:00');
		INSERT INTO chat_attachments (id, execution_id, file_name, file_path, media_type, file_size, created_at) VALUES
			('chat-att-214', 'chat-exec-214', 'chat.png', 'uploads/chat/chat-exec-214/chat.png', 'image/png', 1, '2026-01-02 00:00:00'),
			('thread-att-214', 'thread-exec-214', 'shot.png', 'uploads/chat/thread-exec-214/shot.png', 'image/png', 2, '2026-01-03 00:00:00');
	`); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, ".", 214); err != nil {
		t.Fatal(err)
	}

	var taskID, execID, path, created string
	if err := db.QueryRow(`SELECT task_id, execution_id, file_path, created_at FROM task_attachments WHERE id = 'thread-att-214'`).Scan(&taskID, &execID, &path, &created); err != nil {
		t.Fatalf("task-thread attachment not migrated: %v", err)
	}
	if taskID != "thread-task-214" || execID != "thread-exec-214" || path != "uploads/chat/thread-exec-214/shot.png" || !strings.HasPrefix(strings.Replace(created, "T", " ", 1), "2026-01-03 00:00:00") {
		t.Fatalf("migrated row = %s %s %s %s", taskID, execID, path, created)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_attachments WHERE id = 'thread-att-214'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("task-thread attachment left in chat_attachments: n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_attachments WHERE id = 'chat-att-214'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("chat-page attachment moved: n=%d err=%v", n, err)
	}
	var panelExec *string
	if err := db.QueryRow(`SELECT execution_id FROM task_attachments WHERE id = 'panel-214'`).Scan(&panelExec); err != nil || panelExec != nil {
		t.Fatalf("panel attachment execution_id = %v err=%v", panelExec, err)
	}

	if err := goose.DownTo(db, ".", 213); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT execution_id FROM chat_attachments WHERE id = 'thread-att-214'`).Scan(&execID); err != nil || execID != "thread-exec-214" {
		t.Fatalf("down did not restore chat attachment: %s %v", execID, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_attachments`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("down task_attachments count = %d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('task_attachments') WHERE name = 'execution_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("down kept execution_id column: %d %v", n, err)
	}
}
