package sqlite_test

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"strings"
	"testing"
	"time"
)

func TestChannelContextNormalizationQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	taskRepo := sqlite.NewStores(db, nil).Tasks
	taskSvc := service.NewTaskService(taskRepo, nil, nil)

	backlog := &models.Task{
		ProjectID: "default",
		Title:     "Existing backlog task",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
		Prompt:    "backlog prompt",
	}
	failed := &models.Task{
		ProjectID: "default",
		Title:     "Active failed task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusFailed,
		Prompt:    "failed prompt",
	}
	cancelled := &models.Task{
		ProjectID: "default",
		Title:     "Active cancelled task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusCancelled,
		Prompt:    "cancelled prompt",
	}
	running := &models.Task{
		ProjectID: "default",
		Title:     "Active running task",
		Category:  models.CategoryActive,
		Priority:  2,
		Status:    models.StatusRunning,
		Prompt:    "running prompt",
	}
	for _, task := range []*models.Task{backlog, failed, cancelled, running} {
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", task.Title, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET display_order = CASE title
		WHEN 'Existing backlog task' THEN 5
		WHEN 'Active running task' THEN 20
		WHEN 'Active failed task' THEN 100
		WHEN 'Active cancelled task' THEN 101
	END WHERE project_id = ?`, "default"); err != nil {
		t.Fatalf("set deterministic display order: %v", err)
	}
	executionRepo := sqlite.NewStores(db, nil).Executions
	queuedExecutions := make([]*models.Execution, 0, 2)
	for _, task := range []*models.Task{failed, cancelled} {
		execution := &models.Execution{TaskID: task.ID, Status: models.ExecQueued, PromptSent: task.Prompt}
		if err := executionRepo.Create(ctx, execution); err != nil {
			t.Fatalf("create queued execution for %q: %v", task.Title, err)
		}
		queuedExecutions = append(queuedExecutions, execution)
	}

	counter.Reset()
	counter.SetEnabled(true)
	got, err := taskSvc.ListChatContextByProject(ctx, "default")
	counter.SetEnabled(false)
	statements := counter.Statements()
	compactReads := 0
	categoryUpdates := 0
	executionUpdates := 0
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		switch {
		case strings.Contains(normalized, "substr(t.prompt, 1, 501)"):
			compactReads++
		case strings.Contains(normalized, "update tasks") && strings.Contains(normalized, "returning id, title"):
			categoryUpdates++
		case strings.Contains(normalized, "update executions") && strings.Contains(normalized, "task_id in (?,?)"):
			executionUpdates++
		}
		if strings.Contains(normalized, "where id = ? and project_id = ? and category = ?") {
			t.Fatalf("compact normalization used a per-task category update: %s", statement)
		}
		if strings.Contains(normalized, "where task_id = ? and status = 'queued'") {
			t.Fatalf("compact normalization used a per-task execution update: %s", statement)
		}
	}
	if compactReads != 2 || categoryUpdates != 1 || executionUpdates != 1 {
		t.Fatalf("compact terminal normalization statements = %#v, want two compact reads, one batch category update, and one batch execution update", statements)
	}
	if err != nil {
		t.Fatalf("ListChatContextByProject: %v", err)
	}
	wantOrder := []string{"Existing backlog task", "Active failed task", "Active cancelled task", "Active running task"}
	if len(got) != len(wantOrder) {
		t.Fatalf("compact context tasks = %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Title != want {
			t.Fatalf("compact context order[%d] = %q, want %q", i, got[i].Title, want)
		}
	}
	byID := make(map[string]models.Task, len(got))
	for _, task := range got {
		byID[task.ID] = task
	}
	for _, task := range []*models.Task{failed, cancelled} {
		if got := byID[task.ID].Category; got != models.CategoryBacklog {
			t.Errorf("projected terminal task %q category = %q, want backlog", task.Title, got)
		}
		persisted, err := taskRepo.GetByID(ctx, task.ID)
		if err != nil {
			t.Fatalf("reload terminal task %q: %v", task.Title, err)
		}
		if persisted.Category != models.CategoryBacklog {
			t.Errorf("persisted terminal task %q category = %q, want backlog", task.Title, persisted.Category)
		}
	}
	for _, execution := range queuedExecutions {
		persisted, err := executionRepo.GetByID(ctx, execution.ID)
		if err != nil {
			t.Fatalf("reload queued execution %q: %v", execution.ID, err)
		}
		if persisted.Status != models.ExecCancelled || persisted.ErrorMessage != "Task left the reserved running lane" {
			t.Fatalf("queued execution %q = status %q/error %q, want cancelled/reserved-lane error", execution.ID, persisted.Status, persisted.ErrorMessage)
		}
	}
	if got := byID[running.ID].Category; got != models.CategoryActive {
		t.Errorf("projected running task category = %q, want active", got)
	}
	fullTasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("reload full task context: %v", err)
	}
	wantContext := service.BuildChatContextWithAgentDefinitions(fullTasks, nil, nil, nil, time.Unix(0, 0))
	gotContext := service.BuildChatContextWithAgentDefinitions(got, nil, nil, nil, time.Unix(0, 0))
	if gotContext != wantContext {
		t.Fatalf("compact normalized context changed model-facing bytes\nwant:\n%s\ngot:\n%s", wantContext, gotContext)
	}
	for _, statement := range counter.Statements() {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "select id, project_id, title, category, priority, status, prompt") {
			t.Fatalf("compact normalization hydrated the full task projection: %s", statement)
		}
	}
}

func channelAgentConfigStatements(statements []string) []string {
	var modelStatements []string
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.Contains(normalized, "from agent_configs") {
			modelStatements = append(modelStatements, normalized)
		}
	}
	return modelStatements
}

func assertChannelCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	compact := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if projection == "select id, name, provider, model, is_default, auth_method" {
			compact++
			continue
		}
		if !strings.Contains(statement, "where id = ?") && !strings.Contains(statement, "where a.id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if compact != 1 {
		t.Fatalf("compact chat selection query count = %d, statements = %#v", compact, statements)
	}
}

func assertChannelVisionCompactStatement(t *testing.T, statements []string) {
	t.Helper()
	vision := 0
	for _, statement := range statements {
		projection := strings.Split(statement, " from agent_configs ")[0]
		if strings.HasPrefix(projection, "select id, name, provider, model, auth_method, is_default,") {
			vision++
			for _, forbidden := range []string{
				"oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url", "oauth_token_url",
				"base_url", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json",
			} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("vision compact query selected forbidden column %q: %s", forbidden, statement)
				}
			}
			continue
		}
		if !strings.Contains(statement, "where id = ?") && !strings.Contains(statement, "where a.id = ?") {
			t.Fatalf("unexpected model query: %s", statement)
		}
	}
	if vision != 1 {
		t.Fatalf("compact vision selection query count = %d, statements = %#v", vision, statements)
	}
}

func TestChannelModelSelectionQueryShape(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).LLMConfigs
	ctx := t.Context()
	counter.SetEnabled(true)
	if _, err := repo.ListChatSelectionOptions(ctx); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)
	assertChannelCompactStatement(t, channelAgentConfigStatements(counter.Statements()))
	counter.Reset()
	counter.SetEnabled(true)
	if _, err := repo.ListVisionSelectionOptions(ctx); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)
	assertChannelVisionCompactStatement(t, channelAgentConfigStatements(counter.Statements()))
}

func TestChannelTaskContextProjectionOmitsFullDetailColumns(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := sqlite.NewStores(db, nil).Tasks
	counter.SetEnabled(true)
	if _, err := repo.ListChatContextByProject(t.Context(), "default"); err != nil {
		t.Fatal(err)
	}
	counter.SetEnabled(false)
	found := false
	for _, statement := range counter.Statements() {
		query := strings.ToLower(statement)
		if !strings.Contains(query, "substr(t.prompt") {
			continue
		}
		found = true
		for _, forbidden := range []string{"swarm_config", "worktree_path", "merge_target_branch", "lineage_depth", "completed_at"} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("context query includes %s: %s", forbidden, query)
			}
		}
	}
	if !found {
		t.Fatal("missing compact task read")
	}
}
