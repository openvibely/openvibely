package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createLifecycleTestAgent(t testing.TB, agentRepo *AgentRepo) *models.Agent {
	t.Helper()
	a := &models.Agent{
		Name:         "Lifecycle Test Agent",
		Description:  "fixture",
		SystemPrompt: "You help with tests.",
		Model:        "inherit",
		Tools:        []string{"Read"},
	}
	if err := agentRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return a
}

func TestLifecycleRepo_HookCRUDAndQueryByWhen(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)

	h := &models.AgentLifecycleHook{
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "memory/observe_task_for_learning",
		OutputContract: models.OutputContractLearningSummary,
		Blocking:       true,
		Enabled:        true,
	}
	if err := repo.CreateHook(ctx, h); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	if h.ID == "" {
		t.Fatalf("expected hook ID set")
	}

	byAgent, err := repo.HooksByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatalf("hooks by agent: %v", err)
	}
	if len(byAgent) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(byAgent))
	}
	if byAgent[0].When != models.LifecycleAfterComplete {
		t.Fatalf("expected after_complete, got %s", byAgent[0].When)
	}
	if !byAgent[0].Blocking || !byAgent[0].Enabled {
		t.Fatalf("expected blocking+enabled to round-trip true, got %+v", byAgent[0])
	}

	// Seeded system-agent hooks share the after_complete slot, so assert
	// the test hook is present rather than expecting a single-row result.
	byWhen, err := repo.HooksForWhen(ctx, models.LifecycleAfterComplete)
	if err != nil {
		t.Fatalf("hooks for when: %v", err)
	}
	if !containsHookID(byWhen, h.ID) {
		t.Fatalf("expected hook %s in HooksForWhen result, got %+v", h.ID, byWhen)
	}

	// Disabling should remove the hook from HooksForWhen results.
	h.Enabled = false
	if err := repo.UpdateHook(ctx, h); err != nil {
		t.Fatalf("update hook: %v", err)
	}
	byWhen, err = repo.HooksForWhen(ctx, models.LifecycleAfterComplete)
	if err != nil {
		t.Fatalf("hooks for when after disable: %v", err)
	}
	if containsHookID(byWhen, h.ID) {
		t.Fatalf("expected hook %s to be filtered after disable, got %+v", h.ID, byWhen)
	}

	if err := repo.DeleteHook(ctx, h.ID); err != nil {
		t.Fatalf("delete hook: %v", err)
	}
	byAgent, _ = repo.HooksByAgent(ctx, agent.ID)
	if len(byAgent) != 0 {
		t.Fatalf("expected 0 hooks after delete, got %d", len(byAgent))
	}
}

func containsHookID(hooks []models.AgentLifecycleHook, id string) bool {
	for _, h := range hooks {
		if h.ID == id {
			return true
		}
	}
	return false
}

// TestLifecycleRepo_HooksForWhenExcludesArchivedAgentHooks verifies that
// hooks owned by archived or disabled agents are filtered out of HooksForWhen.
//
// Regression test for duplicate lifecycle activity rows: when a system agent
// (for example "System: Memory Curator") is renamed/absorbed via
// AgentRepo.MarkArchived, the old agent stays in the agents table but is
// flipped to enabled=0/generated_status=archived. Its agent_lifecycle_hooks
// rows still carry enabled=1, so HooksForWhen used to return both the archived
// and the canonical agent's hook for the same (when, skill_key) pair, causing
// the runner to record duplicate before_run/recall_memory and
// after_complete/update_memory executions per task run.
func TestLifecycleRepo_HooksForWhenExcludesArchivedAgentHooks(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	live := createLifecycleTestAgent(t, agentRepo)
	archived := &models.Agent{
		Name:         "Lifecycle Test Agent (archived)",
		Description:  "fixture",
		SystemPrompt: "x",
		Model:        "inherit",
	}
	if err := agentRepo.Create(ctx, archived); err != nil {
		t.Fatalf("create archived agent: %v", err)
	}

	liveHook := &models.AgentLifecycleHook{
		AgentID:        live.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "memory/recall_memory",
		OutputContract: models.OutputContractContextBlock,
		Enabled:        true,
	}
	if err := repo.CreateHook(ctx, liveHook); err != nil {
		t.Fatalf("create live hook: %v", err)
	}
	archivedHook := &models.AgentLifecycleHook{
		AgentID:        archived.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "memory/recall_memory",
		OutputContract: models.OutputContractContextBlock,
		Enabled:        true,
	}
	if err := repo.CreateHook(ctx, archivedHook); err != nil {
		t.Fatalf("create archived hook: %v", err)
	}

	// Before archive: both hooks are visible.
	before, err := repo.HooksForWhen(ctx, models.LifecycleBeforeRun)
	if err != nil {
		t.Fatalf("hooks for when (pre-archive): %v", err)
	}
	if !containsHookID(before, liveHook.ID) || !containsHookID(before, archivedHook.ID) {
		t.Fatalf("expected both hooks before archive, got %+v", before)
	}

	if err := agentRepo.MarkArchived(ctx, archived.ID, live.ID, "duplicate"); err != nil {
		t.Fatalf("mark archived: %v", err)
	}

	after, err := repo.HooksForWhen(ctx, models.LifecycleBeforeRun)
	if err != nil {
		t.Fatalf("hooks for when (post-archive): %v", err)
	}
	if !containsHookID(after, liveHook.ID) {
		t.Fatalf("expected live hook still present, got %+v", after)
	}
	if containsHookID(after, archivedHook.ID) {
		t.Fatalf("expected archived hook filtered from HooksForWhen, got %+v", after)
	}
}

func TestLifecycleRepo_ExecutionLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Lifecycle Exec Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	hook := &models.AgentLifecycleHook{
		AgentID:        agent.ID,
		When:           models.LifecycleBeforeRun,
		SkillKey:       "project_context/load",
		OutputContract: models.OutputContractContextBlock,
		Enabled:        true,
	}
	if err := repo.CreateHook(ctx, hook); err != nil {
		t.Fatalf("create hook: %v", err)
	}

	exec := &models.LifecycleExecution{
		TaskID:          task.ID,
		TaskRunID:       "run-1",
		AgentID:         agent.ID,
		When:            models.LifecycleBeforeRun,
		LifecycleHookID: &hook.ID,
		SkillKey:        hook.SkillKey,
		OutputContract:  hook.OutputContract,
		Status:          models.LifecycleExecRunning,
		AttemptCount:    1,
		InputJSON:       `{"task_id":"t"}`,
	}
	if err := repo.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if exec.ID == "" {
		t.Fatalf("expected execution ID set")
	}

	completed := time.Now().UTC()
	exec.Status = models.LifecycleExecCompleted
	exec.OutputJSON = `{"content":"context"}`
	exec.CompletedAt = &completed
	if err := repo.UpdateExecution(ctx, exec); err != nil {
		t.Fatalf("update execution: %v", err)
	}

	list, err := repo.ListExecutionsForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(list))
	}
	got := list[0]
	if got.Status != models.LifecycleExecCompleted {
		t.Fatalf("expected completed status, got %s", got.Status)
	}
	if got.OutputJSON != `{"content":"context"}` {
		t.Fatalf("expected stored output, got %s", got.OutputJSON)
	}
	if got.InputJSON != "" {
		t.Fatalf("expected compact list projection to omit input_json, got %q", got.InputJSON)
	}
	if got.TaskRunID != "" || got.LifecycleHookID != nil || got.ParentExecID != nil || got.AttemptCount != 0 || got.Priority != 0 || got.NextRetryAt != nil || got.IdempotencyKey != "" {
		t.Fatalf("expected compact list projection to omit non-rendered metadata, got %+v", got)
	}
	for _, forbidden := range []string{"input_json", "task_run_id", "lifecycle_hook_id", "parent_execution_id", "attempt_count", "priority", "next_retry_at", "idempotency_key"} {
		if strings.Contains(listExecutionsForTaskSQL, forbidden) {
			t.Fatalf("lifecycle list SQL must not select %s: %s", forbidden, listExecutionsForTaskSQL)
		}
	}
}

func TestLifecycleRepo_TaskRunFreshnessUsesRunHeadNotLateHookRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Lifecycle Freshness", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	createExec := func(runID string, when models.LifecycleWhen, skill string) *models.LifecycleExecution {
		t.Helper()
		exec := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: runID, AgentID: agent.ID, When: when, SkillKey: skill, OutputContract: models.OutputContractActivitySummary, Status: models.LifecycleExecCompleted}
		if err := repo.CreateExecution(ctx, exec); err != nil {
			t.Fatalf("create lifecycle execution %s/%s: %v", runID, skill, err)
		}
		return exec
	}

	oldHead := createExec("run-old", models.LifecycleRouteTask, "route_task")
	currentHead := createExec("run-current", models.LifecycleRouteTask, "route_task")
	lateOld := createExec("run-old", models.LifecycleAfterComplete, "observe_task_for_learning")

	current, err := repo.TaskRunFreshness(ctx, task.ID, "run-current")
	if err != nil {
		t.Fatalf("current freshness: %v", err)
	}
	if current.Stale {
		t.Fatalf("current latest run should be fresh despite late old hook row: %+v oldHead=%s currentHead=%s lateOld=%s", current, oldHead.ID, currentHead.ID, lateOld.ID)
	}
	if current.LatestRunID != "run-current" || current.SourceRunID != "run-current" || current.SourceRowID == 0 || current.LatestRowID == 0 {
		t.Fatalf("current freshness details not populated correctly: %+v", current)
	}

	old, err := repo.TaskRunFreshness(ctx, task.ID, "run-old")
	if err != nil {
		t.Fatalf("old freshness: %v", err)
	}
	if !old.Stale || old.LatestRunID != "run-current" {
		t.Fatalf("old run should be stale against current latest run, got %+v", old)
	}
}

func TestLifecycleRepo_IdempotencyKeyLookup(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Idemp", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	exec := &models.LifecycleExecution{
		TaskID:         task.ID,
		TaskRunID:      "run-1",
		AgentID:        agent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "activity/summarize",
		OutputContract: models.OutputContractActivitySummary,
		Status:         models.LifecycleExecCompleted,
		IdempotencyKey: "run-1:hook-123:deadbeef",
	}
	if err := repo.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	got, err := repo.FindExecutionByIdempotencyKey(ctx, exec.IdempotencyKey)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.ID != exec.ID {
		t.Fatalf("expected same execution, got %+v", got)
	}
	// Empty key returns sql.ErrNoRows so unkeyed rows never collide.
	if _, err := repo.FindExecutionByIdempotencyKey(ctx, ""); err == nil {
		t.Fatalf("expected error for empty key")
	}
}

func TestLifecycleRepo_ExecutionEvents(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Trace", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	exec := &models.LifecycleExecution{TaskID: task.ID, AgentID: agent.ID, When: models.LifecycleAfterComplete, Status: models.LifecycleExecRunning}
	if err := repo.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("create exec: %v", err)
	}

	first := &models.LifecycleExecutionEvent{LifecycleExecutionID: exec.ID, EventType: "tool_call", PayloadJSON: `{"name":"skills_list"}`}
	if err := repo.AppendExecutionEvent(ctx, first); err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second := &models.LifecycleExecutionEvent{LifecycleExecutionID: exec.ID, EventType: "tool_result", PayloadJSON: `{"ok":true}`}
	if err := repo.AppendExecutionEvent(ctx, second); err != nil {
		t.Fatalf("append second event: %v", err)
	}

	events, err := repo.ListExecutionEvents(ctx, exec.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[0].EventType != "tool_call" || events[1].Seq != 2 || events[1].EventType != "tool_result" {
		t.Fatalf("unexpected ordered events: %+v", events)
	}
}

// TestLifecycleRepo_ListExecutionsForTask_NewestFirst verifies that
// ListExecutionsForTask returns executions in descending started_at order so
// the newest lifecycle event is visible at the top of the UI without scrolling.
func TestLifecycleRepo_ListExecutionsForTask_NewestFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Order Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Insert three executions then explicitly backdate started_at so they have
	// distinct, known timestamps. SQLite datetime('now') has second precision, so
	// inserting quickly then updating is more reliable than sleeping.
	skillKeys := []string{"route_task", "recall_memory", "summarize_activity"}
	var ids []string
	for i, skillKey := range skillKeys {
		e := &models.LifecycleExecution{
			TaskID:   task.ID,
			AgentID:  agent.ID,
			When:     models.LifecycleAfterComplete,
			SkillKey: skillKey,
			Status:   models.LifecycleExecCompleted,
		}
		if err := repo.CreateExecution(ctx, e); err != nil {
			t.Fatalf("create exec %d: %v", i, err)
		}
		ids = append(ids, e.ID)
	}
	// Assign distinct timestamps: route_task is oldest, summarize_activity is newest.
	timestamps := []string{
		"2000-01-01 10:00:00", // route_task — oldest
		"2000-01-01 11:00:00", // recall_memory
		"2000-01-01 12:00:00", // summarize_activity — newest
	}
	for i, id := range ids {
		if _, err := db.ExecContext(ctx, `UPDATE lifecycle_executions SET started_at = ? WHERE id = ?`, timestamps[i], id); err != nil {
			t.Fatalf("backdate exec %d: %v", i, err)
		}
	}

	list, err := repo.ListExecutionsForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(list))
	}
	// Newest (summarize_activity) must be first (DESC ordering).
	if list[0].SkillKey != "summarize_activity" {
		t.Fatalf("expected newest execution first (summarize_activity), got %s", list[0].SkillKey)
	}
	if list[1].SkillKey != "recall_memory" {
		t.Fatalf("expected second execution (recall_memory) at index 1, got %s", list[1].SkillKey)
	}
	if list[2].SkillKey != "route_task" {
		t.Fatalf("expected oldest execution last (route_task), got %s", list[2].SkillKey)
	}
}

func TestLifecycleRepo_ListExecutionsForTaskQueryPlanUsesTaskStartedIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	plan := explainLifecycleRepoQueryPlan(t, db, listExecutionsForTaskSQL, "task-plan")
	if !strings.Contains(plan, "idx_lifecycle_executions_task_started") {
		t.Fatalf("lifecycle list plan = %q, want task started index", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("lifecycle list plan = %q, want no temporary sort", plan)
	}
}

func explainLifecycleRepoQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "; ")
}

func TestLifecycleRepo_ListExecutionsForTask_DeterministicIDTieBreak(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Tie Order Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ids := []string{"tie-a", "tie-c", "tie-b"}
	for _, id := range ids {
		e := &models.LifecycleExecution{
			TaskID:         task.ID,
			AgentID:        agent.ID,
			When:           models.LifecycleAfterComplete,
			SkillKey:       id,
			OutputContract: models.OutputContractActivitySummary,
			Status:         models.LifecycleExecCompleted,
		}
		if err := repo.CreateExecution(ctx, e); err != nil {
			t.Fatalf("create exec %s: %v", id, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE lifecycle_executions SET id = ?, started_at = ? WHERE id = ?`, id, "2000-01-01 12:00:00", e.ID); err != nil {
			t.Fatalf("set deterministic id %s: %v", id, err)
		}
	}

	list, err := repo.ListExecutionsForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(list))
	}
	want := []string{"tie-c", "tie-b", "tie-a"}
	for i, wantID := range want {
		if list[i].ID != wantID {
			t.Fatalf("list[%d].ID = %s, want %s; list=%+v", i, list[i].ID, wantID, list)
		}
	}
}

func TestLifecycleRepo_IdempotencyAllowsRetryAfterFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Retry", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	key := "run-1:hook-x:abc"
	// First attempt fails.
	first := &models.LifecycleExecution{
		TaskID: task.ID, TaskRunID: "run-1", AgentID: agent.ID,
		When: models.LifecycleAfterComplete, SkillKey: "x/y",
		Status: models.LifecycleExecFailed, IdempotencyKey: key,
	}
	if err := repo.CreateExecution(ctx, first); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	// Retry with same key should be allowed because partial unique index
	// only enforces uniqueness for completed rows.
	second := &models.LifecycleExecution{
		TaskID: task.ID, TaskRunID: "run-1", AgentID: agent.ID,
		When: models.LifecycleAfterComplete, SkillKey: "x/y",
		Status: models.LifecycleExecRunning, IdempotencyKey: key,
	}
	if err := repo.CreateExecution(ctx, second); err != nil {
		t.Fatalf("retry should be allowed, got %v", err)
	}
	// Completing second is OK because no other completed row holds the key.
	completed := time.Now().UTC()
	second.Status = models.LifecycleExecCompleted
	second.CompletedAt = &completed
	if err := repo.UpdateExecution(ctx, second); err != nil {
		t.Fatalf("update second: %v", err)
	}
	// A third completed row with same key MUST be rejected.
	third := &models.LifecycleExecution{
		TaskID: task.ID, TaskRunID: "run-1", AgentID: agent.ID,
		When: models.LifecycleAfterComplete, SkillKey: "x/y",
		Status: models.LifecycleExecCompleted, IdempotencyKey: key,
	}
	if err := repo.CreateExecution(ctx, third); err == nil {
		t.Fatalf("expected unique-index violation for second completed row with same key")
	}
}

func TestLifecycleRepo_HasNewerTaskRunUsesCreationOrderForSameSecondRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{ProjectID: "default", Title: "Run order", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "p"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	older := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: "run-old", AgentID: agent.ID, When: models.LifecycleAfterComplete, SkillKey: "x", Status: models.LifecycleExecRunning}
	if err := repo.CreateExecution(ctx, older); err != nil {
		t.Fatalf("create older: %v", err)
	}
	newer := &models.LifecycleExecution{TaskID: task.ID, TaskRunID: "run-new", AgentID: agent.ID, When: models.LifecycleRouteTask, SkillKey: "y", Status: models.LifecycleExecRunning}
	if err := repo.CreateExecution(ctx, newer); err != nil {
		t.Fatalf("create newer: %v", err)
	}

	hasNewer, err := repo.HasNewerTaskRun(ctx, task.ID, "run-old")
	if err != nil {
		t.Fatalf("has newer old: %v", err)
	}
	if !hasNewer {
		t.Fatalf("expected run-old to have newer run")
	}
	hasNewer, err = repo.HasNewerTaskRun(ctx, task.ID, "run-new")
	if err != nil {
		t.Fatalf("has newer new: %v", err)
	}
	if hasNewer {
		t.Fatalf("did not expect run-new to have newer run")
	}
}

func BenchmarkLifecycleRepo_ListExecutionsForTaskCompactProjection(b *testing.B) {
	db := testutil.NewTestDB(b)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(b, agentRepo)
	target := &models.Task{ProjectID: "default", Title: "Lifecycle Benchmark Target", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	if err := taskRepo.Create(ctx, target); err != nil {
		b.Fatalf("create target task: %v", err)
	}
	other := &models.Task{ProjectID: "default", Title: "Lifecycle Benchmark Other", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "p"}
	if err := taskRepo.Create(ctx, other); err != nil {
		b.Fatalf("create other task: %v", err)
	}

	const rowsPerTask = 2500
	largeInput := `{"prompt":"` + strings.Repeat("x", 32*1024) + `"}`
	outputJSON := `{"summary":"` + strings.Repeat("y", 1024) + `"}`
	seedLifecycleExecutionBenchmarkRows(b, db, target.ID, agent.ID, "target", rowsPerTask, largeInput, outputJSON)
	seedLifecycleExecutionBenchmarkRows(b, db, other.ID, agent.ID, "other", rowsPerTask, largeInput, outputJSON)

	b.Run("old_full_row_baseline", func(b *testing.B) {
		b.ReportAllocs()
		var totalStringBytes int64
		for i := 0; i < b.N; i++ {
			list, stringBytes, err := listExecutionsForTaskFullBaseline(ctx, db, target.ID)
			if err != nil {
				b.Fatalf("baseline list: %v", err)
			}
			if len(list) != rowsPerTask {
				b.Fatalf("baseline rows = %d, want %d", len(list), rowsPerTask)
			}
			totalStringBytes += int64(stringBytes)
		}
		b.ReportMetric(float64(totalStringBytes)/float64(b.N), "scanned_string_bytes/op")
	})

	b.Run("compact_projection", func(b *testing.B) {
		b.ReportAllocs()
		var totalStringBytes int64
		for i := 0; i < b.N; i++ {
			list, err := repo.ListExecutionsForTask(ctx, target.ID)
			if err != nil {
				b.Fatalf("compact list: %v", err)
			}
			if len(list) != rowsPerTask {
				b.Fatalf("compact rows = %d, want %d", len(list), rowsPerTask)
			}
			for _, e := range list {
				totalStringBytes += int64(lifecycleExecutionListStringBytes(e))
			}
		}
		b.ReportMetric(float64(totalStringBytes)/float64(b.N), "scanned_string_bytes/op")
	})
}

func seedLifecycleExecutionBenchmarkRows(b *testing.B, db *sql.DB, taskID, agentID, prefix string, count int, inputJSON, outputJSON string) {
	b.Helper()
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin seed tx: %v", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO lifecycle_executions
			(id, task_id, task_run_id, agent_id, when_slot, skill_key, output_contract, status,
			 input_json, output_json, error, attempt_count, priority, started_at, completed_at)
		VALUES (?, ?, ?, ?, 'after_complete', ?, 'activity_summary', 'completed', ?, ?, '', 1, 0, ?, ?)`)
	if err != nil {
		b.Fatalf("prepare seed statement: %v", err)
	}
	defer stmt.Close()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		started := base.Add(-time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")
		id := fmt.Sprintf("bench-%s-%05d", prefix, i)
		if _, err := stmt.Exec(id, taskID, "run-"+id, agentID, "summarize_activity", inputJSON, outputJSON, started, started); err != nil {
			b.Fatalf("insert benchmark row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed tx: %v", err)
	}
}

func listExecutionsForTaskFullBaseline(ctx context.Context, db *sql.DB, taskID string) ([]models.LifecycleExecution, int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+execCols+`
		FROM lifecycle_executions
		WHERE task_id = ?
		ORDER BY started_at DESC, id DESC`, taskID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.LifecycleExecution
	stringBytes := 0
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *e)
		stringBytes += lifecycleExecutionFullStringBytes(*e)
	}
	return out, stringBytes, rows.Err()
}

func lifecycleExecutionFullStringBytes(e models.LifecycleExecution) int {
	return len(e.ID) + len(e.TaskID) + len(e.TaskRunID) + len(e.AgentID) + len(string(e.When)) + len(e.SkillKey) +
		len(string(e.OutputContract)) + len(string(e.Status)) + len(e.InputJSON) + len(e.OutputJSON) + len(e.Error) + len(e.IdempotencyKey)
}

func lifecycleExecutionListStringBytes(e models.LifecycleExecution) int {
	return len(e.ID) + len(e.AgentID) + len(string(e.When)) + len(e.SkillKey) +
		len(string(e.OutputContract)) + len(string(e.Status)) + len(e.OutputJSON) + len(e.Error)
}

func TestLifecycleRepo_PatchExecutionOutputSkills(t *testing.T) {
	db := testutil.NewTestDB(t)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	repo := NewLifecycleRepo(db)
	ctx := context.Background()

	agent := createLifecycleTestAgent(t, agentRepo)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Patch Skills Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	hook := &models.AgentLifecycleHook{
		AgentID:        agent.ID,
		When:           models.LifecycleRouteTask,
		SkillKey:       "skill_curator/route",
		OutputContract: models.OutputContractSelectedSkills,
		Enabled:        true,
	}
	if err := repo.CreateHook(ctx, hook); err != nil {
		t.Fatalf("create hook: %v", err)
	}
	exec := &models.LifecycleExecution{
		TaskID:          task.ID,
		TaskRunID:       "run-patch-1",
		AgentID:         agent.ID,
		When:            models.LifecycleRouteTask,
		LifecycleHookID: &hook.ID,
		SkillKey:        hook.SkillKey,
		OutputContract:  hook.OutputContract,
		Status:          models.LifecycleExecRunning,
		AttemptCount:    1,
	}
	if err := repo.CreateExecution(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	now := time.Now().UTC()
	exec.Status = models.LifecycleExecCompleted
	exec.OutputJSON = `{"skills":["skill_a"],"confidence":0.9,"reason":"test"}`
	exec.CompletedAt = &now
	if err := repo.UpdateExecution(ctx, exec); err != nil {
		t.Fatalf("update execution: %v", err)
	}

	// Patch with merged list.
	if err := repo.PatchExecutionOutputSkills(ctx, exec.ID, []string{"skill_a", "openvibely_project_guidance"}); err != nil {
		t.Fatalf("patch output skills: %v", err)
	}

	list, err := repo.ListExecutionsForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("expected at least one execution")
	}
	got := list[0]

	var probe map[string]any
	if err := json.Unmarshal([]byte(got.OutputJSON), &probe); err != nil {
		t.Fatalf("unmarshal patched output_json: %v", err)
	}
	rawSkills, _ := probe["skills"].([]any)
	if len(rawSkills) != 2 {
		t.Fatalf("expected 2 skills after patch, got %d: %v", len(rawSkills), rawSkills)
	}
	// Other fields must be preserved.
	if conf, _ := probe["confidence"].(float64); conf != 0.9 {
		t.Fatalf("expected confidence=0.9 preserved, got %v", conf)
	}
	if reason, _ := probe["reason"].(string); reason != "test" {
		t.Fatalf("expected reason preserved, got %q", reason)
	}

	// No-op on empty execID must not error.
	if err := repo.PatchExecutionOutputSkills(ctx, "", []string{"x"}); err != nil {
		t.Fatalf("expected no-op for empty execID, got: %v", err)
	}

	// Unknown exec ID must not error (row not found → silent skip).
	if err := repo.PatchExecutionOutputSkills(ctx, "nonexistent-id", []string{"x"}); err != nil {
		t.Fatalf("expected no-op for unknown execID, got: %v", err)
	}
}
