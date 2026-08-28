package repository

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
)

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
	restore, err := boundSQLiteBusyTimeoutToContext(ctx, conn)
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
