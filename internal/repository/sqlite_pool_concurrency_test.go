package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
)

func TestDedicatedWriterDoesNotWaitForReaderPool(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "reader-saturation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	unregister := RegisterDedicatedWriter(connections.Reader, connections.Writer)
	defer unregister()

	ctx := context.Background()
	if _, err := connections.Writer.ExecContext(ctx, `
		INSERT INTO projects(id, name) VALUES ('reader-project', 'Reader project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('reader-task', 'reader-project', 'Reader task', 'active', 'pending');
	`); err != nil {
		t.Fatal(err)
	}

	readers := make([]*sql.Conn, 0, connections.Reader.Stats().MaxOpenConnections)
	for i := 0; i < connections.Reader.Stats().MaxOpenConnections; i++ {
		conn, err := connections.Reader.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, conn)
		if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, conn := range readers {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			_ = conn.Close()
		}
	}()

	writeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	claimed, err := NewTaskRepo(connections.Reader, nil).ClaimTask(writeCtx, "reader-task")
	if err != nil {
		t.Fatalf("write while reader pool saturated: %v", err)
	}
	if !claimed {
		t.Fatal("write while reader pool saturated was not applied")
	}
	if got := connections.Reader.Stats().WaitCount; got != 0 {
		t.Fatalf("writer waited on reader pool %d times", got)
	}
}

func TestDedicatedWriterDoesNotConsumeReaderCapacity(t *testing.T) {
	connections, err := database.NewReadWrite(filepath.Join(t.TempDir(), "dedicated.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connections.Close()
	unregister := RegisterDedicatedWriter(connections.Reader, connections.Writer)
	defer unregister()

	ctx := context.Background()
	if _, err := connections.Writer.ExecContext(ctx, `
		INSERT INTO projects(id, name) VALUES ('dedicated-project', 'Dedicated project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('dedicated-task', 'dedicated-project', 'Dedicated task', 'active', 'pending');
	`); err != nil {
		t.Fatal(err)
	}

	locker, err := connections.Writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer locker.ExecContext(context.Background(), `ROLLBACK`)

	writeCtx, cancelWrite := context.WithTimeout(ctx, time.Second)
	defer cancelWrite()
	writeResult := make(chan error, 1)
	go func() {
		_, err := NewTaskRepo(connections.Reader, nil).ClaimTask(writeCtx, "dedicated-task")
		writeResult <- err
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && connections.Writer.Stats().WaitCount == 0 {
		time.Sleep(time.Millisecond)
	}
	if connections.Writer.Stats().WaitCount == 0 {
		t.Fatal("queued writer did not wait on the dedicated writer pool")
	}
	if got := connections.Reader.Stats().InUse; got != 0 {
		t.Fatalf("reader connections in use by queued writer = %d, want 0", got)
	}

	readCtx, cancelRead := context.WithTimeout(ctx, time.Second)
	defer cancelRead()
	task, err := NewTaskRepo(connections.Reader, nil).GetByID(readCtx, "dedicated-task")
	if err != nil {
		t.Fatalf("read while writer queued: %v", err)
	}
	if task == nil || task.ID != "dedicated-task" {
		t.Fatalf("read task = %#v", task)
	}

	if _, err := locker.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued writer did not complete after writer release")
	}
}

func TestLLMConfigRepoConcurrentFirstCreatesChooseOneDefault(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewLLMConfigRepo(db)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"Concurrent Alpha", "Concurrent Beta"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- repo.Create(context.Background(), &models.LLMConfig{
				Name: name, Provider: models.ProviderTest, Model: "test-model",
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var total, defaults int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_default), 0) FROM agent_configs`).Scan(&total, &defaults); err != nil {
		t.Fatal(err)
	}
	if total != 2 || defaults != 1 {
		t.Fatalf("model configs: total=%d defaults=%d, want total=2 defaults=1", total, defaults)
	}
}

func TestBoundSQLiteBusyTimeoutToContextRestoresSameConnection(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "busy-timeout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	locker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer locker.ExecContext(context.Background(), `ROLLBACK`)

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	restore, err := database.BindSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, lockErr := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	restore()
	if lockErr == nil {
		t.Fatal("BEGIN IMMEDIATE unexpectedly acquired a held writer lock")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context-bounded lock wait took %s", elapsed)
	}
	var timeout int
	if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 5000 {
		t.Fatalf("restored busy_timeout = %d, want 5000", timeout)
	}
}

func TestImmediateRepositoryOperationsHonorContextDeadline(t *testing.T) {
	tests := lockedWriterRepositoryOperations()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.New(filepath.Join(t.TempDir(), "deadline.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			locker, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Close()
			if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			defer locker.ExecContext(context.Background(), `ROLLBACK`)

			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := tt.run(ctx, db); err == nil {
				t.Fatal("operation unexpectedly acquired held writer lock")
			}
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("context-bounded operation took %s", elapsed)
			}
		})
	}
}

func lockedWriterRepositoryOperations() []struct {
	name string
	run  func(context.Context, *sql.DB) error
} {
	return []struct {
		name string
		run  func(context.Context, *sql.DB) error
	}{
		{
			name: "task dispatch claim",
			run: func(ctx context.Context, db *sql.DB) error {
				_, _, err := NewTaskRepo(db, nil).ClaimTaskForDispatch(ctx, "locked-task")
				return err
			},
		},
		{
			name: "automation resume",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewAutomationRepo(db).ResumeAutomation(ctx, "locked-project", "locked-automation")
				return err
			},
		},
		{
			name: "automation dispatch lease",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewAutomationRepo(db).LeaseNextDispatch(ctx, "locked-worker", time.Now().UTC(), time.Minute)
				return err
			},
		},
		{
			name: "automation mark queued",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewAutomationRepo(db).MarkDispatchQueued(ctx, "locked-dispatch", "locked-worker")
			},
		},
		{
			name: "automation mark submitted",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewAutomationRepo(db).MarkDispatchSubmitted(ctx, "locked-dispatch", "locked-worker", "locked-execution")
			},
		},
		{
			name: "execution output flush",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewExecutionRepo(db).UpdateOutput(ctx, "locked-execution", "output")
			},
		},
		{
			name: "execution completion",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewExecutionRepo(db).Complete(ctx, "locked-execution", models.ExecCompleted, "output", "", 0, 0)
			},
		},
		{
			name: "execution replay replacement transaction",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewExecutionRepo(db).ReplaceReasoningReplay(ctx, "locked-execution", "reasoning", nil)
			},
		},
		{
			name: "thread input binding",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewThreadInputRepo(db).BindPreExecutionQueuedTaskInputs(ctx, "locked-task", "locked-execution")
			},
		},
		{
			name: "thread input applied transition",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewThreadInputRepo(db).MarkApplied(ctx, "locked-input", "locked-execution", "locked-turn")
			},
		},
		{
			name: "thread input cancellation returning write",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewThreadInputRepo(db).CancelPending(ctx, "locked-input")
				return err
			},
		},
		{
			name: "task goal create returning write",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewTaskGoalRepo(db).CreateOrReplace(ctx, &models.TaskGoal{
					TaskID: "locked-task", GoalID: "locked-goal", Objective: "locked",
				})
			},
		},
		{
			name: "task goal update returning write",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewTaskGoalRepo(db).UpdateStatus(ctx, "locked-task", "locked-goal", models.TaskGoalStatusPaused, "locked", false)
				return err
			},
		},
		{
			name: "alert mark read",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewAlertRepo(db).MarkRead(ctx, "locked-project", "locked-alert")
			},
		},
		{
			name: "alert delete",
			run: func(ctx context.Context, db *sql.DB) error {
				return NewAlertRepo(db).Delete(ctx, "locked-project", "locked-alert")
			},
		},
		{
			name: "automation reconcile completions",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewAutomationRepo(db).ReconcileInvocationCompletions(ctx, 10)
				return err
			},
		},
		{
			name: "automation prune terminal positions",
			run: func(ctx context.Context, db *sql.DB) error {
				_, err := NewAutomationRepo(db).PruneTerminalizedAutomationPositions(ctx, 10)
				return err
			},
		},
	}
}
func TestImmediateRepositoryOperationsHonorEarlyCancellationWithDeadline(t *testing.T) {
	for _, tt := range lockedWriterRepositoryOperations() {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.New(filepath.Join(t.TempDir(), "early-cancellation.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			locker, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
				_ = locker.Close()
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			result := make(chan error, 1)
			go func() { result <- tt.run(ctx, db) }()
			waitForDBInUse(t, db, 2)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation with a live deadline did not stop promptly after early cancellation")
			}
			if _, err := locker.ExecContext(context.Background(), `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			if err := locker.Close(); err != nil {
				t.Fatal(err)
			}
			assertEveryPooledConnectionBusyTimeout(t, db, 5000)
		})
	}
}

func TestImmediateRepositoryOperationsHonorCancellationWithoutDeadline(t *testing.T) {
	for _, tt := range lockedWriterRepositoryOperations() {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.New(filepath.Join(t.TempDir(), "cancellation.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			locker, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer locker.Close()
			if _, err := locker.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			defer locker.ExecContext(context.Background(), `ROLLBACK`)

			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- tt.run(ctx, db) }()
			waitForDBInUse(t, db, 2)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not stop promptly after cancellation")
			}
			if _, err := locker.ExecContext(context.Background(), `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
			if err := locker.Close(); err != nil {
				t.Fatal(err)
			}
			assertEveryPooledConnectionBusyTimeout(t, db, 5000)
		})
	}
}

func waitForDBInUse(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if db.Stats().InUse >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("database in-use connections = %d, want at least %d", db.Stats().InUse, want)
}

func assertEveryPooledConnectionBusyTimeout(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	connections := make([]*sql.Conn, 0, db.Stats().MaxOpenConnections)
	for i := 0; i < db.Stats().MaxOpenConnections; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for i, conn := range connections {
		var got int
		if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("connection %d busy_timeout = %d, want %d", i, got, want)
		}
	}
}

func TestSuccessfulAutomationOperationsRestoreBusyTimeoutBeforePoolRelease(t *testing.T) {
	t.Run("audited returning writes", func(t *testing.T) {
		db, err := database.New(filepath.Join(t.TempDir(), "audited-returning-restore.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`
			INSERT INTO projects(id, name) VALUES ('returning-project', 'Returning project');
			INSERT INTO tasks(id, project_id, title, category, status) VALUES ('returning-task', 'returning-project', 'Returning task', 'active', 'pending');
			INSERT INTO thread_inputs(id, scope, project_id, task_id, input_mode, input_status, content, queue_position)
			VALUES ('returning-input', 'task_thread', 'returning-project', 'returning-task', 'queued', 'pending', 'cancel me', 1);
		`); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		goal := &models.TaskGoal{TaskID: "returning-task", GoalID: "returning-goal", Objective: "finish"}
		goalRepo := NewTaskGoalRepo(db)
		if err := goalRepo.CreateOrReplace(ctx, goal); err != nil {
			t.Fatal(err)
		}
		if goal.GoalID != "returning-goal" || goal.Status != models.TaskGoalStatusActive {
			t.Fatalf("created goal = %#v", goal)
		}
		updatedGoal, err := goalRepo.UpdateStatus(ctx, goal.TaskID, goal.GoalID, models.TaskGoalStatusPaused, "paused", false)
		if err != nil {
			t.Fatal(err)
		}
		if updatedGoal == nil || updatedGoal.Status != models.TaskGoalStatusPaused || updatedGoal.Reason != "paused" {
			t.Fatalf("updated goal = %#v", updatedGoal)
		}
		input, err := NewThreadInputRepo(db).CancelPending(ctx, "returning-input")
		if err != nil {
			t.Fatal(err)
		}
		if input == nil || input.InputStatus != models.ThreadInputCancelled {
			t.Fatalf("cancelled input = %#v", input)
		}
		assertEveryPooledConnectionBusyTimeout(t, db, 5000)
	})

	t.Run("direct exec and returning writes", func(t *testing.T) {
		db, err := database.New(filepath.Join(t.TempDir(), "direct-write-restore.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := NewAlertRepo(db).MarkAllRead(ctx, "missing-project"); err != nil {
			t.Fatal(err)
		}
		project := &models.Project{Name: "Bounded returning write"}
		if err := NewProjectRepo(db).Create(ctx, project); err != nil {
			t.Fatal(err)
		}
		assertEveryPooledConnectionBusyTimeout(t, db, 5000)
	})

	t.Run("resume", func(t *testing.T) {
		db, err := database.New(filepath.Join(t.TempDir(), "resume-restore.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
		if _, err := db.Exec(`UPDATE automations SET lifecycle_state='paused' WHERE id=?`, fixture.AutomationID); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := NewAutomationRepo(db).ResumeAutomation(ctx, fixture.ProjectID, fixture.AutomationID); err != nil {
			t.Fatal(err)
		}
		assertEveryPooledConnectionBusyTimeout(t, db, 5000)
	})

	t.Run("dispatch claim", func(t *testing.T) {
		db, err := database.New(filepath.Join(t.TempDir(), "claim-restore.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx := context.Background()
		fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
		repo := NewAutomationRepo(db)
		taskRepo := NewTaskRepo(db, nil)
		task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Restore timeout claim")
		due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], due)
		if _, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), nil); err != nil || dispatch == nil {
			t.Fatalf("seed dispatch = %#v, %v", dispatch, err)
		}
		leased, err := repo.LeaseNextDispatch(ctx, "restore-worker", time.Now().UTC(), time.Minute)
		if err != nil || leased == nil {
			t.Fatalf("lease = %#v, %v", leased, err)
		}
		claimCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := taskRepo.ClaimAutomationDispatch(claimCtx, leased.ID, "restore-worker"); err != nil {
			t.Fatal(err)
		}
		assertEveryPooledConnectionBusyTimeout(t, db, 5000)
	})
}

func TestExecutionRepoPeriodicOutputCannotOverwriteTerminalOutput(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "stream-output.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO projects(id, name) VALUES ('stream-project', 'Stream project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('stream-task', 'stream-project', 'Stream task', 'active', 'running');
		INSERT INTO executions(id, task_id, status, output) VALUES ('stream-execution', 'stream-task', 'running', 'partial');
		UPDATE executions SET status='completed', output='final' WHERE id='stream-execution';
	`); err != nil {
		t.Fatal(err)
	}
	if err := NewExecutionRepo(db).UpdateOutput(context.Background(), "stream-execution", "stale periodic output"); err != nil {
		t.Fatal(err)
	}
	var output string
	if err := db.QueryRow(`SELECT output FROM executions WHERE id='stream-execution'`).Scan(&output); err != nil {
		t.Fatal(err)
	}
	if output != "final" {
		t.Fatalf("terminal output = %q, want final", output)
	}
}

func TestAutomationRepoConcurrentLeaseAdmitsExactlyOnceWithFilePool(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "automation-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	task := createRuntimeScheduledTask(t, ctx, NewTaskRepo(db, nil), fixture.ProjectID, "Concurrent Automation claim")
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], due)
	if _, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), nil); err != nil || dispatch == nil {
		t.Fatalf("seed dispatch = %#v, %v", dispatch, err)
	}

	start := make(chan struct{})
	claims := make(chan *models.AutomationDispatch, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			claim, err := repo.LeaseNextDispatch(ctx, fmt.Sprintf("worker-%d", i), time.Now().UTC(), time.Minute)
			claims <- claim
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(claims)
	close(errs)
	count := 0
	for claim := range claims {
		if claim != nil {
			count++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("Automation dispatch leases = %d, want 1", count)
	}
}

func TestThreadInputSteeringPreparationAndPromotionAreAtomicWithFilePool(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "thread-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`
		INSERT INTO agent_configs(id, name, provider, model, is_default) VALUES ('thread-model', 'Thread model', 'test', 'test-model', 1);
		INSERT INTO projects(id, name) VALUES ('thread-project', 'Thread project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('thread-task', 'thread-project', 'Thread task', 'active', 'running');
		INSERT INTO executions(id, task_id, status) VALUES ('active-execution', 'thread-task', 'running');
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewThreadInputRepo(db)
	steering := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "thread-project", TaskID: "thread-task", ExpectedTurnID: "active-execution", Content: "steer"}
	if err := repo.CreateSteeringForActiveExecution(ctx, steering, "active-execution"); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	prepared := make(chan int, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rows, err := repo.PreparePendingSteering(ctx, "active-execution", "active-execution")
			prepared <- len(rows)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(prepared)
	close(errs)
	totalPrepared := 0
	for count := range prepared {
		totalPrepared += count
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if totalPrepared != 1 {
		t.Fatalf("prepared steering rows = %d, want 1", totalPrepared)
	}

	if _, err := db.Exec(`UPDATE executions SET status='completed' WHERE id='active-execution'; UPDATE tasks SET status='pending' WHERE id='thread-task'`); err != nil {
		t.Fatal(err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "thread-project", TaskID: "thread-task", Content: "queued"}
	if err := repo.CreateQueued(ctx, queued); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	promotionErrs := make(chan error, 2)
	var agentConfigID string
	if err := db.QueryRow(`SELECT id FROM agent_configs ORDER BY created_at, id LIMIT 1`).Scan(&agentConfigID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			promotionErrs <- repo.ClaimQueuedForTaskExecution(ctx, queued.ID, &models.Execution{TaskID: "thread-task", AgentConfigID: agentConfigID, PromptSent: "queued", IsFollowup: true})
		}()
	}
	close(start)
	wg.Wait()
	close(promotionErrs)
	successes := 0
	for err := range promotionErrs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInputNotPending) && !errors.Is(err, ErrActiveTurnChanged) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("queued promotions = %d, want 1", successes)
	}
}

func TestTaskRepoConcurrentClaimAdmitsExactlyOnceWithFilePool(t *testing.T) {
	db, err := database.New(filepath.Join(t.TempDir(), "task-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO projects(id, name) VALUES ('claim-project', 'Claim project');
		INSERT INTO tasks(id, project_id, title, category, status) VALUES ('claim-task', 'claim-project', 'Claim task', 'active', 'pending');
	`); err != nil {
		t.Fatal(err)
	}
	repo := NewTaskRepo(db, nil)
	start := make(chan struct{})
	results := make(chan bool, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := repo.ClaimTask(context.Background(), "claim-task")
			results <- claimed
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if claims != 1 {
		t.Fatalf("successful claims = %d, want 1", claims)
	}
}
