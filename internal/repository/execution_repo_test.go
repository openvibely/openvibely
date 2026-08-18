package repository

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestExecutionRepo_ListByTaskHistoryPageUsesTaskStartedIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	plan := explainExecutionRepoPlan(t, db, taskExecutionHistoryPageSQL, "task-history-plan", 21)
	if !strings.Contains(plan, "idx_executions_task_started_at") {
		t.Fatalf("expected execution-history page query to use idx_executions_task_started_at, plan:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("execution-history page query should not sort with a temp B-tree, plan:\n%s", plan)
	}
}

func TestExecutionRepo_GetTaskExecutionMetrics(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "Metrics Test", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		id         string
		startedAt  time.Time
		durationMs int64
	}{
		{id: "exec-old", startedAt: base, durationMs: 2500},
		{id: "exec-latest-duration", startedAt: base.Add(time.Minute), durationMs: 4200},
		{id: "exec-newest", startedAt: base.Add(2 * time.Minute), durationMs: 0},
	}
	for _, row := range rows {
		_, err := db.ExecContext(ctx, `INSERT INTO executions (id, task_id, status, prompt_sent, output, duration_ms, started_at, completed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, row.id, task.ID, models.ExecCompleted, "prompt", "output", row.durationMs, row.startedAt.Format("2006-01-02 15:04:05"), row.startedAt.Add(time.Second).Format("2006-01-02 15:04:05"))
		if err != nil {
			t.Fatalf("insert execution %s: %v", row.id, err)
		}
	}

	metrics, err := execRepo.GetTaskExecutionMetrics(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionMetrics: %v", err)
	}
	if metrics.LatestStartedAt == nil || !metrics.LatestStartedAt.Equal(rows[2].startedAt) {
		t.Fatalf("LatestStartedAt = %v, want %v", metrics.LatestStartedAt, rows[2].startedAt)
	}
	if metrics.LatestDurationMs != rows[1].durationMs {
		t.Fatalf("LatestDurationMs = %d, want %d", metrics.LatestDurationMs, rows[1].durationMs)
	}
}

func TestExecutionRepo_GetTaskExecutionMetricsUsesTaskStartedIndexAndOmitsExecutionText(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, forbidden := range []string{"prompt_sent", "output", "error_message", "reasoning_content", "diff_output"} {
		if strings.Contains(taskExecutionMetricsSQL, forbidden) {
			t.Fatalf("task execution metrics query must not select historical %s text: %s", forbidden, taskExecutionMetricsSQL)
		}
	}
	plan := explainExecutionRepoPlan(t, db, taskExecutionMetricsSQL, "task-metrics-plan", "task-metrics-plan")
	if !strings.Contains(plan, "idx_executions_task_started_at") {
		t.Fatalf("expected task metrics query to use idx_executions_task_started_at, plan:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE") {
		t.Fatalf("task metrics query should not materialize a temp B-tree, plan:\n%s", plan)
	}
}

func explainExecutionRepoPlan(t testing.TB, db interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return strings.Join(lines, "\n")
}

func TestExecutionRepo_CreateAndComplete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	// Create a task
	task := &models.Task{ProjectID: "default", Title: "Exec Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)

	// Get default agent
	agent, _ := agentRepo.GetDefault(ctx)

	// Create execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if exec.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Complete execution
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output text", "", 100, 500); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := execRepo.UpdateReasoningContent(ctx, exec.ID, "private reasoning"); err != nil {
		t.Fatalf("UpdateReasoningContent: %v", err)
	}

	// Verify
	got, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.ExecCompleted {
		t.Errorf("expected Status=completed, got %q", got.Status)
	}
	if got.Output != "output text" {
		t.Errorf("expected Output=output text, got %q", got.Output)
	}
	if got.ReasoningContent != "private reasoning" {
		t.Errorf("expected ReasoningContent=private reasoning, got %q", got.ReasoningContent)
	}
	if got.TokensUsed != 100 {
		t.Errorf("expected TokensUsed=100, got %d", got.TokensUsed)
	}
	if got.DurationMs != 500 {
		t.Errorf("expected DurationMs=500, got %d", got.DurationMs)
	}
}

func TestExecutionRepo_ReplaceReasoningReplay(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Replay Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent, err := agentRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default agent: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "first question"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	want := []models.ExecutionReplayMessage{
		{UserContent: "first question", AssistantContent: "first answer", ReasoningContent: "first thought", TranscriptJSON: `[{"role":"user","content":"first question"}]`},
		{UserContent: "steer", AssistantContent: "second answer", ReasoningContent: "second thought"},
	}
	if err := execRepo.ReplaceReasoningReplay(ctx, exec.ID, "first thoughtsecond thought", want); err != nil {
		t.Fatalf("replace reasoning replay: %v", err)
	}

	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if stored.ReasoningContent != "first thoughtsecond thought" {
		t.Fatalf("reasoning content = %q", stored.ReasoningContent)
	}
	replay, err := execRepo.ReplayMessagesByExecutionIDs(ctx, []string{"", exec.ID, exec.ID})
	if err != nil {
		t.Fatalf("get replay messages: %v", err)
	}
	if !reflect.DeepEqual(replay[exec.ID], want) {
		t.Fatalf("replay messages = %#v, want %#v", replay[exec.ID], want)
	}

	replacement := []models.ExecutionReplayMessage{
		{UserContent: "replacement", AssistantContent: "answer", ReasoningContent: "thought"},
	}
	if err := execRepo.ReplaceReasoningReplay(ctx, exec.ID, "thought", replacement); err != nil {
		t.Fatalf("replace replay again: %v", err)
	}
	replay, err = execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	if err != nil {
		t.Fatalf("get replaced replay messages: %v", err)
	}
	if !reflect.DeepEqual(replay[exec.ID], replacement) {
		t.Fatalf("replaced replay messages = %#v, want %#v", replay[exec.ID], replacement)
	}
}

func TestExecutionRepo_CompletePreservesCodedAliasesAndNormalizesRealAliases(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Alias Persistence", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent, err := agentRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default agent: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	coded := "`\u003cthinking\u003ecoded\nthought\u003c/thinking\u003e`\n``[Using tool: bash\">\n\u003cparameter name=\"command\"\u003eecho coded\u003c/parameter\u003e\n\u003c/invoke\u003e``\n```text\n[Using tool: bash\"> \u003cparameter name=\"command\"\u003eecho fenced\u003c/parameter\u003e \u003c/invoke\u003e\n```\n~~~text\r\u003cthinking\u003ebare CR fenced\u003c/thinking\u003e\r~~~"
	escapedReal := `Escaped \` + "`" + `<thinking>escaped real</thinking> escaped \` + "`"
	output := "Unmatched ``` prefix; " + coded + "\n" + escapedReal + "\n\u003cthinking\u003ereal\u003c/thinking\u003e"
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, output, "", 0, 1); err != nil {
		t.Fatalf("complete execution: %v", err)
	}

	got, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if !strings.Contains(got.Output, coded) {
		t.Fatalf("coded aliases changed during persistence:\n%q", got.Output)
	}
	if !strings.Contains(got.Output, "[Thinking]\nescaped real\n[/Thinking]") || !strings.Contains(got.Output, "[Thinking]\nreal\n[/Thinking]") {
		t.Fatalf("real aliases were not normalized during persistence:\n%q", got.Output)
	}
}

func TestExecutionRepo_CompleteFailed(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Fail Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	execRepo.Create(ctx, exec)

	execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "something broke", 0, 100)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecFailed {
		t.Errorf("expected Status=failed, got %q", got.Status)
	}
	if got.ErrorMessage != "something broke" {
		t.Errorf("expected ErrorMessage=something broke, got %q", got.ErrorMessage)
	}
}

func TestExecutionRepo_CompleteFailedPreservesStreamedOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Fail Preserve Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	execRepo.Create(ctx, exec)

	streamed := "[Using tool: bash]\nrunning commands...\n[Thinking] lots of intermediate content"
	// Simulate streaming writer writing partial output during execution
	execRepo.UpdateOutput(ctx, exec.ID, streamed)

	// Verify output was written
	before, _ := execRepo.GetByID(ctx, exec.ID)
	if before.Output == "" {
		t.Fatal("expected output to be set before Complete")
	}

	// Complete with failed status and empty output — must preserve streamed content
	execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "command failed with exit code 1", 0, 500)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecFailed {
		t.Errorf("expected Status=failed, got %q", got.Status)
	}
	if got.Output != streamed {
		t.Errorf("expected Output to preserve streamed content on failure, got %q", got.Output)
	}
	if got.ErrorMessage != "command failed with exit code 1" {
		t.Errorf("expected ErrorMessage='command failed with exit code 1', got %q", got.ErrorMessage)
	}
	if got.PromptSent != "test prompt" {
		t.Errorf("expected PromptSent preserved, got %q", got.PromptSent)
	}
}

func TestExecutionRepo_CompleteNonFailedPreservesOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Preserve Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	execRepo.Create(ctx, exec)

	// Simulate streaming writer writing output
	execRepo.UpdateOutput(ctx, exec.ID, "streamed output content")

	// Complete with completed status and empty output — should preserve existing output
	execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "", "", 100, 500)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecCompleted {
		t.Errorf("expected Status=completed, got %q", got.Status)
	}
	if got.Output != "streamed output content" {
		t.Errorf("expected Output preserved for non-failed status, got %q", got.Output)
	}
}

func TestExecutionRepo_ListByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "List Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create two executions
	var executionIDs []string
	for i := 0; i < 2; i++ {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
		execRepo.Create(ctx, exec)
		executionIDs = append(executionIDs, exec.ID)
	}
	if err := execRepo.UpdateReasoningContent(ctx, executionIDs[0], "large private reasoning"); err != nil {
		t.Fatalf("UpdateReasoningContent: %v", err)
	}

	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 2 {
		t.Errorf("expected 2 executions, got %d", len(execs))
	}
	for _, exec := range execs {
		if exec.ReasoningContent != "" {
			t.Errorf("lightweight list loaded reasoning content for %q", exec.ID)
		}
	}

	reasoningByID, err := execRepo.ReasoningContentByIDs(ctx, []string{executionIDs[0], executionIDs[0], ""})
	if err != nil {
		t.Fatalf("ReasoningContentByIDs: %v", err)
	}
	if got := reasoningByID[executionIDs[0]]; got != "large private reasoning" {
		t.Errorf("ReasoningContentByIDs[%q] = %q", executionIDs[0], got)
	}
}

func TestExecutionRepo_ListChatHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	agent, _ := agentRepo.GetDefault(ctx)

	// Create chat tasks and executions
	chatMessages := []struct {
		prompt string
		output string
	}{
		{"Hello", "Hi there!"},
		{"How are you?", "I'm doing well, thanks!"},
		{"What's the weather?", "I don't have access to weather data."},
	}

	for _, msg := range chatMessages {
		task := &models.Task{
			ProjectID: "default",
			Title:     "Chat: " + msg.prompt,
			Category:  models.CategoryChat,
			Status:    models.StatusPending,
			Prompt:    msg.prompt,
		}
		taskRepo.Create(ctx, task)

		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    msg.prompt,
		}
		execRepo.Create(ctx, exec)
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, msg.output, "", 50, 100)
	}

	// Create a non-chat task to ensure it's not included
	regularTask := &models.Task{
		ProjectID: "default",
		Title:     "Regular Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "regular prompt",
	}
	taskRepo.Create(ctx, regularTask)
	regularExec := &models.Execution{
		TaskID:        regularTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "regular prompt",
	}
	execRepo.Create(ctx, regularExec)

	// Test ListChatHistory
	history, err := execRepo.ListChatHistory(ctx, "default", 50)
	if err != nil {
		t.Fatalf("ListChatHistory: %v", err)
	}

	// Should only return chat messages (3), not the regular task
	if len(history) != 3 {
		t.Fatalf("expected 3 chat messages, got %d", len(history))
	}

	// Verify messages are in chronological order (oldest first)
	if history[0].PromptSent != "Hello" {
		t.Errorf("expected first message to be 'Hello', got %q", history[0].PromptSent)
	}
	if history[1].PromptSent != "How are you?" {
		t.Errorf("expected second message to be 'How are you?', got %q", history[1].PromptSent)
	}
	if history[2].PromptSent != "What's the weather?" {
		t.Errorf("expected third message to be 'What's the weather?', got %q", history[2].PromptSent)
	}

	// Verify outputs
	if history[0].Output != "Hi there!" {
		t.Errorf("expected first output to be 'Hi there!', got %q", history[0].Output)
	}

	// Test limit parameter
	limitedHistory, err := execRepo.ListChatHistory(ctx, "default", 2)
	if err != nil {
		t.Fatalf("ListChatHistory with limit: %v", err)
	}
	if len(limitedHistory) != 2 {
		t.Errorf("expected 2 messages with limit=2, got %d", len(limitedHistory))
	}
}

func TestExecutionRepo_IsFollowup(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Followup Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create a regular execution (not a followup)
	regularExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "original prompt",
		IsFollowup:    false,
	}
	if err := execRepo.Create(ctx, regularExec); err != nil {
		t.Fatalf("Create regular exec: %v", err)
	}

	// Create a followup execution
	followupExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup message",
		IsFollowup:    true,
	}
	if err := execRepo.Create(ctx, followupExec); err != nil {
		t.Fatalf("Create followup exec: %v", err)
	}

	// Verify regular execution
	got, err := execRepo.GetByID(ctx, regularExec.ID)
	if err != nil {
		t.Fatalf("GetByID regular: %v", err)
	}
	if got.IsFollowup {
		t.Error("expected regular execution to have IsFollowup=false")
	}

	// Verify followup execution
	got, err = execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("GetByID followup: %v", err)
	}
	if !got.IsFollowup {
		t.Error("expected followup execution to have IsFollowup=true")
	}

	// Verify ListByTask also returns the IsFollowup flag
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(execs))
	}
	// Check that one is followup and one is not (order may vary due to same timestamp)
	followupCount := 0
	for _, e := range execs {
		if e.IsFollowup {
			followupCount++
		}
	}
	if followupCount != 1 {
		t.Errorf("expected exactly 1 followup execution, got %d", followupCount)
	}
}

func TestExecutionRepo_ListByTaskChronological(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Chrono Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create executions in order
	prompts := []string{"first", "second", "third"}
	for _, p := range prompts {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    p,
		}
		execRepo.Create(ctx, exec)
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output for "+p, "", 10, 100)
	}

	// ListByTaskChronological should return oldest first
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(execs))
	}
	if execs[0].PromptSent != "first" {
		t.Errorf("expected first execution to be 'first', got %q", execs[0].PromptSent)
	}
	if execs[1].PromptSent != "second" {
		t.Errorf("expected second execution to be 'second', got %q", execs[1].PromptSent)
	}
	if execs[2].PromptSent != "third" {
		t.Errorf("expected third execution to be 'third', got %q", execs[2].PromptSent)
	}

	// Verify chronological order: first should come before third
	if execs[0].StartedAt.After(execs[2].StartedAt) {
		t.Error("expected chronological order: first execution should not be after third")
	}
}

func TestExecutionRepo_ListByTaskChronological_FollowupOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Followup Order Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create original execution
	exec1 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "original prompt",
		IsFollowup:    false,
	}
	execRepo.Create(ctx, exec1)
	execRepo.Complete(ctx, exec1.ID, models.ExecCompleted, "original output", "", 100, 500)

	// Create follow-up executions
	exec2 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup 1",
		IsFollowup:    true,
	}
	execRepo.Create(ctx, exec2)
	execRepo.Complete(ctx, exec2.ID, models.ExecCompleted, "followup 1 output", "", 50, 300)

	exec3 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup 2",
		IsFollowup:    true,
	}
	execRepo.Create(ctx, exec3)
	execRepo.Complete(ctx, exec3.ID, models.ExecCompleted, "followup 2 output", "", 50, 200)

	// ListByTaskChronological should return in creation order
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(execs))
	}

	// Verify order: original, followup 1, followup 2
	if execs[0].PromptSent != "original prompt" {
		t.Errorf("expected first execution to be original, got %q", execs[0].PromptSent)
	}
	if execs[0].IsFollowup {
		t.Error("expected first execution to be non-followup")
	}
	if execs[1].PromptSent != "followup 1" {
		t.Errorf("expected second execution to be followup 1, got %q", execs[1].PromptSent)
	}
	if !execs[1].IsFollowup {
		t.Error("expected second execution to be followup")
	}
	if execs[2].PromptSent != "followup 2" {
		t.Errorf("expected third execution to be followup 2, got %q", execs[2].PromptSent)
	}
	if !execs[2].IsFollowup {
		t.Error("expected third execution to be followup")
	}
}

// TestExecutionRepo_MultiTurnOrderingWithReRuns verifies that chronological ordering
// works correctly when a task has multiple runs AND follow-ups, reproducing the bug
// where follow-up messages appeared at the top of the chat instead of the bottom.
func TestExecutionRepo_DiffOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Diff Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Complete the execution
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output text", "", 100, 500); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Update diff output
	diffData := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n package main\n+import \"fmt\"\n func main() {\n }\n"
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, diffData); err != nil {
		t.Fatalf("UpdateDiffOutput: %v", err)
	}

	// Verify via GetByID
	got, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DiffOutput != diffData {
		t.Errorf("expected DiffOutput=%q, got %q", diffData, got.DiffOutput)
	}

	// ListByTask and ListByTaskChronological use light column sets that omit diff_output to
	// avoid loading large blobs on every list/pagination request. Verify they return empty DiffOutput.
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(execs))
	}
	if execs[0].DiffOutput != "" {
		t.Errorf("ListByTask: expected DiffOutput to be empty (light query), got %q", execs[0].DiffOutput)
	}

	chronoExecs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(chronoExecs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(chronoExecs))
	}
	if chronoExecs[0].DiffOutput != "" {
		t.Errorf("ListByTaskChronological: expected DiffOutput to be empty (light query), got %q", chronoExecs[0].DiffOutput)
	}

	// Verify the targeted diff lookup returns the stored diff.
	latestDiff, err := execRepo.GetLatestNonEmptyDiffOutput(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetLatestNonEmptyDiffOutput: %v", err)
	}
	if latestDiff != diffData {
		t.Errorf("GetLatestNonEmptyDiffOutput: expected %q, got %q", diffData, latestDiff)
	}

	// A task with no non-empty diff output should return "".
	emptyTask := &models.Task{ProjectID: "default", Title: "No Diff Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "x"}
	taskRepo.Create(ctx, emptyTask)
	noDiff, err := execRepo.GetLatestNonEmptyDiffOutput(ctx, emptyTask.ID)
	if err != nil {
		t.Fatalf("GetLatestNonEmptyDiffOutput (no diff): %v", err)
	}
	if noDiff != "" {
		t.Errorf("GetLatestNonEmptyDiffOutput: expected empty for task with no diff, got %q", noDiff)
	}
}

func TestExecutionRepo_ListByTaskIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	agent, _ := agentRepo.GetDefault(ctx)

	task1 := &models.Task{ProjectID: "default", Title: "Task A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "a"}
	task2 := &models.Task{ProjectID: "default", Title: "Task B", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "b"}
	taskRepo.Create(ctx, task1)
	taskRepo.Create(ctx, task2)

	// Create executions: 2 for task1, 1 for task2
	for i := 0; i < 2; i++ {
		exec := &models.Execution{TaskID: task1.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "prompt"}
		execRepo.Create(ctx, exec)
	}
	exec3 := &models.Execution{TaskID: task2.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "prompt"}
	execRepo.Create(ctx, exec3)

	// Batch load
	result, err := execRepo.ListByTaskIDs(ctx, []string{task1.ID, task2.ID})
	if err != nil {
		t.Fatalf("ListByTaskIDs: %v", err)
	}
	if len(result[task1.ID]) != 2 {
		t.Errorf("expected 2 executions for task1, got %d", len(result[task1.ID]))
	}
	if len(result[task2.ID]) != 1 {
		t.Errorf("expected 1 execution for task2, got %d", len(result[task2.ID]))
	}

	// Task with no executions should not appear
	task3 := &models.Task{ProjectID: "default", Title: "Task C", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "c"}
	taskRepo.Create(ctx, task3)
	result2, err := execRepo.ListByTaskIDs(ctx, []string{task3.ID})
	if err != nil {
		t.Fatalf("ListByTaskIDs no execs: %v", err)
	}
	if len(result2[task3.ID]) != 0 {
		t.Errorf("expected 0 executions for task3, got %d", len(result2[task3.ID]))
	}

	// Empty input
	result3, err := execRepo.ListByTaskIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("ListByTaskIDs empty: %v", err)
	}
	if len(result3) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result3))
	}
}

func TestExecutionRepo_MultiTurnOrderingWithReRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Multi-turn Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original task prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Simulate: initial run → re-run → follow-up → another follow-up
	execData := []struct {
		prompt     string
		isFollowup bool
	}{
		{"original task prompt", false},   // Initial run
		{"original task prompt", false},   // Re-run (e.g., from scheduler)
		{"Summarize this", true},          // Follow-up 1
		{"What about performance?", true}, // Follow-up 2
	}

	execIDs := make([]string, len(execData))
	for i, ed := range execData {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    ed.prompt,
			IsFollowup:    ed.isFollowup,
		}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("Create exec %d: %v", i, err)
		}
		execIDs[i] = exec.ID
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output "+exec.ID, "", 50, 100)
	}

	// ListByTaskChronological must return in creation order (ASC)
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 4 {
		t.Fatalf("expected 4 executions, got %d", len(execs))
	}

	// Verify order matches creation order
	for i, exec := range execs {
		if exec.ID != execIDs[i] {
			t.Errorf("execution %d: expected ID %s, got %s", i, execIDs[i], exec.ID)
		}
	}

	// Follow-ups must be LAST (at the bottom of the chat)
	if execs[2].PromptSent != "Summarize this" {
		t.Errorf("expected 3rd execution to be follow-up 'Summarize this', got %q", execs[2].PromptSent)
	}
	if execs[3].PromptSent != "What about performance?" {
		t.Errorf("expected 4th execution to be follow-up 'What about performance?', got %q", execs[3].PromptSent)
	}

	// Also verify that ListByTaskChronological and ListByTask return
	// different orderings, confirming the bug was using the wrong query.
	// ListByTask uses DESC (newest first), ListByTaskChronological uses ASC.
	descExecs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(descExecs) != 4 {
		t.Fatalf("expected 4 executions from ListByTask, got %d", len(descExecs))
	}
}

// TestExecutionRepo_ListByProjectExcludingChat verifies that memory
// consolidation's execution snippets exclude Chat-category tasks so chat
// prompts and orchestration/mode-control text never feed durable memory,
// while task and task-thread follow-up executions still surface.
func TestExecutionRepo_ListByProjectExcludingChat(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	agent, _ := agentRepo.GetDefault(ctx)

	chatTask := &models.Task{ProjectID: "default", Title: "Chat 12:00:00.000: hi", Category: models.CategoryChat, Status: models.StatusPending, Prompt: "switch to orchestrate mode"}
	if err := taskRepo.Create(ctx, chatTask); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	taskTask := &models.Task{ProjectID: "default", Title: "Real task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "implement feature"}
	if err := taskRepo.Create(ctx, taskTask); err != nil {
		t.Fatalf("create real task: %v", err)
	}

	mkExec := func(taskID, prompt string, followup bool) string {
		exec := &models.Execution{TaskID: taskID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: prompt, IsFollowup: followup}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec: %v", err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "out", "", 1, 1); err != nil {
			t.Fatalf("complete exec: %v", err)
		}
		return exec.ID
	}
	chatExecID := mkExec(chatTask.ID, "Plan: refactor", false)
	taskExecID := mkExec(taskTask.ID, "implement feature", false)
	threadExecID := mkExec(taskTask.ID, "follow-up question", true)

	// Plain ListByProject must include all of them (used by other surfaces).
	all, err := execRepo.ListByProject(ctx, "default", 100)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListByProject expected 3 execs, got %d", len(all))
	}

	got, err := execRepo.ListByProjectExcludingChat(ctx, "default", 100)
	if err != nil {
		t.Fatalf("ListByProjectExcludingChat: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if ids[chatExecID] {
		t.Errorf("chat execution %s must be excluded from memory consolidation source", chatExecID)
	}
	if !ids[taskExecID] {
		t.Errorf("task execution %s must remain in memory consolidation source", taskExecID)
	}
	if !ids[threadExecID] {
		t.Errorf("task-thread follow-up execution %s must remain in memory consolidation source", threadExecID)
	}
}

func TestExecutionRepo_CompleteSuccessIfNoPendingSteeringReportsTerminalState(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)

	task := &models.Task{ProjectID: "default", Title: "Terminal Race Test", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent, err := agentRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("get default agent: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCancelled, "partial", "cancelled", 0, 10); err != nil {
		t.Fatalf("cancel execution: %v", err)
	}

	outcome, err := execRepo.CompleteSuccessIfNoPendingSteering(ctx, exec.ID, "late success", 1, 20)
	if err != nil {
		t.Fatalf("CompleteSuccessIfNoPendingSteering: %v", err)
	}
	if outcome != CompleteSuccessAlreadyTerminal {
		t.Fatalf("expected already-terminal outcome, got %s", outcome)
	}
	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.Status != models.ExecCancelled || stored.Output != "partial" || stored.ErrorMessage != "cancelled" {
		t.Fatalf("late success must not overwrite cancelled execution, got status=%s output=%q err=%q", stored.Status, stored.Output, stored.ErrorMessage)
	}
}

func TestExecutionRepo_ChatHistoryWindowReturnsLatestChronologicalAndBeforeCursor(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()
	agent, _ := agentRepo.GetDefault(ctx)

	created := make([]models.Execution, 0, 5)
	for i := 1; i <= 5; i++ {
		task := &models.Task{ProjectID: "default", Title: fmt.Sprintf("Chat %d", i), Category: models.CategoryChat, Status: models.StatusPending, Prompt: fmt.Sprintf("prompt-%d", i)}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("prompt-%d", i)}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec %d: %v", i, err)
		}
		created = append(created, *exec)
	}

	latest, err := execRepo.ListChatHistory(ctx, "default", 3)
	if err != nil {
		t.Fatalf("ListChatHistory: %v", err)
	}
	if got := promptsOf(latest); !reflect.DeepEqual(got, []string{"prompt-3", "prompt-4", "prompt-5"}) {
		t.Fatalf("expected latest chat window in chronological order, got %#v", got)
	}

	earlier, err := execRepo.ListChatHistoryBefore(ctx, "default", created[2].ID, 2)
	if err != nil {
		t.Fatalf("ListChatHistoryBefore: %v", err)
	}
	if got := promptsOf(earlier); !reflect.DeepEqual(got, []string{"prompt-1", "prompt-2"}) {
		t.Fatalf("expected earlier chat page, got %#v", got)
	}
}

func TestExecutionRepo_TaskExecutionWindowReturnsLatestChronologicalAndBeforeCursor(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()
	agent, _ := agentRepo.GetDefault(ctx)

	task := &models.Task{ProjectID: "default", Title: "Thread Task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "prompt"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	created := make([]models.Execution, 0, 5)
	for i := 1; i <= 5; i++ {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("turn-%d", i)}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create exec %d: %v", i, err)
		}
		created = append(created, *exec)
	}

	latest, err := execRepo.ListByTaskChronologicalLimit(ctx, task.ID, 3)
	if err != nil {
		t.Fatalf("ListByTaskChronologicalLimit: %v", err)
	}
	if got := promptsOf(latest); !reflect.DeepEqual(got, []string{"turn-3", "turn-4", "turn-5"}) {
		t.Fatalf("expected latest task window in chronological order, got %#v", got)
	}

	earlier, err := execRepo.ListByTaskChronologicalBefore(ctx, task.ID, created[2].ID, 2)
	if err != nil {
		t.Fatalf("ListByTaskChronologicalBefore: %v", err)
	}
	if got := promptsOf(earlier); !reflect.DeepEqual(got, []string{"turn-1", "turn-2"}) {
		t.Fatalf("expected earlier task page, got %#v", got)
	}
}

func promptsOf(execs []models.Execution) []string {
	out := make([]string, len(execs))
	for i, exec := range execs {
		out[i] = exec.PromptSent
	}
	return out
}
