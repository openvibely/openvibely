package repository

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func getDefaultProjectID(t *testing.T, db interface {
	QueryRow(string, ...any) interface{ Scan(...any) error }
}) string {
	t.Helper()
	// The migration seeds a default project
	return "default"
}

func TestTaskRepo_GetThreadRenderMetadataUsesCompactProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Large Thread Metadata Task",
		Category:  models.CategoryActive,
		Priority:  3,
		Prompt:    strings.Repeat("prompt-payload", 4096),
		Status:    models.StatusRunning,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	largeChainConfig := strings.Repeat("chain-payload", 4096)
	largeSwarmConfig := strings.Repeat("swarm-payload", 4096)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET chain_config = ?, swarm_config = ? WHERE id = ?`, largeChainConfig, largeSwarmConfig, task.ID); err != nil {
		t.Fatalf("seed large task configs: %v", err)
	}

	got, err := repo.GetThreadRenderMetadata(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetThreadRenderMetadata: %v", err)
	}
	if got == nil {
		t.Fatal("expected task metadata, got nil")
	}
	if got.ID != task.ID || got.ProjectID != task.ProjectID || got.Category != task.Category || got.Status != task.Status {
		t.Fatalf("unexpected compact metadata: %+v", got)
	}
	if got.Title != "" || got.Priority != 0 || got.Prompt != "" || got.ChainConfig != "" || got.SwarmConfig != "" {
		t.Fatalf("compact metadata carried omitted full-detail fields: title=%q priority=%d prompt=%d chain=%d swarm=%d",
			got.Title, got.Priority, len(got.Prompt), len(got.ChainConfig), len(got.SwarmConfig))
	}

	full, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(full.Prompt) != len(task.Prompt) || full.ChainConfig != largeChainConfig || full.SwarmConfig != largeSwarmConfig {
		t.Fatalf("full task hydration did not preserve payloads: prompt=%d chain=%d swarm=%d", len(full.Prompt), len(full.ChainConfig), len(full.SwarmConfig))
	}
}

func TestTaskRepo_GetThreadRenderMetadataQueryPlanUsesTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT `+taskThreadRenderMetadataColumns+` FROM tasks WHERE id = ?`, "task-id")
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "SEARCH tasks") || !strings.Contains(plan, "id=?") {
		t.Fatalf("thread metadata query plan = %s, want indexed tasks.id lookup", plan)
	}
}

func BenchmarkTaskRepo_GetThreadRenderMetadataProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "Thread Metadata Benchmark", Category: models.CategoryActive, Priority: 2, Prompt: strings.Repeat("prompt", 128*1024), Status: models.StatusRunning}
	if err := repo.Create(ctx, task); err != nil {
		b.Fatalf("Create: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET chain_config = ?, swarm_config = ? WHERE id = ?`, strings.Repeat("chain", 128*1024), strings.Repeat("swarm", 128*1024), task.ID); err != nil {
		b.Fatalf("seed large configs: %v", err)
	}
	b.ReportMetric(0, "task_payload_bytes_scanned/op")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetThreadRenderMetadata(ctx, task.ID); err != nil {
			b.Fatalf("projection: %v", err)
		}
	}
}

func TestTaskRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		//Description: "A test task",
		Category: models.CategoryActive,
		Priority: 1,
		Prompt:   "Do something",
		Status:   models.StatusPending,
	}

	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if task.ID == "" {
		t.Fatal("expected ID to be set")
	}

	got, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected task, got nil")
	}
	if got.Title != "Test Task" {
		t.Errorf("expected Title=Test Task, got %q", got.Title)
	}
	if got.Category != models.CategoryActive {
		t.Errorf("expected Category=active, got %q", got.Category)
	}
	if got.Status != models.StatusPending {
		t.Errorf("expected Status=pending, got %q", got.Status)
	}
}

func TestTaskRepo_ListByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// List all
	all, err := repo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("ListByProject all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(all))
	}

	// List by category
	active, err := repo.ListByProject(ctx, "default", "active")
	if err != nil {
		t.Fatalf("ListByProject active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active tasks, got %d", len(active))
	}

	backlog, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(backlog) != 1 {
		t.Errorf("expected 1 backlog task, got %d", len(backlog))
	}
}

func TestTaskRepo_ListByProject_SetsHasGoalForActiveGoal(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	goalRepo := NewTaskGoalRepo(db)
	ctx := context.Background()

	withGoal := &models.Task{ProjectID: "default", Title: "With Goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	withoutGoal := &models.Task{ProjectID: "default", Title: "Without Goal", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	if err := repo.Create(ctx, withGoal); err != nil {
		t.Fatalf("create with goal task: %v", err)
	}
	if err := repo.Create(ctx, withoutGoal); err != nil {
		t.Fatalf("create without goal task: %v", err)
	}
	if err := goalRepo.CreateOrReplace(ctx, &models.TaskGoal{TaskID: withGoal.ID, GoalID: "goal-1", Objective: "finish", Status: models.TaskGoalStatusActive}); err != nil {
		t.Fatalf("create goal: %v", err)
	}

	tasks, err := repo.ListByProject(ctx, "default", "backlog")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	var seenWithGoal, seenWithoutGoal bool
	for _, task := range tasks {
		switch task.ID {
		case withGoal.ID:
			seenWithGoal = task.HasGoal
		case withoutGoal.ID:
			seenWithoutGoal = task.HasGoal
		}
	}
	if !seenWithGoal {
		t.Fatalf("expected task with active goal to have HasGoal=true")
	}
	if seenWithoutGoal {
		t.Fatalf("expected task without goal to have HasGoal=false")
	}

	if err := goalRepo.Clear(ctx, withGoal.ID, "done"); err != nil {
		t.Fatalf("clear goal: %v", err)
	}
	tasks, err = repo.ListByProject(ctx, "default", "backlog")
	if err != nil {
		t.Fatalf("ListByProject after clear: %v", err)
	}
	for _, task := range tasks {
		if task.ID == withGoal.ID && task.HasGoal {
			t.Fatalf("expected cleared goal to hide HasGoal badge")
		}
	}
}

func TestTaskRepo_ListBoardByProject_ProjectsGoalMetState(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	goalRepo := NewTaskGoalRepo(db)
	ctx := context.Background()

	activeGoal := &models.Task{ProjectID: "default", Title: "Active Goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	metGoal := &models.Task{ProjectID: "default", Title: "Met Goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	withoutGoal := &models.Task{ProjectID: "default", Title: "Without Goal", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	for _, task := range []*models.Task{activeGoal, metGoal, withoutGoal} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", task.Title, err)
		}
	}
	if err := goalRepo.CreateOrReplace(ctx, &models.TaskGoal{TaskID: activeGoal.ID, GoalID: "goal-active", Objective: "finish", Status: models.TaskGoalStatusActive}); err != nil {
		t.Fatalf("create active goal: %v", err)
	}
	if err := goalRepo.CreateOrReplace(ctx, &models.TaskGoal{TaskID: metGoal.ID, GoalID: "goal-met", Objective: "finish", Status: models.TaskGoalStatusAchieved}); err != nil {
		t.Fatalf("create met goal: %v", err)
	}

	tasks, err := repo.ListBoardByProjectWithCategorySorts(ctx, "default", "completed", "", "")
	if err != nil {
		t.Fatalf("ListBoardByProjectWithCategorySorts: %v", err)
	}
	byID := make(map[string]models.Task, len(tasks))
	for _, task := range tasks {
		byID[task.ID] = task
	}
	if got := byID[activeGoal.ID]; !got.HasGoal || got.GoalMet {
		t.Fatalf("active goal projection = HasGoal %t GoalMet %t, want true/false", got.HasGoal, got.GoalMet)
	}
	if got := byID[metGoal.ID]; !got.HasGoal || !got.GoalMet {
		t.Fatalf("met goal projection = HasGoal %t GoalMet %t, want true/true", got.HasGoal, got.GoalMet)
	}
	if got := byID[withoutGoal.ID]; got.HasGoal || got.GoalMet {
		t.Fatalf("missing goal projection = HasGoal %t GoalMet %t, want false/false", got.HasGoal, got.GoalMet)
	}
}

func TestTaskRepo_ListByProject_OrderingFIFO(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with the same priority to test FIFO ordering by created_at
	task1 := &models.Task{ProjectID: "default", Title: "First Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Second Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Third Task", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// List tasks in the active category
	tasks, err := repo.ListByProject(ctx, "default", "active")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Verify FIFO ordering (oldest first)
	if tasks[0].Title != "First Task" {
		t.Errorf("expected first task to be 'First Task', got %q", tasks[0].Title)
	}
	if tasks[1].Title != "Second Task" {
		t.Errorf("expected second task to be 'Second Task', got %q", tasks[1].Title)
	}
	if tasks[2].Title != "Third Task" {
		t.Errorf("expected third task to be 'Third Task', got %q", tasks[2].Title)
	}

	// Verify created_at timestamps are in ascending order
	if tasks[0].CreatedAt.After(tasks[1].CreatedAt) {
		t.Error("expected tasks[0].CreatedAt <= tasks[1].CreatedAt")
	}
	if tasks[1].CreatedAt.After(tasks[2].CreatedAt) {
		t.Error("expected tasks[1].CreatedAt <= tasks[2].CreatedAt")
	}
}

func TestTaskRepo_ListActivePendingAdmissionsUsesCompactProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()
	largePrompt := strings.Repeat("prompt payload ", 4096)
	largeChainConfig := strings.Repeat("chain payload ", 2048)
	largeSwarmConfig := strings.Repeat("swarm payload ", 2048)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Compact active admission",
		Category:          models.CategoryActive,
		Priority:          4,
		Status:            models.StatusPending,
		Prompt:            largePrompt,
		ChainConfig:       largeChainConfig,
		SwarmRole:         models.SwarmRoleParent,
		SwarmConfig:       largeSwarmConfig,
		WorktreePath:      "/tmp/compact-admission-worktree",
		WorktreeBranch:    "task/compact-admission",
		MergeTargetBranch: "main",
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	admissions, err := repo.ListActivePendingAdmissions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListActivePendingAdmissions: %v", err)
	}
	if len(admissions) != 1 {
		t.Fatalf("expected one admission, got %d", len(admissions))
	}
	admission := admissions[0]
	if admission.ID != task.ID || admission.ProjectID != task.ProjectID || admission.Title != task.Title || admission.Category != task.Category || admission.Priority != task.Priority || admission.Status != task.Status || admission.SwarmRole != task.SwarmRole {
		t.Fatalf("unexpected admission fields: %#v", admission)
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("expected one admission query, got %d: %#v", len(statements), statements)
	}
	query := strings.ToLower(statements[0])
	const expectedProjection = "select id, project_id, title, category, priority, status, agent_id, agent_definition_id, parent_task_id, swarm_role"
	if !strings.Contains(query, expectedProjection) {
		t.Fatalf("admission query did not use the compact projection: %s", statements[0])
	}
	projection := strings.SplitN(query, " from ", 2)[0]
	for _, forbidden := range []string{"prompt", "chain_config", "swarm_config", "worktree_path", "worktree_branch", "merge_target_branch", "created_at", "updated_at", "completed_at"} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("admission query selected execution/detail column %q: %s", forbidden, statements[0])
		}
	}

	full, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if full == nil || full.Prompt != largePrompt || full.ChainConfig != largeChainConfig || full.SwarmConfig != largeSwarmConfig || full.WorktreePath != task.WorktreePath {
		t.Fatalf("authoritative task read did not preserve full payload: %#v", full)
	}
}

func TestTaskRepo_ListActivePendingAdmissionsEmptyResult(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	counter.Reset()
	counter.SetEnabled(true)
	admissions, err := repo.ListActivePendingAdmissions(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListActivePendingAdmissions: %v", err)
	}
	if len(admissions) != 0 {
		t.Fatalf("expected no active pending admissions, got %#v", admissions)
	}
	if statements := counter.Statements(); len(statements) != 1 {
		t.Fatalf("expected one bounded empty-result query, got %d: %#v", len(statements), statements)
	}
}

func TestTaskRepo_ListActivePendingAdmissionsPreservesEligibilityAndOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	inputRepo := NewThreadInputRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	first := &models.Task{ProjectID: "default", Title: "highest priority", Category: models.CategoryActive, Priority: 4, Status: models.StatusPending, Prompt: "first"}
	second := &models.Task{ProjectID: "default", Title: "same priority earlier display", Category: models.CategoryActive, Priority: 3, Status: models.StatusPending, Prompt: "second"}
	third := &models.Task{ProjectID: "default", Title: "same priority later display", Category: models.CategoryActive, Priority: 3, Status: models.StatusPending, Prompt: "third"}
	for _, task := range []*models.Task{first, second, third} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create eligible task %q: %v", task.Title, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = CASE title WHEN ? THEN 2 WHEN ? THEN 1 ELSE display_order END WHERE id IN (?, ?)`, second.Title, third.Title, second.ID, third.ID); err != nil {
		t.Fatalf("set display order: %v", err)
	}

	backlog := &models.Task{ProjectID: "default", Title: "backlog", Category: models.CategoryBacklog, Priority: 4, Status: models.StatusPending, Prompt: "excluded"}
	running := &models.Task{ProjectID: "default", Title: "running", Category: models.CategoryActive, Priority: 4, Status: models.StatusRunning, Prompt: "excluded"}
	queuedExecution := &models.Task{ProjectID: "default", Title: "queued execution", Category: models.CategoryActive, Priority: 4, Status: models.StatusPending, Prompt: "excluded"}
	pendingInput := &models.Task{ProjectID: "default", Title: "pending input", Category: models.CategoryActive, Priority: 4, Status: models.StatusPending, Prompt: "excluded"}
	for _, task := range []*models.Task{backlog, running, queuedExecution, pendingInput} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create excluded task %q: %v", task.Title, err)
		}
	}
	queuedExec := &models.Execution{TaskID: queuedExecution.ID, Status: models.ExecQueued, PromptSent: "queued"}
	if err := execRepo.Create(ctx, queuedExec); err != nil {
		t.Fatalf("create queued execution: %v", err)
	}
	prior := &models.Execution{TaskID: pendingInput.ID, Status: models.ExecCompleted, PromptSent: "prior"}
	if err := execRepo.Create(ctx, prior); err != nil {
		t.Fatalf("create prior execution: %v", err)
	}
	queuedInput := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: pendingInput.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "follow-up"}
	if err := inputRepo.CreateQueued(ctx, queuedInput); err != nil {
		t.Fatalf("create pending input: %v", err)
	}

	reserved := &models.Task{ProjectID: "default", Title: "reserved", Category: models.CategoryActive, Priority: 4, Status: models.StatusPending, Prompt: "excluded"}
	if err := repo.Create(ctx, reserved); err != nil {
		t.Fatalf("create reserved task: %v", err)
	}
	reservationStatements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automations (id, project_id, stable_key, name) VALUES ('admission-projection-auto', 'default', 'admission-projection-auto', 'Admission projection')`, nil},
		{`INSERT INTO automation_versions (id, project_id, automation_id, version, adapter_key) VALUES ('admission-projection-version', 'default', 'admission-projection-auto', 1, 'test')`, nil},
		{`INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role) VALUES ('admission-projection-node', 'default', 'admission-projection-auto', 'admission-projection-version', 'trigger', 'Trigger', 'trigger', 'trigger')`, nil},
		{`INSERT INTO automation_invocations (id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key) VALUES ('admission-projection-invocation', 'default', 'admission-projection-auto', 'admission-projection-version', 'admission-projection-node', 'schedule', 'fixture', 'fixture')`, nil},
		{`INSERT INTO automation_dispatch_outbox (id, invocation_id, task_id) VALUES ('admission-projection-dispatch', 'admission-projection-invocation', ?)`, []any{reserved.ID}},
		{`INSERT INTO automation_task_run_reservations (task_id, dispatch_id, project_id) VALUES (?, 'admission-projection-dispatch', 'default')`, []any{reserved.ID}},
	}
	for _, statement := range reservationStatements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("create reservation fixture: %v", err)
		}
	}

	admissions, err := repo.ListActivePendingAdmissions(ctx)
	if err != nil {
		t.Fatalf("ListActivePendingAdmissions: %v", err)
	}
	if len(admissions) != 3 {
		t.Fatalf("expected three eligible admissions, got %d: %#v", len(admissions), admissions)
	}
	gotTitles := []string{admissions[0].Title, admissions[1].Title, admissions[2].Title}
	wantTitles := []string{first.Title, third.Title, second.Title}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Errorf("admission %d: got %q, want %q (all=%#v)", i, gotTitles[i], wantTitles[i], gotTitles)
		}
	}
}

// TestTaskRepo_ListByCategory_WithChainConfig verifies ListByCategory correctly
// scans parent_task_id and chain_config columns.
func TestTaskRepo_ListByCategory_WithChainConfig(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID:   "default",
		Title:       "Chained Backlog Task",
		Category:    models.CategoryBacklog,
		Status:      models.StatusPending,
		Prompt:      "test",
		ChainConfig: `{"enabled":true,"trigger":"on_completion"}`,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	tasks, err := repo.ListByCategory(ctx, models.CategoryBacklog)
	if err != nil {
		t.Fatalf("ListByCategory: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].ChainConfig != `{"enabled":true,"trigger":"on_completion"}` {
		t.Errorf("expected ChainConfig preserved, got %q", tasks[0].ChainConfig)
	}
}

func TestTaskRepo_UpdateStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Status Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Status != models.StatusRunning {
		t.Errorf("expected Status=running, got %q", got.Status)
	}
}

func TestTaskRepo_UpdateCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Category Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Category != models.CategoryBacklog {
		t.Errorf("expected Category=backlog, got %q", got.Category)
	}
}

func TestTaskRepo_CountByProjectAndCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "A1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "A2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "B1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	counts, err := repo.CountByProjectAndCategory(ctx, "default")
	if err != nil {
		t.Fatalf("CountByProjectAndCategory: %v", err)
	}
	if counts["active"] != 2 {
		t.Errorf("expected active=2, got %d", counts["active"])
	}
	if counts["backlog"] != 1 {
		t.Errorf("expected backlog=1, got %d", counts["backlog"])
	}
}

func TestTaskRepo_CountPendingByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a second project
	project2 := &models.Project{Name: "Project2", RepoPath: "/tmp/test2"}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project2: %v", err)
	}

	// Create active pending/queued tasks for default project (should be counted as queue)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P1b", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Q1", Category: models.CategoryActive, Status: models.StatusQueued, Prompt: "p"})

	// Create backlog+pending task (should NOT be counted - not in active queue)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "P2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// Create a running task for default project (should not be counted)
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "R1", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})

	// Create active+pending task for project2
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "P3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Create completed task for project2 (should not be counted)
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "C1", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Create scheduled+pending task for project2 (should NOT be counted - not in active queue)
	repo.Create(ctx, &models.Task{ProjectID: project2.ID, Title: "S1", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "p"})

	counts, err := repo.CountPendingByProject(ctx)
	if err != nil {
		t.Fatalf("CountPendingByProject: %v", err)
	}

	// Should count only active pending/queued tasks for default project (3, not backlog/running)
	if counts["default"] != 3 {
		t.Errorf("expected default=3, got %d", counts["default"])
	}

	// Should count 1 active+pending task for project2 (not scheduled or completed)
	if counts[project2.ID] != 1 {
		t.Errorf("expected project2=1, got %d", counts[project2.ID])
	}

	// Should not include projects with no active+pending tasks in the map
	if len(counts) != 2 {
		t.Errorf("expected 2 projects in map, got %d", len(counts))
	}
}

func TestTaskRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "ToDelete", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	if err := repo.Delete(ctx, task.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestTaskRepo_DeleteWithCleanupManifest_TerminalizesRetainedSwarmChildren(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	parent := &models.Task{ProjectID: "default", Title: "Swarm", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", SwarmRole: models.SwarmRoleParent, SwarmStatus: "current"}
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	planner := &models.Task{ProjectID: "default", Title: "Swarm · Planner", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRolePlanner, SwarmStatus: "planned"}
	worker := &models.Task{ProjectID: "default", Title: "Swarm · Worker", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "running"}
	reviewer := &models.Task{ProjectID: "default", Title: "Swarm · Reviewer", Category: models.CategoryActive, Status: models.StatusBlocked, Prompt: "p", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleReviewer, SwarmStatus: "pending"}
	merger := &models.Task{ProjectID: "default", Title: "Swarm · Merger", Category: models.CategoryActive, Status: models.StatusBlocked, Prompt: "p", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleMerger, SwarmStatus: "pending"}
	integrator := &models.Task{ProjectID: "default", Title: "Swarm · Integrator", Category: models.CategoryActive, Status: models.StatusBlocked, Prompt: "p", ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleLegacyIntegrator, SwarmStatus: "pending"}
	for _, child := range []*models.Task{planner, worker, reviewer, merger, integrator} {
		if err := repo.Create(ctx, child); err != nil {
			t.Fatalf("Create child %s: %v", child.SwarmRole, err)
		}
	}

	_, deleted, err := repo.DeleteWithCleanupManifest(ctx, parent.ID, nil)
	if err != nil {
		t.Fatalf("DeleteWithCleanupManifest: %v", err)
	}
	if !deleted {
		t.Fatal("expected swarm parent deletion")
	}

	for _, child := range []*models.Task{planner, worker, reviewer, merger, integrator} {
		got, err := repo.GetByID(ctx, child.ID)
		if err != nil || got == nil {
			t.Fatalf("retained child %s: task=%v err=%v", child.SwarmRole, got, err)
		}
		if got.ParentTaskID != nil {
			t.Errorf("child %s parent = %v, want nil after parent deletion", child.SwarmRole, *got.ParentTaskID)
		}
		if child == planner {
			if got.Status != models.StatusCompleted || got.SwarmStatus != "planned" || got.Category != models.CategoryCompleted {
				t.Errorf("completed planner changed during parent deletion: category=%s status=%s swarm_status=%s", got.Category, got.Status, got.SwarmStatus)
			}
			continue
		}
		if got.Status != models.StatusCancelled || got.SwarmStatus != "cancelled" || got.Category != models.CategoryCompleted {
			t.Errorf("unfinished child %s not terminalized: category=%s status=%s swarm_status=%s", child.SwarmRole, got.Category, got.Status, got.SwarmStatus)
		}
	}
}

func TestTaskRepo_GetByID_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("should not error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent")
	}
}

func TestTaskRepo_ClaimTask_PendingSucceeds(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Claim Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if !claimed {
		t.Error("expected claim to succeed for pending task")
	}

	got, _ := repo.GetByID(ctx, task.ID)
	if got.Status != models.StatusRunning {
		t.Errorf("expected status=running after claim, got %q", got.Status)
	}
}

func TestTaskRepo_ClaimTask_AlreadyRunningFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Running Claim", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed {
		t.Error("expected claim to fail for running task")
	}
}

func TestTaskRepo_ClaimTask_CompletedFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Completed Claim", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	repo.Create(ctx, task)

	claimed, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed {
		t.Error("expected claim to fail for completed task")
	}
}

func TestTaskRepo_ClaimTask_DoubleClaimFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Double Claim", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	repo.Create(ctx, task)

	// First claim succeeds
	claimed1, _ := repo.ClaimTask(ctx, task.ID)
	if !claimed1 {
		t.Fatal("expected first claim to succeed")
	}

	// Second claim fails (task is now running)
	claimed2, err := repo.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed2 {
		t.Error("expected second claim to fail (task already claimed)")
	}
}

func TestTaskRepo_MoveCompletedActiveToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create various tasks
	completedActive1 := &models.Task{ProjectID: "default", Title: "Completed Active 1", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	completedActive2 := &models.Task{ProjectID: "default", Title: "Completed Active 2", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	pendingActive := &models.Task{ProjectID: "default", Title: "Pending Active", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	completedBacklog := &models.Task{ProjectID: "default", Title: "Completed Backlog", Category: models.CategoryBacklog, Status: models.StatusCompleted, Prompt: "p"}
	alreadyCompleted := &models.Task{ProjectID: "default", Title: "Already Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}

	repo.Create(ctx, completedActive1)
	repo.Create(ctx, completedActive2)
	repo.Create(ctx, pendingActive)
	repo.Create(ctx, completedBacklog)
	repo.Create(ctx, alreadyCompleted)

	// Move completed active tasks to completed category
	count, err := repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		t.Fatalf("MoveCompletedActiveToCompleted: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks moved, got %d", count)
	}

	// Verify the tasks were moved
	task1, _ := repo.GetByID(ctx, completedActive1.ID)
	if task1.Category != models.CategoryCompleted {
		t.Errorf("expected task1 category=completed, got %q", task1.Category)
	}

	task2, _ := repo.GetByID(ctx, completedActive2.ID)
	if task2.Category != models.CategoryCompleted {
		t.Errorf("expected task2 category=completed, got %q", task2.Category)
	}

	// Verify other tasks were not affected
	pendingTask, _ := repo.GetByID(ctx, pendingActive.ID)
	if pendingTask.Category != models.CategoryActive {
		t.Errorf("expected pending task still in active, got %q", pendingTask.Category)
	}

	backlogTask, _ := repo.GetByID(ctx, completedBacklog.ID)
	if backlogTask.Category != models.CategoryBacklog {
		t.Errorf("expected backlog task still in backlog, got %q", backlogTask.Category)
	}

	completedTask, _ := repo.GetByID(ctx, alreadyCompleted.ID)
	if completedTask.Category != models.CategoryCompleted {
		t.Errorf("expected completed task still in completed, got %q", completedTask.Category)
	}
}

func TestTaskRepo_MoveCompletedActiveToCompleted_NoCompletedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create only non-completed or non-active tasks
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Pending Active", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running Active", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})

	// Should move 0 tasks
	count, err := repo.MoveCompletedActiveToCompleted(ctx)
	if err != nil {
		t.Fatalf("MoveCompletedActiveToCompleted: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks moved, got %d", count)
	}
}

func TestTaskRepo_Create_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task1); err != nil {
		t.Fatalf("Create first task: %v", err)
	}

	// Attempt to create another task with the same title in the same project
	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Duplicate Task",
		//Description: "Second task",
		Category: models.CategoryBacklog,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	err := repo.Create(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskRepo_Create_SameTitleDifferentProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a second project
	project2 := &models.Project{
		Name: "Project 2",
		//Description: "Second project",
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Create task in first project
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Same Title",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task1); err != nil {
		t.Fatalf("Create first task: %v", err)
	}

	// Create task with same title in second project - should succeed
	task2 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Same Title",
		//Description: "Second task",
		Category: models.CategoryBacklog,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	if err := repo.Create(ctx, task2); err != nil {
		t.Errorf("expected no error for same title in different project, got %v", err)
	}
}

func TestTaskRepo_Update_DuplicateTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create two tasks with different titles
	task1 := &models.Task{
		ProjectID: "default",
		Title:     "Task 1",
		//Description: "First task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task1)

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Task 2",
		//Description: "Second task",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task2)

	// Try to update task2 to have the same title as task1
	task2.Title = "Task 1"
	err := repo.Update(ctx, task2)
	if err != ErrDuplicateTask {
		t.Errorf("expected ErrDuplicateTask, got %v", err)
	}
}

func TestTaskRepo_Update_SameTitleAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Original Title",
		//Description: "Original description",
		Category: models.CategoryActive,
		Status:   models.StatusPending,
		Prompt:   "p",
	}
	repo.Create(ctx, task)

	// Update other fields but keep the same title - should succeed
	task.Priority = 5
	if err := repo.Update(ctx, task); err != nil {
		t.Errorf("expected no error when updating task with same title, got %v", err)
	}

	// Verify the update worked
	got, _ := repo.GetByID(ctx, task.ID)
	if got.Priority != 5 {
		t.Errorf("expected priority=5, got %d", got.Priority)
	}
}

func TestTaskRepo_DeleteAllCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create tasks in various categories and statuses
	completedTask1 := &models.Task{ProjectID: project1.ID, Title: "Completed 1", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	completedTask2 := &models.Task{ProjectID: project1.ID, Title: "Completed 2", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}
	activeTask := &models.Task{ProjectID: project1.ID, Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	backlogTask := &models.Task{ProjectID: project1.ID, Title: "Backlog Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	// Create completed task in a different project (should NOT be deleted)
	completedTaskProject2 := &models.Task{ProjectID: project2.ID, Title: "Completed Other Project", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"}

	repo.Create(ctx, completedTask1)
	repo.Create(ctx, completedTask2)
	repo.Create(ctx, activeTask)
	repo.Create(ctx, backlogTask)
	repo.Create(ctx, completedTaskProject2)

	// Delete all completed tasks for project1
	count, err := repo.DeleteAllCompleted(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllCompleted: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify completed tasks from project1 were deleted
	task1, _ := repo.GetByID(ctx, completedTask1.ID)
	if task1 != nil {
		t.Error("expected completed task 1 to be deleted")
	}

	task2, _ := repo.GetByID(ctx, completedTask2.ID)
	if task2 != nil {
		t.Error("expected completed task 2 to be deleted")
	}

	// Verify other tasks from project1 were not affected
	activeTaskResult, _ := repo.GetByID(ctx, activeTask.ID)
	if activeTaskResult == nil {
		t.Error("expected active task to still exist")
	}

	backlogTaskResult, _ := repo.GetByID(ctx, backlogTask.ID)
	if backlogTaskResult == nil {
		t.Error("expected backlog task to still exist")
	}

	// Verify completed task from project2 was NOT deleted
	taskProject2, _ := repo.GetByID(ctx, completedTaskProject2.ID)
	if taskProject2 == nil {
		t.Error("expected completed task from project2 to still exist")
	}
}

func TestTaskRepo_DeleteAllCompleted_NoCompletedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllCompleted(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllCompleted: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_DeleteAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create backlog tasks for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask1); err != nil {
		t.Fatalf("failed to create backlog task 1: %v", err)
	}
	if err := repo.Create(ctx, backlogTask2); err != nil {
		t.Fatalf("failed to create backlog task 2: %v", err)
	}

	// Create a backlog task for project2 (should not be deleted)
	backlogTask3 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Backlog Task 3",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask3); err != nil {
		t.Fatalf("failed to create backlog task 3: %v", err)
	}

	// Create tasks in other categories for project1 (should not be deleted)
	activeTask := &models.Task{
		ProjectID: project1.ID,
		Title:     "Active Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, activeTask); err != nil {
		t.Fatalf("failed to create active task: %v", err)
	}

	// Delete all backlog tasks for project1
	count, err := repo.DeleteAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify backlog tasks from project1 are deleted
	task, _ := repo.GetByID(ctx, backlogTask1.ID)
	if task != nil {
		t.Error("expected backlog task 1 from project1 to be deleted")
	}
	task, _ = repo.GetByID(ctx, backlogTask2.ID)
	if task != nil {
		t.Error("expected backlog task 2 from project1 to be deleted")
	}

	// Verify backlog task from project2 still exists
	task, _ = repo.GetByID(ctx, backlogTask3.ID)
	if task == nil {
		t.Error("expected backlog task from project2 to still exist")
	}

	// Verify active task from project1 still exists
	task, _ = repo.GetByID(ctx, activeTask.ID)
	if task == nil {
		t.Error("expected active task from project1 to still exist")
	}
}

func TestTaskRepo_DeleteAllBacklog_NoBacklogTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllBacklog: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_ActivateAllBacklog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create backlog tasks for project1
	backlogTask1 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 1",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	backlogTask2 := &models.Task{
		ProjectID: project1.ID,
		Title:     "Backlog Task 2",
		Category:  models.CategoryBacklog,
		Status:    models.StatusCompleted, // Test that status is reset to pending
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask1); err != nil {
		t.Fatalf("failed to create backlog task 1: %v", err)
	}
	if err := repo.Create(ctx, backlogTask2); err != nil {
		t.Fatalf("failed to create backlog task 2: %v", err)
	}

	// Create a backlog task for project2 (should not be activated)
	backlogTask3 := &models.Task{
		ProjectID: project2.ID,
		Title:     "Backlog Task 3",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "p",
	}
	if err := repo.Create(ctx, backlogTask3); err != nil {
		t.Fatalf("failed to create backlog task 3: %v", err)
	}

	// Activate all backlog tasks for project1
	count, err := repo.ActivateAllBacklog(ctx, project1.ID)
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks activated, got %d", count)
	}

	// Verify backlog tasks from project1 are now active with pending status
	task, _ := repo.GetByID(ctx, backlogTask1.ID)
	if task == nil {
		t.Fatal("expected backlog task 1 from project1 to exist")
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected task 1 category to be active, got %s", task.Category)
	}
	if task.Status != models.StatusPending {
		t.Errorf("expected task 1 status to be pending, got %s", task.Status)
	}

	task, _ = repo.GetByID(ctx, backlogTask2.ID)
	if task == nil {
		t.Fatal("expected backlog task 2 from project1 to exist")
	}
	if task.Category != models.CategoryActive {
		t.Errorf("expected task 2 category to be active, got %s", task.Category)
	}
	if task.Status != models.StatusPending {
		t.Errorf("expected task 2 status to be pending (reset from completed), got %s", task.Status)
	}

	// Verify backlog task from project2 is still backlog
	task, _ = repo.GetByID(ctx, backlogTask3.ID)
	if task == nil {
		t.Fatal("expected backlog task from project2 to exist")
	}
	if task.Category != models.CategoryBacklog {
		t.Errorf("expected task 3 category to still be backlog, got %s", task.Category)
	}
}

func TestTaskRepo_ActivateAllBacklog_NoBacklogTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Should activate 0 tasks
	count, err := repo.ActivateAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks activated, got %d", count)
	}
}

func TestTaskRepo_ActivateAllBacklog_SkipsBlockedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a parent task (needed for foreign key on parent_task_id)
	parentTask := &models.Task{ProjectID: "default", Title: "Parent Task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"}
	if err := repo.Create(ctx, parentTask); err != nil {
		t.Fatalf("Create parent task: %v", err)
	}

	// Create a normal backlog task and a blocked backlog task
	normalTask := &models.Task{ProjectID: "default", Title: "Normal Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	blockedTask := &models.Task{ProjectID: "default", Title: "Blocked Child", Category: models.CategoryBacklog, Status: models.StatusBlocked, Prompt: "waiting", ParentTaskID: &parentTask.ID}
	if err := repo.Create(ctx, normalTask); err != nil {
		t.Fatalf("Create normal task: %v", err)
	}
	if err := repo.Create(ctx, blockedTask); err != nil {
		t.Fatalf("Create blocked task: %v", err)
	}

	count, err := repo.ActivateAllBacklog(ctx, "default")
	if err != nil {
		t.Fatalf("ActivateAllBacklog: %v", err)
	}
	// Only the normal task should be activated; blocked task stays in backlog
	if count != 1 {
		t.Errorf("expected 1 task activated (skipping blocked), got %d", count)
	}

	// Verify blocked child is still in backlog with blocked status
	bt, _ := repo.GetByID(ctx, blockedTask.ID)
	if bt == nil {
		t.Fatal("blocked task not found after ActivateAllBacklog")
	}
	if bt.Category != models.CategoryBacklog {
		t.Errorf("expected blocked child category=backlog, got %s", bt.Category)
	}
	if bt.Status != models.StatusBlocked {
		t.Errorf("expected blocked child status=blocked, got %s", bt.Status)
	}
}

func TestTaskRepo_ReorderTask_MoveDown(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in order: Task 0, Task 1, Task 2, Task 3
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move Task 1 (position 1) to position 3 (moving down)
	// Expected order after: Task 0, Task 2, Task 3, Task 1
	if err := repo.ReorderTask(ctx, task1.ID, 3); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify the new order
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 2", "Task 3", "Task 1"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveUp(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in order: Task 0, Task 1, Task 2, Task 3
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move Task 3 (position 3) to position 1 (moving up)
	// Expected order after: Task 0, Task 3, Task 1, Task 2
	if err := repo.ReorderTask(ctx, task3.ID, 1); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify the new order
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 3", "Task 1", "Task 2"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveToFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Move Task 2 to position 0 (first)
	// Expected order: Task 2, Task 0, Task 1
	if err := repo.ReorderTask(ctx, task2.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	expectedOrder := []string{"Task 2", "Task 0", "Task 1"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ReorderTask_MoveToLast(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Move Task 0 to position 2 (last)
	// Expected order: Task 1, Task 2, Task 0
	if err := repo.ReorderTask(ctx, task0.ID, 2); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	expectedOrder := []string{"Task 1", "Task 2", "Task 0"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ReorderTask_LastTaskToEnd(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	// Move the last task (Task 3 at position 3) to position 3 (same position)
	// This simulates dragging the last task to the end of the dropzone
	// With the bug, this would create a gap (position 4), but should stay at position 3
	if err := repo.ReorderTask(ctx, task3.ID, 3); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify order is unchanged
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks, got %d", len(tasks))
	}

	expectedOrder := []string{"Task 0", "Task 1", "Task 2", "Task 3"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		// CRITICAL: Verify display_order values are sequential (no gaps)
		if task.DisplayOrder != i {
			t.Errorf("%s: expected DisplayOrder=%d, got %d (gap detected!)", task.Title, i, task.DisplayOrder)
		}
	}
}

func TestTaskRepo_ReorderTask_NoChange(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)

	// Move Task 0 to position 0 (same position)
	if err := repo.ReorderTask(ctx, task0.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify order unchanged
	tasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if tasks[0].Title != "Task 0" || tasks[1].Title != "Task 1" {
		t.Error("expected order to remain unchanged")
	}
}

func TestTaskRepo_ReorderTask_OnlyCategoryTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	backlog0 := &models.Task{ProjectID: "default", Title: "Backlog 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	backlog1 := &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	active0 := &models.Task{ProjectID: "default", Title: "Active 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	active1 := &models.Task{ProjectID: "default", Title: "Active 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, backlog0)
	repo.Create(ctx, backlog1)
	repo.Create(ctx, active0)
	repo.Create(ctx, active1)

	// Reorder backlog1 to position 0
	if err := repo.ReorderTask(ctx, backlog1.ID, 0); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Verify only backlog tasks were affected
	backlogTasks, _ := repo.ListByProject(ctx, "default", "backlog")
	if len(backlogTasks) != 2 {
		t.Fatalf("expected 2 backlog tasks, got %d", len(backlogTasks))
	}
	if backlogTasks[0].Title != "Backlog 1" {
		t.Errorf("expected Backlog 1 first, got %q", backlogTasks[0].Title)
	}

	// Verify active tasks unchanged
	activeTasks, _ := repo.ListByProject(ctx, "default", "active")
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	if activeTasks[0].Title != "Active 0" || activeTasks[1].Title != "Active 1" {
		t.Error("active tasks should not be affected by backlog reorder")
	}
}

func TestTaskRepo_ReorderTask_NonexistentTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	err := repo.ReorderTask(ctx, "nonexistent-id", 0)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestTaskRepo_Create_AssignsDisplayOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in sequence
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0)
	repo.Create(ctx, task1)
	repo.Create(ctx, task2)

	// Verify display orders are assigned sequentially per category
	got0, _ := repo.GetByID(ctx, task0.ID)
	if got0.DisplayOrder != 0 {
		t.Errorf("task0: expected DisplayOrder=0, got %d", got0.DisplayOrder)
	}

	got1, _ := repo.GetByID(ctx, task1.ID)
	if got1.DisplayOrder != 1 {
		t.Errorf("task1: expected DisplayOrder=1, got %d", got1.DisplayOrder)
	}

	// task2 is in a different category, should also start at 0
	got2, _ := repo.GetByID(ctx, task2.ID)
	if got2.DisplayOrder != 0 {
		t.Errorf("task2: expected DisplayOrder=0 (different category), got %d", got2.DisplayOrder)
	}
}

func TestTaskRepo_UpdateCategory_AssignsNewDisplayOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks: 2 in backlog, 1 in active
	backlog0 := &models.Task{ProjectID: "default", Title: "Backlog 0", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	backlog1 := &models.Task{ProjectID: "default", Title: "Backlog 1", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"}
	active0 := &models.Task{ProjectID: "default", Title: "Active 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, backlog0)
	repo.Create(ctx, backlog1)
	repo.Create(ctx, active0)

	// Move backlog1 to active - should get display_order = 1 (after active0)
	if err := repo.UpdateCategory(ctx, backlog1.ID, models.CategoryActive); err != nil {
		t.Fatalf("UpdateCategory: %v", err)
	}

	got, _ := repo.GetByID(ctx, backlog1.ID)
	if got.DisplayOrder != 1 {
		t.Errorf("expected DisplayOrder=1 after moving to active, got %d", got.DisplayOrder)
	}

	// Verify it appears in correct order
	activeTasks, _ := repo.ListByProject(ctx, "default", "active")
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	if activeTasks[0].Title != "Active 0" || activeTasks[1].Title != "Backlog 1" {
		t.Error("tasks not in expected order after category change")
	}
}

func TestTaskRepo_TaskTags(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Test creating task with feature tag
	featureTask := &models.Task{
		ProjectID: "default",
		Title:     "Feature Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Implement feature",
		Tag:       models.TagFeature,
	}
	if err := repo.Create(ctx, featureTask); err != nil {
		t.Fatalf("Create feature task: %v", err)
	}

	// Test creating task with bug tag
	bugTask := &models.Task{
		ProjectID: "default",
		Title:     "Bug Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Fix bug",
		Tag:       models.TagBug,
	}
	if err := repo.Create(ctx, bugTask); err != nil {
		t.Fatalf("Create bug task: %v", err)
	}

	// Test creating task with no tag
	noTagTask := &models.Task{
		ProjectID: "default",
		Title:     "No Tag Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Do something",
		Tag:       models.TagNone,
	}
	if err := repo.Create(ctx, noTagTask); err != nil {
		t.Fatalf("Create no tag task: %v", err)
	}

	// Verify tags are saved correctly
	gotFeature, err := repo.GetByID(ctx, featureTask.ID)
	if err != nil {
		t.Fatalf("GetByID feature: %v", err)
	}
	if gotFeature.Tag != models.TagFeature {
		t.Errorf("expected Tag=feature, got %q", gotFeature.Tag)
	}

	gotBug, err := repo.GetByID(ctx, bugTask.ID)
	if err != nil {
		t.Fatalf("GetByID bug: %v", err)
	}
	if gotBug.Tag != models.TagBug {
		t.Errorf("expected Tag=bug, got %q", gotBug.Tag)
	}

	gotNoTag, err := repo.GetByID(ctx, noTagTask.ID)
	if err != nil {
		t.Fatalf("GetByID no tag: %v", err)
	}
	if gotNoTag.Tag != models.TagNone {
		t.Errorf("expected Tag='', got %q", gotNoTag.Tag)
	}

	// Test updating task tag
	featureTask.Tag = models.TagBug
	if err := repo.Update(ctx, featureTask); err != nil {
		t.Fatalf("Update tag: %v", err)
	}

	gotUpdated, err := repo.GetByID(ctx, featureTask.ID)
	if err != nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if gotUpdated.Tag != models.TagBug {
		t.Errorf("expected Tag=bug after update, got %q", gotUpdated.Tag)
	}

	// Verify tags are returned in list operations
	allTasks, err := repo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(allTasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(allTasks))
	}

	// Count tasks by tag
	bugCount := 0
	featureCount := 0
	noTagCount := 0
	for _, task := range allTasks {
		switch task.Tag {
		case models.TagBug:
			bugCount++
		case models.TagFeature:
			featureCount++
		case models.TagNone:
			noTagCount++
		}
	}
	if bugCount != 2 {
		t.Errorf("expected 2 bug tasks, got %d", bugCount)
	}
	if featureCount != 0 {
		t.Errorf("expected 0 feature tasks, got %d", featureCount)
	}
	if noTagCount != 1 {
		t.Errorf("expected 1 no-tag task, got %d", noTagCount)
	}
}

// TestTaskRepo_ReorderTask_NonContiguousPositions tests reordering when display_order values have gaps
// This simulates the scenario in the Active column where Running/Queued tasks have non-contiguous positions
func TestTaskRepo_ReorderTask_NonContiguousPositions(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with contiguous positions
	task0 := &models.Task{ProjectID: "default", Title: "Task 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task1 := &models.Task{ProjectID: "default", Title: "Task 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task2 := &models.Task{ProjectID: "default", Title: "Task 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task3 := &models.Task{ProjectID: "default", Title: "Task 3", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	task4 := &models.Task{ProjectID: "default", Title: "Task 4", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, task0) // display_order = 0
	repo.Create(ctx, task1) // display_order = 1
	repo.Create(ctx, task2) // display_order = 2
	repo.Create(ctx, task3) // display_order = 3
	repo.Create(ctx, task4) // display_order = 4

	// Delete task2 to create a gap (display_order will be: 0, 1, 3, 4)
	repo.Delete(ctx, task2.ID)

	// Now simulate dragging task0 to the end (position after task4)
	// The frontend fix should send position = task4.display_order + 1 = 5
	if err := repo.ReorderTask(ctx, task0.ID, 5); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Expected order: Task 1, Task 3, Task 4, Task 0
	tasks, _ := repo.ListByProject(ctx, "default", "active")
	expectedOrder := []string{"Task 1", "Task 3", "Task 4", "Task 0"}
	if len(tasks) != len(expectedOrder) {
		t.Fatalf("expected %d tasks, got %d", len(expectedOrder), len(tasks))
	}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}

	// Verify task0 moved to the end
	if tasks[len(tasks)-1].Title != "Task 0" {
		t.Errorf("expected Task 0 at end, got %q", tasks[len(tasks)-1].Title)
	}
}

// TestTaskRepo_ReorderTask_SubZoneScenario tests the specific bug where tasks in a sub-zone
// (like Queued in Active column) couldn't be reordered to the end properly
func TestTaskRepo_ReorderTask_SubZoneScenario(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Simulate Running tasks (display_order 0, 1) and Queued tasks (display_order 2, 3, 4)
	running0 := &models.Task{ProjectID: "default", Title: "Running 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	running1 := &models.Task{ProjectID: "default", Title: "Running 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued0 := &models.Task{ProjectID: "default", Title: "Queued 0", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued1 := &models.Task{ProjectID: "default", Title: "Queued 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	queued2 := &models.Task{ProjectID: "default", Title: "Queued 2", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}

	repo.Create(ctx, running0) // display_order = 0
	repo.Create(ctx, running1) // display_order = 1
	repo.Create(ctx, queued0)  // display_order = 2
	repo.Create(ctx, queued1)  // display_order = 3
	repo.Create(ctx, queued2)  // display_order = 4

	// Drag Queued 0 (position 2) to the end of the Queued sub-zone
	// The frontend fix should send position = queued2.display_order + 1 = 5
	// Previously, it would send taskCards.length = 2 (only counting queued1 and queued2 after filtering)
	// which would result in no change since oldPosition=2, newPosition=2
	if err := repo.ReorderTask(ctx, queued0.ID, 5); err != nil {
		t.Fatalf("ReorderTask: %v", err)
	}

	// Expected order: Running 0, Running 1, Queued 1, Queued 2, Queued 0
	tasks, _ := repo.ListByProject(ctx, "default", "active")
	expectedOrder := []string{"Running 0", "Running 1", "Queued 1", "Queued 2", "Queued 0"}
	if len(tasks) != len(expectedOrder) {
		t.Fatalf("expected %d tasks, got %d", len(expectedOrder), len(tasks))
	}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_DeleteAllChat(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create test projects
	project1 := &models.Project{Name: "Test Project 1"}
	project2 := &models.Project{Name: "Test Project 2"}
	if err := projectRepo.Create(ctx, project1); err != nil {
		t.Fatalf("failed to create project1: %v", err)
	}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("failed to create project2: %v", err)
	}

	// Create an agent for executions
	agent := &models.LLMConfig{
		Name:      "Test Agent",
		Provider:  models.ProviderAnthropic,
		Model:     "claude-3-sonnet-20240229",
		APIKey:    "test-key",
		IsDefault: true,
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create chat tasks in project1
	chatTask1 := &models.Task{ProjectID: project1.ID, Title: "Chat 1", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "hello"}
	chatTask2 := &models.Task{ProjectID: project1.ID, Title: "Chat 2", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "world"}
	// Create non-chat task in project1 (should NOT be deleted)
	activeTask := &models.Task{ProjectID: project1.ID, Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	// Create chat task in project2 (should NOT be deleted)
	chatTaskProject2 := &models.Task{ProjectID: project2.ID, Title: "Chat Other", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "other"}

	repo.Create(ctx, chatTask1)
	repo.Create(ctx, chatTask2)
	repo.Create(ctx, activeTask)
	repo.Create(ctx, chatTaskProject2)

	// Create executions for chat tasks (should be cascade-deleted)
	exec1 := &models.Execution{TaskID: chatTask1.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "hello"}
	exec2 := &models.Execution{TaskID: chatTask2.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "world"}
	execRepo.Create(ctx, exec1)
	execRepo.Create(ctx, exec2)

	// Delete all chat tasks for project1
	count, err := repo.DeleteAllChat(ctx, project1.ID)
	if err != nil {
		t.Fatalf("DeleteAllChat: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 tasks deleted, got %d", count)
	}

	// Verify chat tasks from project1 were deleted
	task1, _ := repo.GetByID(ctx, chatTask1.ID)
	if task1 != nil {
		t.Error("expected chat task 1 to be deleted")
	}
	task2, _ := repo.GetByID(ctx, chatTask2.ID)
	if task2 != nil {
		t.Error("expected chat task 2 to be deleted")
	}

	// Verify executions were cascade-deleted
	e1, _ := execRepo.GetByID(ctx, exec1.ID)
	if e1 != nil {
		t.Error("expected execution 1 to be cascade-deleted")
	}
	e2, _ := execRepo.GetByID(ctx, exec2.ID)
	if e2 != nil {
		t.Error("expected execution 2 to be cascade-deleted")
	}

	// Verify active task was NOT deleted
	activeResult, _ := repo.GetByID(ctx, activeTask.ID)
	if activeResult == nil {
		t.Error("expected active task to still exist")
	}

	// Verify chat task from project2 was NOT deleted
	otherResult, _ := repo.GetByID(ctx, chatTaskProject2.ID)
	if otherResult == nil {
		t.Error("expected chat task from project2 to still exist")
	}
}

func TestTaskRepo_DeleteAllChat_NoChatTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in other categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Should delete 0 tasks
	count, err := repo.DeleteAllChat(ctx, "default")
	if err != nil {
		t.Fatalf("DeleteAllChat: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 tasks deleted, got %d", count)
	}
}

func TestTaskRepo_ListByProjectWithSort_TitleAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create backlog tasks with different titles
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mango Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Create active task to ensure it's not affected
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Fetch with title ascending sort
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	// Filter backlog tasks
	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	if len(backlogTasks) != 3 {
		t.Fatalf("expected 3 backlog tasks, got %d", len(backlogTasks))
	}

	expectedOrder := []string{"Apple Task", "Mango Task", "Zebra Task"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_TitleDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mango Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	expectedOrder := []string{"Zebra Task", "Mango Task", "Apple Task"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_PriorityDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different priorities
	task1 := &models.Task{ProjectID: "default", Title: "Low Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1}
	task2 := &models.Task{ProjectID: "default", Title: "High Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 3}
	task3 := &models.Task{ProjectID: "default", Title: "Urgent Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4}
	task4 := &models.Task{ProjectID: "default", Title: "Normal Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)
	repo.Create(ctx, task4)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "priority_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	if len(backlogTasks) != 4 {
		t.Fatalf("expected 4 backlog tasks, got %d", len(backlogTasks))
	}

	expectedPriorities := []int{4, 3, 2, 1}
	for i, task := range backlogTasks {
		if task.Priority != expectedPriorities[i] {
			t.Errorf("position %d: expected priority %d, got %d", i, expectedPriorities[i], task.Priority)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_PriorityAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{ProjectID: "default", Title: "Low Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1}
	task2 := &models.Task{ProjectID: "default", Title: "High Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 3}
	task3 := &models.Task{ProjectID: "default", Title: "Urgent Priority", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "priority_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	expectedPriorities := []int{1, 3, 4}
	for i, task := range backlogTasks {
		if task.Priority != expectedPriorities[i] {
			t.Errorf("position %d: expected priority %d, got %d", i, expectedPriorities[i], task.Priority)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_CreatedDesc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in sequence
	task1 := &models.Task{ProjectID: "default", Title: "First", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task2 := &models.Task{ProjectID: "default", Title: "Second", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task3 := &models.Task{ProjectID: "default", Title: "Third", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "created_desc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Newest first
	expectedOrder := []string{"Third", "Second", "First"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_CreatedAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	task1 := &models.Task{ProjectID: "default", Title: "First", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task2 := &models.Task{ProjectID: "default", Title: "Second", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}
	task3 := &models.Task{ProjectID: "default", Title: "Third", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2}

	repo.Create(ctx, task1)
	repo.Create(ctx, task2)
	repo.Create(ctx, task3)

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "created_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Oldest first
	expectedOrder := []string{"First", "Second", "Third"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_OnlyBacklogCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks in different categories
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Z", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog A", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Z", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Fetch with sorting (should only affect backlog)
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	// Separate tasks by category
	var backlogTasks, activeTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		} else if task.Category == models.CategoryActive {
			activeTasks = append(activeTasks, task)
		}
	}

	// Backlog should be sorted by title
	if len(backlogTasks) != 2 {
		t.Fatalf("expected 2 backlog tasks, got %d", len(backlogTasks))
	}
	if backlogTasks[0].Title != "Backlog A" || backlogTasks[1].Title != "Backlog Z" {
		t.Errorf("backlog tasks not sorted correctly: got %q, %q", backlogTasks[0].Title, backlogTasks[1].Title)
	}

	// Active tasks should maintain default order (display_order, created_at)
	if len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(activeTasks))
	}
	// Active tasks are created in order Z, A so they should appear in that order (by display_order)
	if activeTasks[0].Title != "Active Z" || activeTasks[1].Title != "Active A" {
		t.Errorf("active tasks affected by backlog sort: got %q, %q", activeTasks[0].Title, activeTasks[1].Title)
	}
}

func TestTaskRepo_ListByProjectWithSort_CaseInsensitive(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different case
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "apple", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Banana", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "CHERRY", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithSort(ctx, "default", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	var backlogTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryBacklog {
			backlogTasks = append(backlogTasks, task)
		}
	}

	// Should be sorted case-insensitively
	expectedOrder := []string{"apple", "Banana", "CHERRY"}
	for i, task := range backlogTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithSort_BacklogCategoryFilter(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zebra", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Apple", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	// Fetch only backlog category with sorting
	tasks, err := repo.ListByProjectWithSort(ctx, "default", "backlog", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithSort: %v", err)
	}

	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (backlog only), got %d", len(tasks))
	}

	// Should be sorted
	expectedOrder := []string{"Apple", "Zebra"}
	for i, task := range tasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
		if task.Category != models.CategoryBacklog {
			t.Errorf("expected only backlog tasks, got %s", task.Category)
		}
	}
}

func TestTaskRepo_ListBoardByProjectWithCategorySorts_ProjectsUnicodePromptPreview(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	fullPrompt := strings.Repeat("a", 299) + "界" + "🙂full prompt tail"
	task := &models.Task{
		ProjectID: "default",
		Title:     "Unicode preview",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    fullPrompt,
		Priority:  2,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	boardTasks, err := repo.ListBoardByProjectWithCategorySorts(ctx, "default", "", "", "")
	if err != nil {
		t.Fatalf("ListBoardByProjectWithCategorySorts: %v", err)
	}
	if len(boardTasks) != 1 {
		t.Fatalf("expected 1 board task, got %d", len(boardTasks))
	}
	wantPreview := strings.Repeat("a", 299) + "界"
	if boardTasks[0].Prompt != wantPreview {
		t.Fatalf("board prompt = %q, want 300-code-point preview %q", boardTasks[0].Prompt, wantPreview)
	}
	if got := utf8.RuneCountInString(boardTasks[0].Prompt); got != BoardPromptPreviewCodePoints {
		t.Fatalf("board prompt has %d code points, want %d", got, BoardPromptPreviewCodePoints)
	}

	fullTasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "", "")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}
	if len(fullTasks) != 1 || fullTasks[0].Prompt != fullPrompt {
		t.Fatalf("ordinary list did not retain full prompt")
	}
	got, err := repo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Prompt != fullPrompt {
		t.Fatalf("GetByID prompt was truncated")
	}
}

func TestTaskRepo_ListBoardByProjectWithCategorySorts_ProjectsFailedAndMergedState(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	failed := &models.Task{ProjectID: "default", Title: "Failed board task", Category: models.CategoryBacklog, Status: models.StatusFailed, Prompt: strings.Repeat("f", BoardPromptPreviewCodePoints+25), Priority: 2}
	merged := &models.Task{ProjectID: "default", Title: "Merged board task", Category: models.CategoryCompleted, Status: models.StatusCompleted, MergeStatus: models.MergeStatusMerged, Prompt: strings.Repeat("m", BoardPromptPreviewCodePoints+25), Priority: 2}
	for _, task := range []*models.Task{failed, merged} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create %q: %v", task.Title, err)
		}
	}

	boardTasks, err := repo.ListBoardByProjectWithCategorySorts(ctx, "default", "", "", "")
	if err != nil {
		t.Fatalf("ListBoardByProjectWithCategorySorts: %v", err)
	}
	byID := make(map[string]models.Task, len(boardTasks))
	for _, task := range boardTasks {
		byID[task.ID] = task
	}
	if got := byID[failed.ID]; got.Status != models.StatusFailed {
		t.Fatalf("failed board status = %q, want %q", got.Status, models.StatusFailed)
	}
	if got := byID[merged.ID]; got.Status != models.StatusCompleted || got.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("merged board state = status %q merge %q, want completed/merged", got.Status, got.MergeStatus)
	}
	for _, task := range boardTasks {
		if len([]rune(task.Prompt)) > BoardPromptPreviewCodePoints {
			t.Fatalf("board state projection hydrated an unbounded prompt for %q", task.Title)
		}
	}
}

func TestTaskRepo_ListBoardByProjectWithCategorySorts_PreservesOrderingAndMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	tasks := []*models.Task{
		{
			ProjectID:         "default",
			Title:             "Zulu backlog",
			Category:          models.CategoryBacklog,
			Status:            models.StatusPending,
			Prompt:            strings.Repeat("z", BoardPromptPreviewCodePoints+50),
			Priority:          4,
			Tag:               models.TagBug,
			ChainConfig:       `{"enabled":true,"trigger":"on_completion"}`,
			SwarmRole:         models.SwarmRoleParent,
			SwarmStatus:       "planning",
			SwarmConfig:       `{"mode":"autonomous","max_workers":2}`,
			SwarmSequence:     3,
			WorktreePath:      "/tmp/worktree",
			WorktreeBranch:    "task/branch",
			AutoMerge:         true,
			MergeTargetBranch: "main",
			MergeStatus:       models.MergeStatusPending,
			BaseBranch:        "main",
			BaseCommitSHA:     strings.Repeat("a", 40),
			LineageDepth:      2,
			CreatedVia:        models.TaskOriginSlack,
			TelegramChatID:    42,
		},
		{ProjectID: "default", Title: "Alpha backlog", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "short backlog", Priority: 1},
		{ProjectID: "default", Title: "Zulu completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "short completed", Priority: 2},
		{ProjectID: "default", Title: "Alpha completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "another completed", Priority: 3},
	}
	for i, task := range tasks {
		if i == 0 {
			goal := &models.TaskGoal{Objective: "preserve goal badge"}
			if err := repo.CreateWithGoal(ctx, task, goal); err != nil {
				t.Fatalf("CreateWithGoal: %v", err)
			}
			continue
		}
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create task %d: %v", i, err)
		}
	}

	fullTasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "title_desc", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}
	boardTasks, err := repo.ListBoardByProjectWithCategorySorts(ctx, "default", "", "title_desc", "title_asc")
	if err != nil {
		t.Fatalf("ListBoardByProjectWithCategorySorts: %v", err)
	}
	if len(boardTasks) != len(fullTasks) {
		t.Fatalf("board task count = %d, want %d", len(boardTasks), len(fullTasks))
	}
	for i := range fullTasks {
		if boardTasks[i].ID != fullTasks[i].ID {
			t.Fatalf("board task %d ID = %q, want ordered ID %q", i, boardTasks[i].ID, fullTasks[i].ID)
		}
		projected := boardTasks[i]
		projected.Prompt = fullTasks[i].Prompt
		if !reflect.DeepEqual(projected, fullTasks[i]) {
			t.Fatalf("board task %q metadata differs from full list\nboard: %#v\nfull: %#v", boardTasks[i].ID, projected, fullTasks[i])
		}
	}
}

func TestTaskRepo_ListByProjectWithCategorySorts_CompletedTitleAsc(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Zulu Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Alpha Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Mike Done", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}

	var completedTasks []models.Task
	for _, task := range tasks {
		if task.Category == models.CategoryCompleted {
			completedTasks = append(completedTasks, task)
		}
	}

	if len(completedTasks) != 3 {
		t.Fatalf("expected 3 completed tasks, got %d", len(completedTasks))
	}

	expectedOrder := []string{"Alpha Done", "Mike Done", "Zulu Done"}
	for i, task := range completedTasks {
		if task.Title != expectedOrder[i] {
			t.Errorf("position %d: expected %q, got %q", i, expectedOrder[i], task.Title)
		}
	}
}

func TestTaskRepo_ListByProjectWithCategorySorts_BacklogAndCompletedIndependent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog Z", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 1})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Backlog A", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p", Priority: 4})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed Z", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Completed A", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p", Priority: 3})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active Z", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Active A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p", Priority: 2})

	tasks, err := repo.ListByProjectWithCategorySorts(ctx, "default", "", "priority_desc", "title_asc")
	if err != nil {
		t.Fatalf("ListByProjectWithCategorySorts: %v", err)
	}

	var backlogTasks, completedTasks, activeTasks []models.Task
	for _, task := range tasks {
		switch task.Category {
		case models.CategoryBacklog:
			backlogTasks = append(backlogTasks, task)
		case models.CategoryCompleted:
			completedTasks = append(completedTasks, task)
		case models.CategoryActive:
			activeTasks = append(activeTasks, task)
		}
	}

	if len(backlogTasks) != 2 || len(completedTasks) != 2 || len(activeTasks) != 2 {
		t.Fatalf("unexpected category counts: backlog=%d completed=%d active=%d", len(backlogTasks), len(completedTasks), len(activeTasks))
	}

	if backlogTasks[0].Title != "Backlog A" || backlogTasks[1].Title != "Backlog Z" {
		t.Errorf("backlog sort mismatch: got %q, %q", backlogTasks[0].Title, backlogTasks[1].Title)
	}
	if completedTasks[0].Title != "Completed A" || completedTasks[1].Title != "Completed Z" {
		t.Errorf("completed sort mismatch: got %q, %q", completedTasks[0].Title, completedTasks[1].Title)
	}
	// Active tasks should remain in default display_order sequence.
	if activeTasks[0].Title != "Active Z" || activeTasks[1].Title != "Active A" {
		t.Errorf("active tasks affected by category sorts: got %q, %q", activeTasks[0].Title, activeTasks[1].Title)
	}
}

func TestTaskRepo_CountRunningByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with different statuses across projects
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running 1", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Running 2", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Pending 1", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})

	// Create a second project with tasks
	projRepo := NewProjectRepo(db)
	p2 := &models.Project{Name: "Project 2"}
	projRepo.Create(ctx, p2)
	repo.Create(ctx, &models.Task{ProjectID: p2.ID, Title: "P2 Running", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: p2.ID, Title: "P2 Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "p"})

	// Count running for default project
	count, err := repo.CountRunningByProject(ctx, "default")
	if err != nil {
		t.Fatalf("CountRunningByProject: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 running tasks for default project, got %d", count)
	}

	// Count running for project 2
	count, err = repo.CountRunningByProject(ctx, p2.ID)
	if err != nil {
		t.Fatalf("CountRunningByProject p2: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 running task for project 2, got %d", count)
	}

	// Count total running
	total, err := repo.CountRunningTotal(ctx)
	if err != nil {
		t.Fatalf("CountRunningTotal: %v", err)
	}
	if total != 3 {
		t.Errorf("expected 3 total running tasks, got %d", total)
	}
}

func TestTaskRepo_SearchByTitle(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks with various titles
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Fix login bug", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Add authentication", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Login page redesign", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	// Chat tasks should be excluded
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Chat about login", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "p"})

	// Search for "login" - should find 2 non-chat tasks
	results, err := repo.SearchByTitle(ctx, "default", "login")
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'login', got %d", len(results))
	}
	// Results should not include chat task
	for _, r := range results {
		if r.Category == models.CategoryChat {
			t.Errorf("expected no chat tasks in results, got category=%s title=%q", r.Category, r.Title)
		}
	}

	// Search for "auth" - should find 1
	authResults, err := repo.SearchByTitle(ctx, "default", "auth")
	if err != nil {
		t.Fatalf("SearchByTitle auth: %v", err)
	}
	if len(authResults) != 1 {
		t.Fatalf("expected 1 result for 'auth', got %d", len(authResults))
	}
	if authResults[0].Title != "Add authentication" {
		t.Errorf("expected 'Add authentication', got %q", authResults[0].Title)
	}

	// Search for non-existent title - should find 0
	noResults, err := repo.SearchByTitle(ctx, "default", "nonexistent")
	if err != nil {
		t.Fatalf("SearchByTitle nonexistent: %v", err)
	}
	if len(noResults) != 0 {
		t.Errorf("expected 0 results for 'nonexistent', got %d", len(noResults))
	}
}

func TestTaskRepo_SearchByTitle_ExactMatchFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create tasks where one has an exact title match
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Fix something with login", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "login", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"})
	repo.Create(ctx, &models.Task{ProjectID: "default", Title: "Login page fixes", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "p"})

	results, err := repo.SearchByTitle(ctx, "default", "login")
	if err != nil {
		t.Fatalf("SearchByTitle: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
	// Exact match should be first (case-insensitive)
	if results[0].Title != "login" {
		t.Errorf("expected exact match 'login' first, got %q", results[0].Title)
	}
}

func TestTaskRepo_ListStaleQueuedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a stale queued task (active + queued, old updated_at)
	staleTask := &models.Task{
		ProjectID: "default",
		Title:     "Stale Queued",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, staleTask); err != nil {
		t.Fatalf("Create stale task: %v", err)
	}
	if err := repo.UpdateStatus(ctx, staleTask.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus stale task: %v", err)
	}
	// Set updated_at to 15 minutes ago
	_, err := db.ExecContext(ctx,
		`UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, staleTask.ID)
	if err != nil {
		t.Fatalf("Set stale updated_at: %v", err)
	}

	// Create a recent queued task (should NOT be found)
	recentTask := &models.Task{
		ProjectID: "default",
		Title:     "Recent Queued",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, recentTask); err != nil {
		t.Fatalf("Create recent task: %v", err)
	}
	if err := repo.UpdateStatus(ctx, recentTask.ID, models.StatusQueued); err != nil {
		t.Fatalf("UpdateStatus recent task: %v", err)
	}

	// Create a pending task (should NOT be found — wrong status)
	pendingTask := &models.Task{
		ProjectID: "default",
		Title:     "Pending Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := repo.Create(ctx, pendingTask); err != nil {
		t.Fatalf("Create pending task: %v", err)
	}

	// Query for stale tasks with 10-minute threshold
	staleTasks, err := repo.ListStaleQueuedTasks(ctx, 10*time.Minute)
	if err != nil {
		t.Fatalf("ListStaleQueuedTasks: %v", err)
	}

	if len(staleTasks) != 1 {
		t.Fatalf("expected 1 stale queued task, got %d", len(staleTasks))
	}
	if staleTasks[0].ID != staleTask.ID {
		t.Errorf("expected stale task ID=%s, got %s", staleTask.ID, staleTasks[0].ID)
	}
}

func TestTaskRepo_AdmissionOwnershipExcludesSchedulerRecoveryAndClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	inputRepo := NewThreadInputRepo(db)

	withRunningFollowup := &models.Task{ProjectID: "default", Title: "running follow-up", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original"}
	if err := taskRepo.Create(ctx, withRunningFollowup); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.UpdateStatus(ctx, withRunningFollowup.ID, models.StatusQueued); err != nil {
		t.Fatal(err)
	}
	active := &models.Execution{TaskID: withRunningFollowup.ID, Status: models.ExecRunning, PromptSent: "follow-up", IsFollowup: true}
	if err := execRepo.Create(ctx, active); err != nil {
		t.Fatal(err)
	}

	withPendingInput := &models.Task{ProjectID: "default", Title: "pending input", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original"}
	if err := taskRepo.Create(ctx, withPendingInput); err != nil {
		t.Fatal(err)
	}
	prior := &models.Execution{TaskID: withPendingInput.ID, Status: models.ExecCompleted, PromptSent: "prior turn"}
	if err := execRepo.Create(ctx, prior); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.UpdateStatus(ctx, withPendingInput.ID, models.StatusQueued); err != nil {
		t.Fatal(err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: "default", TaskID: withPendingInput.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued follow-up"}
	if err := inputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatal(err)
	}

	withReservation := &models.Task{ProjectID: "default", Title: "reserved run", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original"}
	if err := taskRepo.Create(ctx, withReservation); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.UpdateStatus(ctx, withReservation.ID, models.StatusQueued); err != nil {
		t.Fatal(err)
	}
	reservationFixture := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO automations (id, project_id, stable_key, name) VALUES ('admission-auto', 'default', 'admission-auto', 'Admission')`, nil},
		{`INSERT INTO automation_versions (id, project_id, automation_id, version, adapter_key) VALUES ('admission-version', 'default', 'admission-auto', 1, 'test')`, nil},
		{`INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role) VALUES ('admission-node', 'default', 'admission-auto', 'admission-version', 'trigger', 'Trigger', 'trigger', 'trigger')`, nil},
		{`INSERT INTO automation_invocations (id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key) VALUES ('admission-invocation', 'default', 'admission-auto', 'admission-version', 'admission-node', 'schedule', 'fixture', 'fixture')`, nil},
		{`INSERT INTO automation_dispatch_outbox (id, invocation_id, task_id) VALUES ('admission-dispatch', 'admission-invocation', ?)`, []any{withReservation.ID}},
		{`INSERT INTO automation_task_run_reservations (task_id, dispatch_id, project_id) VALUES (?, 'admission-dispatch', 'default')`, []any{withReservation.ID}},
	}
	for _, statement := range reservationFixture {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("create reservation fixture: %v", err)
		}
	}

	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id IN (?, ?, ?)`, withRunningFollowup.ID, withPendingInput.ID, withReservation.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := taskRepo.ListStaleQueuedTasks(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("admission-owned tasks must not be stale-recovery candidates: %#v", stale)
	}

	if err := taskRepo.UpdateStatus(ctx, withRunningFollowup.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := taskRepo.ClaimTaskForDispatch(ctx, withRunningFollowup.ID); err != nil || claimed {
		t.Fatalf("atomic dispatch claim must reject a running follow-up: claimed=%v err=%v", claimed, err)
	}
	if err := taskRepo.UpdateStatus(ctx, withPendingInput.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := taskRepo.ClaimTaskForDispatch(ctx, withPendingInput.ID); err != nil || claimed {
		t.Fatalf("atomic dispatch claim must reject a pending FIFO input: claimed=%v err=%v", claimed, err)
	}
	if err := taskRepo.UpdateStatus(ctx, withReservation.ID, models.StatusPending); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := taskRepo.ClaimTaskForDispatch(ctx, withReservation.ID); err != nil || claimed {
		t.Fatalf("atomic dispatch claim must reject an Automation reservation: claimed=%v err=%v", claimed, err)
	}
}

func TestTaskRepo_FirstTurnQueuedInputStillAllowsSchedulerAndAtomicDispatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := NewTaskRepo(db, nil)
	inputRepo := NewThreadInputRepo(db)
	task := &models.Task{ProjectID: "default", Title: "first turn with queued follow-up", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original prompt"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: task.ProjectID, TaskID: task.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "follow-up"}
	if err := inputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatal(err)
	}

	listed, err := taskRepo.ListActivePendingAdmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != task.ID {
		t.Fatalf("scheduler must retain a fresh first turn with queued follow-ups: %#v", listed)
	}
	claim, claimed, err := taskRepo.ClaimTaskForDispatch(ctx, task.ID)
	if err != nil || !claimed {
		t.Fatalf("atomic dispatch must claim the original first turn: claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	pending, err := inputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("dispatch claim must preserve queued FIFO input: %#v err=%v", pending, err)
	}
}

func TestTaskRepo_ReclaimStaleQueuedTaskRejectsOwnerAddedAfterListing(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: "default", Title: "stale snapshot race", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = datetime('now', '-15 minutes') WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := taskRepo.ListStaleQueuedTasks(ctx, 10*time.Minute)
	if err != nil || len(stale) != 1 || stale[0].ID != task.ID {
		t.Fatalf("expected ownerless stale snapshot: %#v err=%v", stale, err)
	}

	active := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "follow-up admitted after listing", IsFollowup: true}
	if err := NewExecutionRepo(db).Create(ctx, active); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := taskRepo.ReclaimStaleQueuedTask(ctx, task.ID, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed {
		t.Fatal("stale reclaim must lose when ownership appears after listing")
	}
	stored, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.StatusQueued {
		t.Fatalf("owner-added race changed task status to %s", stored.Status)
	}
}

func TestTaskRepo_ListTasksForDiscovery_UsesCompactProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	parent := &models.Task{
		ProjectID: "default",
		Title:     "Discovery parent",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusRunning,
		Prompt:    "parent prompt",
	}
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	largePrompt := strings.Repeat("p", 64*1024)
	largeChainConfig := `{"payload":"` + strings.Repeat("c", 16*1024) + `"}`
	largeSwarmConfig := `{"payload":"` + strings.Repeat("s", 16*1024) + `"}`
	child := &models.Task{
		ProjectID:    "default",
		Title:        "Discovery compact worker",
		Category:     models.CategoryActive,
		Priority:     4,
		Status:       models.StatusRunning,
		Prompt:       largePrompt,
		ParentTaskID: &parent.ID,
		ChainConfig:  largeChainConfig,
		SwarmRole:    models.SwarmRoleWorker,
		SwarmConfig:  largeSwarmConfig,
	}
	if err := repo.Create(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	tasks, total, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Query: child.Title})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery: %v", err)
	}
	if total != 1 || len(tasks) != 1 {
		t.Fatalf("expected one compact discovery result, got total=%d len=%d", total, len(tasks))
	}
	got := tasks[0]
	if got.ID != child.ID || got.Title != child.Title || got.Category != child.Category || got.Status != child.Status || got.Priority != child.Priority || got.UpdatedAt.IsZero() {
		t.Fatalf("discovery fields changed: %+v", got)
	}
	if got.ParentTaskID == nil || *got.ParentTaskID != parent.ID || got.SwarmRole != models.SwarmRoleWorker {
		t.Fatalf("optional discovery fields changed: %+v", got)
	}
	if got.Prompt != "" || got.ChainConfig != "" || got.SwarmConfig != "" {
		t.Fatalf("discovery materialized unreturned payloads: prompt=%d chain_config=%d swarm_config=%d", len(got.Prompt), len(got.ChainConfig), len(got.SwarmConfig))
	}
}

func TestTaskRepo_ListTasksForDiscovery(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Second project for isolation checks.
	project2 := &models.Project{Name: "Discovery Project 2"}
	if err := projectRepo.Create(ctx, project2); err != nil {
		t.Fatalf("Create project2: %v", err)
	}

	mk := func(projectID, title string, category models.TaskCategory, status models.TaskStatus) *models.Task {
		task := &models.Task{ProjectID: projectID, Title: title, Category: category, Status: status, Prompt: "p"}
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("Create %q: %v", title, err)
		}
		if err := repo.UpdateStatus(ctx, task.ID, status); err != nil {
			t.Fatalf("UpdateStatus %q: %v", title, err)
		}
		return task
	}

	exact := mk("default", "Deploy pipeline", models.CategoryActive, models.StatusPending)
	mk("default", "Deploy pipeline docs", models.CategoryBacklog, models.StatusPending)
	mk("default", "Refactor deploy hooks", models.CategoryActive, models.StatusRunning)
	mk("default", "Unrelated cleanup", models.CategoryCompleted, models.StatusCompleted)
	// Internal chat row must never appear in discovery.
	mk("default", "Deploy pipeline chat", models.CategoryChat, models.StatusCompleted)
	// Other project row must never appear.
	mk(project2.ID, "Deploy pipeline elsewhere", models.CategoryActive, models.StatusPending)

	// Partial title matching: substring "deploy" matches three non-chat rows in
	// this project (chat + other-project rows excluded).
	partial, partialTotal, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Query: "deploy"})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery partial query: %v", err)
	}
	if partialTotal != 3 || len(partial) != 3 {
		t.Fatalf("expected 3 partial matches, got total=%d len=%d", partialTotal, len(partial))
	}
	prefixTitles := map[string]bool{
		"Deploy pipeline":      false,
		"Deploy pipeline docs": false,
	}
	for _, task := range partial[:2] {
		if _, ok := prefixTitles[task.Title]; !ok {
			t.Fatalf("title relevance ordering changed: prefix result %q was not expected", task.Title)
		}
		prefixTitles[task.Title] = true
	}
	if !prefixTitles["Deploy pipeline"] || !prefixTitles["Deploy pipeline docs"] || partial[2].Title != "Refactor deploy hooks" {
		t.Fatalf("title relevance ordering changed: %#v", partial)
	}
	for _, task := range partial {
		if task.Category == models.CategoryChat {
			t.Fatalf("chat row leaked into discovery: %q", task.Title)
		}
		if task.Title == "Deploy pipeline elsewhere" {
			t.Fatalf("cross-project row leaked: %q", task.Title)
		}
	}

	// Exact title query ranks the exact match ahead of prefix/contains matches.
	tasks, total, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Query: "Deploy pipeline"})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery exact query: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 matches for 'Deploy pipeline', got %d", total)
	}
	if len(tasks) == 0 || tasks[0].ID != exact.ID {
		t.Fatalf("expected exact title match first, got %+v", tasks)
	}

	// Category filter.
	activeTasks, activeTotal, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Category: string(models.CategoryActive)})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery category: %v", err)
	}
	if activeTotal != 2 || len(activeTasks) != 2 {
		t.Fatalf("expected 2 active tasks, got total=%d len=%d", activeTotal, len(activeTasks))
	}
	for _, task := range activeTasks {
		if task.Category != models.CategoryActive {
			t.Fatalf("unexpected category %q", task.Category)
		}
	}

	// Status filter.
	runningTasks, runningTotal, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Status: string(models.StatusRunning)})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery status: %v", err)
	}
	if runningTotal != 1 || len(runningTasks) != 1 || runningTasks[0].Title != "Refactor deploy hooks" {
		t.Fatalf("expected single running task, got total=%d len=%d", runningTotal, len(runningTasks))
	}

	// Deterministic ordering without query: updated_at DESC, id ASC.
	all, allTotal, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery all: %v", err)
	}
	if allTotal != 4 {
		t.Fatalf("expected 4 non-chat tasks, got %d", allTotal)
	}
	for i := 1; i < len(all); i++ {
		prev, cur := all[i-1], all[i]
		if cur.UpdatedAt.After(prev.UpdatedAt) {
			t.Fatalf("results not ordered by updated_at DESC at index %d", i)
		}
		if cur.UpdatedAt.Equal(prev.UpdatedAt) && cur.ID < prev.ID {
			t.Fatalf("ties not ordered by id ASC at index %d", i)
		}
	}

	// Result bounds + pagination.
	page1, page1Total, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery page1: %v", err)
	}
	if page1Total != 4 || len(page1) != 2 {
		t.Fatalf("expected page1 total=4 len=2, got total=%d len=%d", page1Total, len(page1))
	}
	page2, _, err := repo.ListTasksForDiscovery(ctx, "default", TaskDiscoveryFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected page2 len=2, got %d", len(page2))
	}
	seen := map[string]bool{}
	for _, task := range append(append([]models.Task{}, page1...), page2...) {
		if seen[task.ID] {
			t.Fatalf("pagination returned duplicate task %q", task.ID)
		}
		seen[task.ID] = true
	}
}

func TestTaskRepo_ListTasksForDiscoveryOrdersIdenticalTimestampsByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Identical timestamp discovery project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	var expectedIDs []string
	for _, title := range []string{"Tie task A", "Tie task B", "Tie task C"} {
		task := &models.Task{
			ProjectID: project.ID,
			Title:     title,
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "p",
		}
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		expectedIDs = append(expectedIDs, task.ID)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE project_id = ?`, time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC), project.ID); err != nil {
		t.Fatalf("set identical timestamps: %v", err)
	}

	got, total, err := repo.ListTasksForDiscovery(ctx, project.ID, TaskDiscoveryFilter{Limit: 20})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery: %v", err)
	}
	if total != len(expectedIDs) || len(got) != len(expectedIDs) {
		t.Fatalf("discovery result size = total %d len %d, want %d", total, len(got), len(expectedIDs))
	}

	sort.Strings(expectedIDs)
	for i, task := range got {
		if task.ID != expectedIDs[i] {
			t.Fatalf("identical-timestamp result %d has ID %q, want ascending ID %q; result=%#v", i, task.ID, expectedIDs[i], got)
		}
		if !task.UpdatedAt.Equal(time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)) {
			t.Fatalf("identical-timestamp result %d has updated_at %s", i, task.UpdatedAt)
		}
	}
}

func TestTaskRepo_ListTasksForDiscoveryOrdersIdenticalTimestampsWithinTitleRelevance(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Title relevance tie discovery project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	create := func(title string) string {
		t.Helper()
		task := &models.Task{
			ProjectID: project.ID,
			Title:     title,
			Category:  models.CategoryActive,
			Status:    models.StatusPending,
			Prompt:    "p",
		}
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return task.ID
	}

	exactID := create("Deploy")
	prefixIDs := []string{create("Deploy alpha"), create("Deploy beta")}
	containsIDs := []string{create("Refactor deploy"), create("Cleanup deploy")}
	tieTime := time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE project_id = ?`, tieTime, project.ID); err != nil {
		t.Fatalf("set identical title relevance timestamps: %v", err)
	}

	got, total, err := repo.ListTasksForDiscovery(ctx, project.ID, TaskDiscoveryFilter{Query: "deploy", Limit: 20})
	if err != nil {
		t.Fatalf("ListTasksForDiscovery: %v", err)
	}
	if total != 5 || len(got) != 5 {
		t.Fatalf("title relevance result size = total %d len %d, want 5", total, len(got))
	}

	sort.Strings(prefixIDs)
	sort.Strings(containsIDs)
	expectedIDs := append([]string{exactID}, prefixIDs...)
	expectedIDs = append(expectedIDs, containsIDs...)
	for i, task := range got {
		if task.ID != expectedIDs[i] {
			t.Fatalf("title relevance result %d has ID %q, want %q; result=%#v", i, task.ID, expectedIDs[i], got)
		}
		if !task.UpdatedAt.Equal(tieTime) {
			t.Fatalf("title relevance result %d has updated_at %s", i, task.UpdatedAt)
		}
	}
}

func TestTaskRepo_ListWithSchedulesByProject_UsesCalendarProjection(t *testing.T) {
	if got, want := scheduleCalendarTaskSelectColumns, "t.id, t.project_id, t.title, t.category, t.status"; got != want {
		t.Fatalf("calendar projection changed: got %q, want %q", got, want)
	}
	for _, forbidden := range []string{"prompt", "chain_config", "swarm_config"} {
		if projectionContainsColumn(scheduleCalendarTaskSelectColumns, forbidden) {
			t.Fatalf("calendar projection must not select unused task column %q: %s", forbidden, scheduleCalendarTaskSelectColumns)
		}
	}

	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)
	ctx := context.Background()

	largePrompt := strings.Repeat("p", 64*1024)
	largeChainConfig := `{"payload":"` + strings.Repeat("c", 16*1024) + `"}`
	largeSwarmConfig := `{"payload":"` + strings.Repeat("s", 16*1024) + `"}`
	scheduledWithSchedule := &models.Task{
		ProjectID:     "default",
		Title:         "Calendar bounded scheduled task",
		Category:      models.CategoryActive,
		Priority:      4,
		Status:        models.StatusPending,
		Prompt:        largePrompt,
		ChainConfig:   largeChainConfig,
		SwarmConfig:   largeSwarmConfig,
		SwarmRole:     models.SwarmRoleWorker,
		SwarmSequence: 7,
	}
	if err := repo.Create(ctx, scheduledWithSchedule); err != nil {
		t.Fatalf("create scheduled task: %v", err)
	}

	runAt := time.Date(2026, 8, 9, 14, 30, 0, 0, time.UTC)
	nextRun := runAt.Add(48 * time.Hour)
	lastRun := runAt.Add(-48 * time.Hour)
	schedule := &models.Schedule{
		TaskID:              scheduledWithSchedule.ID,
		RunAt:               runAt,
		RepeatType:          models.RepeatDaily,
		RepeatInterval:      2,
		Enabled:             false,
		ClearContextOnStart: true,
		NextRun:             &nextRun,
	}
	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schedules SET last_run = ? WHERE id = ?`, lastRun, schedule.ID); err != nil {
		t.Fatalf("set schedule last_run: %v", err)
	}
	insertAutomationScheduleOwner(t, ctx, db, "default", schedule.ID, "Automation Node Override")

	scheduledWithoutSchedule := &models.Task{
		ProjectID:   "default",
		Title:       "Calendar scheduled category without schedule",
		Category:    models.CategoryScheduled,
		Priority:    1,
		Status:      models.StatusPending,
		Prompt:      largePrompt,
		ChainConfig: largeChainConfig,
		SwarmConfig: largeSwarmConfig,
	}
	if err := repo.Create(ctx, scheduledWithoutSchedule); err != nil {
		t.Fatalf("create scheduled category task: %v", err)
	}

	results, err := repo.ListWithSchedulesByProject(ctx, "default")
	if err != nil {
		t.Fatalf("ListWithSchedulesByProject: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected two calendar rows, got %d: %#v", len(results), results)
	}

	byID := make(map[string]TaskWithSchedule, len(results))
	for _, result := range results {
		byID[result.Task.ID] = result
		if result.Task.Prompt != "" || result.Task.ChainConfig != "" || result.Task.SwarmConfig != "" {
			t.Fatalf("calendar projection materialized unused payloads for task %s: prompt=%d chain_config=%d swarm_config=%d", result.Task.ID, len(result.Task.Prompt), len(result.Task.ChainConfig), len(result.Task.SwarmConfig))
		}
	}

	withSchedule := byID[scheduledWithSchedule.ID]
	if withSchedule.Task.ProjectID != "default" || withSchedule.Task.Title != scheduledWithSchedule.Title || withSchedule.Task.Category != models.CategoryActive || withSchedule.Task.Status != models.StatusPending {
		t.Fatalf("calendar task fields changed: %+v", withSchedule.Task)
	}
	if withSchedule.AutomationScheduleName != "Automation Node Override" {
		t.Fatalf("expected automation schedule name override, got %q", withSchedule.AutomationScheduleName)
	}
	if withSchedule.Schedule == nil {
		t.Fatal("expected schedule metadata")
	}
	if withSchedule.Schedule.ID != schedule.ID || withSchedule.Schedule.TaskID != scheduledWithSchedule.ID || !withSchedule.Schedule.RunAt.Equal(runAt) {
		t.Fatalf("schedule identity/time fields changed: %+v", withSchedule.Schedule)
	}
	if withSchedule.Schedule.RepeatType != models.RepeatDaily || withSchedule.Schedule.RepeatInterval != 2 || withSchedule.Schedule.Enabled || !withSchedule.Schedule.ClearContextOnStart {
		t.Fatalf("schedule repeat/enabled fields changed: %+v", withSchedule.Schedule)
	}
	if withSchedule.Schedule.NextRun == nil || !withSchedule.Schedule.NextRun.Equal(nextRun) {
		t.Fatalf("schedule next_run changed: %+v", withSchedule.Schedule.NextRun)
	}
	if withSchedule.Schedule.LastRun == nil || !withSchedule.Schedule.LastRun.Equal(lastRun) {
		t.Fatalf("schedule last_run changed: %+v", withSchedule.Schedule.LastRun)
	}

	withoutSchedule := byID[scheduledWithoutSchedule.ID]
	if withoutSchedule.Task.Title != scheduledWithoutSchedule.Title || withoutSchedule.Schedule != nil || withoutSchedule.AutomationScheduleName != "" {
		t.Fatalf("scheduled category row without schedule changed: %+v", withoutSchedule)
	}
}

func TestTaskRepoCoveragePriorityTagsWorktreeOriginsAndDescendants(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTaskRepo(db, nil)
	ctx := context.Background()

	parent := &models.Task{ProjectID: "default", Title: "Coverage parent", Category: models.CategoryBacklog, Status: models.StatusBlocked, Prompt: "parent", Priority: 5, SwarmRole: models.SwarmRoleParent, Tag: models.TagFeature}
	high := &models.Task{ProjectID: "default", Title: "Coverage high", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "high", Priority: 5, Tag: models.TagBug}
	low := &models.Task{ProjectID: "default", Title: "Coverage low", Category: models.CategoryBacklog, Status: models.StatusFailed, Prompt: "low", Priority: 1, Tag: models.TagFeature}
	active := &models.Task{ProjectID: "default", Title: "Coverage active", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "active", Priority: 3, Tag: models.TagFeature}
	chatRunning := &models.Task{ProjectID: "default", Title: "Coverage chat running", Category: models.CategoryChat, Status: models.StatusRunning, Prompt: "chat"}
	chatDone := &models.Task{ProjectID: "default", Title: "Coverage chat done", Category: models.CategoryChat, Status: models.StatusCompleted, Prompt: "chat done"}
	for _, task := range []*models.Task{parent, high, low, active, chatRunning, chatDone} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create %s: %v", task.Title, err)
		}
	}
	child := &models.Task{ProjectID: "default", Title: "Coverage child", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "child", ParentTaskID: &parent.ID}
	grandchild := &models.Task{ProjectID: "default", Title: "Coverage grandchild", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "grandchild", ParentTaskID: &child.ID}
	for _, task := range []*models.Task{child, grandchild} {
		if err := repo.Create(ctx, task); err != nil {
			t.Fatalf("create descendant %s: %v", task.Title, err)
		}
	}

	backlog, err := repo.ListBacklogByPriority(ctx, "default", 0)
	if err != nil {
		t.Fatalf("ListBacklogByPriority all: %v", err)
	}
	if len(backlog) != 3 || backlog[0].Priority < backlog[1].Priority {
		t.Fatalf("unexpected backlog priority list: %#v", backlog)
	}
	priorityFive, err := repo.ListBacklogByPriority(ctx, "default", 5)
	if err != nil {
		t.Fatalf("ListBacklogByPriority priority: %v", err)
	}
	if len(priorityFive) != 2 {
		t.Fatalf("expected two priority-five backlog tasks, got %#v", priorityFive)
	}
	counts, err := repo.CountBacklogByPriority(ctx, "default")
	if err != nil {
		t.Fatalf("CountBacklogByPriority: %v", err)
	}
	if counts[5] != 2 || counts[1] != 1 {
		t.Fatalf("priority counts = %#v", counts)
	}

	tagged, err := repo.ListByTags(ctx, []models.TaskTag{models.TagFeature}, "default", models.CategoryBacklog, 2, "")
	if err != nil {
		t.Fatalf("ListByTags: %v", err)
	}
	if len(tagged) != 1 || tagged[0].ID != parent.ID {
		t.Fatalf("expected only feature backlog task with priority >=2, got %#v", tagged)
	}
	activeTagged, err := repo.ListByTags(ctx, nil, "default", models.CategoryActive, 0, models.StatusPending)
	if err != nil {
		t.Fatalf("ListByTags status/category: %v", err)
	}
	if len(activeTagged) != 2 {
		t.Fatalf("expected active pending tasks, got %#v", activeTagged)
	}

	runningChatIDs, err := repo.ListRunningChatTaskIDs(ctx, "default")
	if err != nil {
		t.Fatalf("ListRunningChatTaskIDs: %v", err)
	}
	if !reflect.DeepEqual(runningChatIDs, []string{chatRunning.ID}) {
		t.Fatalf("running chat IDs = %#v", runningChatIDs)
	}

	if err := repo.UpdateSwarmFields(ctx, child.ID, models.SwarmRoleWorker, "blocked", `{"role":"worker"}`, 7); err != nil {
		t.Fatalf("UpdateSwarmFields: %v", err)
	}
	defaultAgentID := defaultAgentConfigID(t, ctx, db)
	if err := repo.UpdateAgentID(ctx, child.ID, defaultAgentID); err != nil {
		t.Fatalf("UpdateAgentID set: %v", err)
	}
	if err := repo.UpdateAgentID(ctx, child.ID, ""); err != nil {
		t.Fatalf("UpdateAgentID clear: %v", err)
	}
	if err := repo.UpdateWorktreeInfo(ctx, child.ID, "/tmp/worktree", "task/branch"); err != nil {
		t.Fatalf("UpdateWorktreeInfo: %v", err)
	}
	if err := repo.UpdateMergeStatus(ctx, child.ID, models.MergeStatusMerged); err != nil {
		t.Fatalf("UpdateMergeStatus: %v", err)
	}
	if err := repo.UpdateAutoMerge(ctx, child.ID, true, "main"); err != nil {
		t.Fatalf("UpdateAutoMerge: %v", err)
	}
	if err := repo.UpdateLineage(ctx, child.ID, "main", strings.Repeat("a", 40), 2); err != nil {
		t.Fatalf("UpdateLineage: %v", err)
	}
	if err := repo.UpdateTelegramOrigin(ctx, child.ID, 123); err != nil {
		t.Fatalf("UpdateTelegramOrigin: %v", err)
	}
	if err := repo.UpdateSlackOrigin(ctx, child.ID); err != nil {
		t.Fatalf("UpdateSlackOrigin: %v", err)
	}
	if err := repo.UpdateEmailOrigin(ctx, child.ID); err != nil {
		t.Fatalf("UpdateEmailOrigin: %v", err)
	}
	if err := repo.UpdateDiscordOrigin(ctx, child.ID); err != nil {
		t.Fatalf("UpdateDiscordOrigin: %v", err)
	}

	updated, err := repo.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetByID updated child: %v", err)
	}
	if updated.AgentID != nil || updated.WorktreePath != "/tmp/worktree" || updated.WorktreeBranch != "task/branch" ||
		updated.MergeStatus != models.MergeStatusMerged || !updated.AutoMerge || updated.MergeTargetBranch != "main" ||
		updated.BaseBranch != "main" || updated.LineageDepth != 2 || updated.CreatedVia != models.TaskOriginDiscord {
		t.Fatalf("updated child = %#v", updated)
	}
	if err := repo.ClearWorktreeInfo(ctx, child.ID); err != nil {
		t.Fatalf("ClearWorktreeInfo: %v", err)
	}
	cleared, err := repo.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetByID cleared child: %v", err)
	}
	if cleared.WorktreePath != "" || cleared.WorktreeBranch != "" {
		t.Fatalf("worktree info not cleared: %#v", cleared)
	}

	hasActiveDescendant, err := repo.HasNonTerminalDescendants(ctx, parent.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants active: %v", err)
	}
	if !hasActiveDescendant {
		t.Fatal("expected active descendant")
	}
	if err := repo.UpdateStatus(ctx, child.ID, models.StatusCompleted); err != nil {
		t.Fatalf("complete child: %v", err)
	}
	hasActiveDescendant, err = repo.HasNonTerminalDescendants(ctx, parent.ID)
	if err != nil {
		t.Fatalf("HasNonTerminalDescendants terminal: %v", err)
	}
	if hasActiveDescendant {
		t.Fatal("expected only terminal descendants")
	}

	foundChild, err := repo.FindBlockedChildByParent(ctx, parent.ID)
	if err != nil {
		t.Fatalf("FindBlockedChildByParent before blocked: %v", err)
	}
	if foundChild != nil {
		t.Fatalf("completed child should not be returned as blocked: %#v", foundChild)
	}
	if err := repo.UpdateStatus(ctx, child.ID, models.StatusBlocked); err != nil {
		t.Fatalf("block child: %v", err)
	}
	foundChild, err = repo.FindBlockedChildByParent(ctx, parent.ID)
	if err != nil {
		t.Fatalf("FindBlockedChildByParent: %v", err)
	}
	if foundChild == nil || foundChild.ID != child.ID {
		t.Fatalf("blocked child = %#v", foundChild)
	}
	if err := repo.DeleteBlockedChildrenByParent(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteBlockedChildrenByParent: %v", err)
	}
	foundChild, err = repo.FindBlockedChildByParent(ctx, parent.ID)
	if err != nil {
		t.Fatalf("FindBlockedChildByParent after delete: %v", err)
	}
	if foundChild != nil {
		t.Fatalf("blocked child still present: %#v", foundChild)
	}
}

func insertAutomationScheduleOwner(t *testing.T, ctx context.Context, db *sql.DB, projectID, scheduleID, nodeName string) {
	t.Helper()
	automationID := "calendar-automation-" + scheduleID
	versionID := "calendar-version-" + scheduleID
	nodeID := "calendar-node-" + scheduleID
	if _, err := db.ExecContext(ctx, `INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state) VALUES (?, ?, ?, ?, 'active')`, automationID, projectID, automationID, "Calendar Automation"); err != nil {
		t.Fatalf("insert automation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key) VALUES (?, ?, ?, 1, 'published', 'manual', 'calendar-test')`, versionID, projectID, automationID); err != nil {
		t.Fatalf("insert automation version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_nodes (id, project_id, automation_id, version_id, node_key, name, node_type, role, config_json) VALUES (?, ?, ?, ?, 'schedule-trigger', ?, 'trigger', 'schedule', '{}')`, nodeID, projectID, automationID, versionID, nodeName); err != nil {
		t.Fatalf("insert automation node: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_trigger_owners (schedule_id, project_id, automation_id, version_id, node_id, ownership_state) VALUES (?, ?, ?, ?, ?, 'active')`, scheduleID, projectID, automationID, versionID, nodeID); err != nil {
		t.Fatalf("insert automation schedule owner: %v", err)
	}
}
