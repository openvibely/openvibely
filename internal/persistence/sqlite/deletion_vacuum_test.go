package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite/database"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestDeletionRequestsVacuumOnlyAfterCommit(t *testing.T) {
	for _, mode := range []string{"single", "manifest", "completed", "backlog", "chat", "blocked-children"} {
		t.Run(mode, func(t *testing.T) {
			// Production's split reader/writer mapping must deliver the trigger to the
			// writer worker. Observer sees actual cleanup SQL, not a mocked notifier.
			reader, writer, counter := testutil.NewFileBackedSplitStatementCountingTestDB(t)
			unregister := RegisterDedicatedWriter(reader, writer)
			defer unregister()
			ctx, cancel := context.WithCancel(context.Background())
			checks := make(chan struct{}, 100)
			counter.SetObserver(func(_ context.Context, q string) {
				if strings.EqualFold(strings.TrimSpace(q), "PRAGMA freelist_count") {
					checks <- struct{}{}
				}
			})
			counter.SetEnabled(true)
			done := database.StartIncrementalVacuum(ctx, writer)
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("worker shutdown stalled")
				}
			}()
			repo := NewTaskRepo(reader, nil)
			category := models.CategoryCompleted
			if mode == "backlog" {
				category = models.CategoryBacklog
			}
			if mode == "chat" {
				category = models.CategoryChat
			}
			task := &models.Task{ProjectID: "default", Title: "delete trigger", Category: category, Status: models.StatusBlocked}
			if err := repo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}
			if mode == "blocked-children" {
				parent := &models.Task{ProjectID: "default", Title: "parent", Category: models.CategoryActive, Status: models.StatusPending}
				if err := repo.Create(ctx, parent); err != nil {
					t.Fatal(err)
				}
				if _, err := execBoundSQLite(ctx, reader, `UPDATE tasks SET parent_task_id=? WHERE id=?`, parent.ID, task.ID); err != nil {
					t.Fatal(err)
				}
				task.ParentTaskID = &parent.ID
			}
			// Force a statement failure (and hence rollback) after DELETE begins. It
			// must not queue a cleanup check for either autocommit or explicit paths.
			if _, err := execBoundSQLite(ctx, reader, `CREATE TRIGGER fail_task_delete BEFORE DELETE ON tasks BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`); err != nil {
				t.Fatal(err)
			}
			remove := func() error {
				switch mode {
				case "manifest":
					_, _, err := repo.DeleteWithCleanupManifest(ctx, task.ID, nil)
					return err
				case "completed":
					_, err := repo.DeleteAllCompleted(ctx, "default")
					return err
				case "backlog":
					_, err := repo.DeleteAllBacklog(ctx, "default")
					return err
				case "chat":
					_, err := repo.DeleteAllChat(ctx, "default")
					return err
				case "blocked-children":
					return repo.DeleteBlockedChildrenByParent(ctx, *task.ParentTaskID)
				default:
					return repo.Delete(ctx, task.ID)
				}
			}
			if err := remove(); err == nil {
				t.Fatal("expected rolled-back deletion")
			}
			select {
			case <-checks:
				t.Fatal("rollback triggered vacuum")
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := execBoundSQLite(ctx, reader, `DROP TRIGGER fail_task_delete`); err != nil {
				t.Fatal(err)
			}
			if mode == "manifest" {
				injected := errors.New("before commit")
				if _, _, err := repo.DeleteWithCleanupManifest(ctx, task.ID, func(persistence.TaskDeletionManifest) error { return injected }); !errors.Is(err, injected) {
					t.Fatal(err)
				}
				select {
				case <-checks:
					t.Fatal("callback rollback triggered vacuum")
				case <-time.After(30 * time.Millisecond):
				}
			}
			// A deferred FK fails at COMMIT rather than at DELETE. This exercises
			// the explicit manifest transaction's commit-error/rollback path too.
			for _, query := range []string{
				`CREATE TABLE delete_guard_parent(id INTEGER PRIMARY KEY)`,
				`CREATE TABLE delete_guard(id INTEGER REFERENCES delete_guard_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
				`CREATE TRIGGER fail_task_commit AFTER DELETE ON tasks BEGIN INSERT INTO delete_guard VALUES (1); END`,
			} {
				if _, err := execBoundSQLite(ctx, reader, query); err != nil {
					t.Fatal(err)
				}
			}
			if err := remove(); err == nil {
				t.Fatal("expected failed commit")
			}
			select {
			case <-checks:
				t.Fatal("failed commit triggered cleanup")
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := execBoundSQLite(ctx, reader, `DROP TRIGGER fail_task_commit`); err != nil {
				t.Fatal(err)
			}

			// Bulk methods remove multiple rows, but issue only one post-commit hint.
			if mode == "completed" || mode == "backlog" || mode == "chat" {
				other := &models.Task{ProjectID: "default", Title: "second delete", Category: category, Status: models.StatusCompleted}
				if err := repo.Create(ctx, other); err != nil {
					t.Fatal(err)
				}
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-checks:
			case <-time.After(5 * time.Second):
				t.Fatal("committed deletion did not wake cleanup")
			}
			var count int
			if err := reader.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id=?`, task.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("deletion not committed: count=%d err=%v", count, err)
			}
			// These small deletions leave no free pages, so the initial check
			// finishes the run. Do not assume the former size thresholds apply.
			var free int
			if err := reader.QueryRow(`SELECT freelist_count FROM pragma_freelist_count`).Scan(&free); err != nil || free != 0 {
				t.Fatalf("expected empty freelist for no-op trigger assertion: free=%d err=%v", free, err)
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-checks:
				t.Fatal("empty deletion triggered cleanup")
			case <-time.After(30 * time.Millisecond):
			}
		})
	}
}
