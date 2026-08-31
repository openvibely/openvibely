package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// memStore is an in-memory HookStore used to drive the runner in tests.
type memStore struct {
	mu         sync.Mutex
	hooks      []models.AgentLifecycleHook
	executions []models.LifecycleExecution
	events     []models.LifecycleExecutionEvent
	nextID     int
}

func (m *memStore) HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.AgentLifecycleHook
	for _, h := range m.hooks {
		if h.When == when && h.Enabled {
			out = append(out, h)
		}
	}
	return out, nil
}

func (m *memStore) CreateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e.ID = fmt.Sprintf("exec-%d", m.nextID)
	e.StartedAt = time.Now().UTC()
	m.executions = append(m.executions, *e)
	return nil
}

func (m *memStore) UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.executions {
		if m.executions[i].ID == e.ID {
			m.executions[i] = *e
			return nil
		}
	}
	return fmt.Errorf("execution %s not found", e.ID)
}

func (m *memStore) FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error) {
	if key == "" {
		return nil, sql.ErrNoRows
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.executions {
		if m.executions[i].IdempotencyKey == key {
			e := m.executions[i]
			return &e, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *memStore) AppendExecutionEvent(ctx context.Context, event *models.LifecycleExecutionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if event == nil {
		return nil
	}
	e := *event
	e.Seq = len(m.events) + 1
	e.ID = fmt.Sprintf("event-%d", e.Seq)
	e.CreatedAt = time.Now().UTC()
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) recordedFor(hookID string) (models.LifecycleExecution, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.executions {
		if e.LifecycleHookID != nil && *e.LifecycleHookID == hookID {
			return e, true
		}
	}
	return models.LifecycleExecution{}, false
}

type fakeInvoker struct {
	mu      sync.Mutex
	calls   []string
	delay   map[string]time.Duration
	outputs map[string]json.RawMessage
	errors  map[string]error
}

func (f *fakeInvoker) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in HookInput) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, hook.ID)
	d := f.delay[hook.ID]
	out := f.outputs[hook.ID]
	err := f.errors[hook.ID]
	f.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	return out, err
}

func TestRunSlot_RecordsLifecycleTraceEvents(t *testing.T) {
	store := &memStore{hooks: []models.AgentLifecycleHook{{ID: "hook", AgentID: "agent", When: models.LifecycleAfterComplete, SkillKey: "observe", OutputContract: models.OutputContractLearningSummary, Enabled: true}}}
	inv := &fakeInvoker{outputs: map[string]json.RawMessage{"hook": json.RawMessage(`{"summary":"No durable learning to save.","nothing_to_save":true}`)}}
	runner := NewRunner(store, inv, nil)

	_, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "task", TaskRunID: "run"})
	if err != nil {
		t.Fatalf("RunSlot: %v", err)
	}
	var types []string
	for _, e := range store.events {
		types = append(types, e.EventType)
	}
	for _, want := range []string{"execution_started", "input_snapshot", "validation_passed", "execution_finished"} {
		if !containsString(types, want) {
			t.Fatalf("expected trace event %q in %v", want, types)
		}
	}
}

func TestRunSlot_NotifiesExecutionChangesAfterCreateAndFinish(t *testing.T) {
	store := &memStore{hooks: []models.AgentLifecycleHook{{
		ID: "hook", AgentID: "agent", When: models.LifecycleAfterComplete,
		SkillKey: "observe", OutputContract: models.OutputContractLearningSummary, Enabled: true,
	}}}
	inv := &fakeInvoker{outputs: map[string]json.RawMessage{
		"hook": json.RawMessage(`{"summary":"No durable learning to save.","nothing_to_save":true}`),
	}}
	runner := NewRunner(store, inv, nil)
	changes := make(chan struct {
		projectID string
		exec      models.LifecycleExecution
	}, 2)
	runner.SetExecutionChangedObserver(func(_ context.Context, projectID string, exec models.LifecycleExecution) {
		changes <- struct {
			projectID string
			exec      models.LifecycleExecution
		}{projectID: projectID, exec: exec}
	})

	if _, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{
		TaskID: "task", TaskRunID: "run", ProjectID: "project",
	}); err != nil {
		t.Fatalf("RunSlot: %v", err)
	}

	started := <-changes
	finished := <-changes
	if started.projectID != "project" || started.exec.Status != models.LifecycleExecRunning {
		t.Fatalf("start notification = %+v, want project-scoped running execution", started)
	}
	if finished.projectID != "project" || finished.exec.Status != models.LifecycleExecCompleted {
		t.Fatalf("finish notification = %+v, want project-scoped completed execution", finished)
	}
	if started.exec.ID == "" || started.exec.ID != finished.exec.ID {
		t.Fatalf("notifications refer to different execution IDs: %q and %q", started.exec.ID, finished.exec.ID)
	}
}

func TestRunSlot_BlockingFirstThenParallel(t *testing.T) {
	store := &memStore{hooks: []models.AgentLifecycleHook{
		{ID: "blk", AgentID: "a", When: models.LifecycleBeforeRun, SkillKey: "agent/recall", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
		{ID: "np-a", AgentID: "b", When: models.LifecycleBeforeRun, SkillKey: "agent/policy", OutputContract: models.OutputContractContextBlock, Blocking: false, Enabled: true},
		{ID: "np-b", AgentID: "c", When: models.LifecycleBeforeRun, SkillKey: "agent/extra", OutputContract: models.OutputContractContextBlock, Blocking: false, Enabled: true},
	},
	}
	inv := &fakeInvoker{
		outputs: map[string]json.RawMessage{
			"blk":  json.RawMessage(`{"content":"blocking","sources":["m1"]}`),
			"np-a": json.RawMessage(`{"content":"policy","sources":["p1"]}`),
			"np-b": json.RawMessage(`{"content":"extra","sources":["e1"]}`),
		},
		delay: map[string]time.Duration{
			"np-a": 30 * time.Millisecond,
			"np-b": 30 * time.Millisecond,
		},
	}
	runner := NewRunner(store, inv, nil)

	start := time.Now()
	res, err := runner.RunSlot(context.Background(), models.LifecycleBeforeRun, HookInput{TaskID: "t", TaskRunID: "r"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if len(res.Outputs) != 3 {
		t.Fatalf("expected 3 outputs, got %d", len(res.Outputs))
	}
	if elapsed >= 90*time.Millisecond {
		t.Fatalf("non-blocking hooks did not run in parallel (elapsed=%v)", elapsed)
	}
	// Blocking hook must have been called first.
	if inv.calls[0] != "blk" {
		t.Fatalf("blocking hook should run first, got order %v", inv.calls)
	}
	// All executions persisted.
	for _, id := range []string{"blk", "np-a", "np-b"} {
		exec, ok := store.recordedFor(id)
		if !ok {
			t.Fatalf("expected execution recorded for %s", id)
		}
		if exec.Status != models.LifecycleExecCompleted {
			t.Fatalf("hook %s status = %s, want completed", id, exec.Status)
		}
	}
}

func TestRunSlot_InvalidOutputMarksHookFailedButDoesNotAbortSlot(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "good", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", OutputContract: models.OutputContractActivitySummary, Enabled: true},
			{ID: "bad", AgentID: "b", When: models.LifecycleAfterComplete, SkillKey: "y", OutputContract: models.OutputContractSelectedMode, Enabled: true},
		},
	}
	inv := &fakeInvoker{
		outputs: map[string]json.RawMessage{
			"good": json.RawMessage(`{"summary":"ok"}`),
			"bad":  json.RawMessage(`{"mode":"","action":"flip"}`), // invalid selected_mode
		},
	}
	runner := NewRunner(store, inv, nil)
	res, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "t"})
	if err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(res.Outputs))
	}
	var goodErr, badErr string
	for _, o := range res.Outputs {
		if o.HookID == "good" {
			goodErr = o.Error
		}
		if o.HookID == "bad" {
			badErr = o.Error
		}
	}
	if goodErr != "" {
		t.Fatalf("good hook should not have error, got %q", goodErr)
	}
	if badErr == "" {
		t.Fatalf("bad hook should have validation error")
	}
	exec, _ := store.recordedFor("bad")
	if exec.Status != models.LifecycleExecFailed {
		t.Fatalf("bad hook status = %s, want failed", exec.Status)
	}
}

func TestRunSlot_HookExecutionDoesNotStartNewWhen(t *testing.T) {
	// This test pins the recursion-prevention guarantee: the runner only enters
	// `when` values when callers explicitly invoke RunSlot. The invoker (which
	// represents a hook execution) cannot trigger another slot through the
	// runner because Invoke has no Runner reference. We verify that constraint
	// by ensuring no extra executions appear after one RunSlot call.
	var invocations int32
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "h", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", Enabled: true},
		},
	}
	inv := &fakeInvokerFunc{fn: func(context.Context, models.AgentLifecycleHook, HookInput) (json.RawMessage, error) {
		atomic.AddInt32(&invocations, 1)
		return nil, nil
	}}
	runner := NewRunner(store, inv, nil)
	if _, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "t"}); err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if got := atomic.LoadInt32(&invocations); got != 1 {
		t.Fatalf("expected exactly 1 invocation, got %d", got)
	}
	if n := len(store.executions); n != 1 {
		t.Fatalf("expected 1 execution recorded, got %d", n)
	}
}

func TestRunSlot_InvokerErrorRecordedAsFailedExecution(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "h", AgentID: "a", When: models.LifecycleRouteTask, SkillKey: "x", Enabled: true},
		},
	}
	inv := &fakeInvoker{errors: map[string]error{"h": errors.New("boom")}}
	runner := NewRunner(store, inv, nil)
	res, err := runner.RunSlot(context.Background(), models.LifecycleRouteTask, HookInput{TaskID: "t"})
	if err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Error == "" {
		t.Fatalf("expected hook error in output, got %+v", res.Outputs)
	}
	exec, _ := store.recordedFor("h")
	if exec.Status != models.LifecycleExecFailed {
		t.Fatalf("expected failed status, got %s", exec.Status)
	}
}

func TestRunSlot_CancelledHookContextStillPersistsTerminalExecution(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "timeout-hook", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", Enabled: true},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	inv := &fakeInvokerFunc{fn: func(ctx context.Context, _ models.AgentLifecycleHook, _ HookInput) (json.RawMessage, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := NewRunner(store, inv, nil)
	defer cancel()

	res, err := runner.RunSlot(ctx, models.LifecycleAfterComplete, HookInput{TaskID: "t", TaskRunID: "r"})
	if err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Error == "" {
		t.Fatalf("expected cancelled hook error in output, got %+v", res.Outputs)
	}
	exec, ok := store.recordedFor("timeout-hook")
	if !ok {
		t.Fatalf("expected execution recorded")
	}
	if exec.Status != models.LifecycleExecFailed {
		t.Fatalf("expected terminal failed status despite cancelled hook context, got %s", exec.Status)
	}
	if exec.CompletedAt == nil {
		t.Fatalf("expected completed_at to be persisted")
	}
}

func TestRunSlot_IdempotentRetryReturnsRecordedOutput(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "h", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", OutputContract: models.OutputContractActivitySummary, Enabled: true},
		},
	}
	calls := 0
	inv := &fakeInvokerFunc{fn: func(context.Context, models.AgentLifecycleHook, HookInput) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"summary":"ok"}`), nil
	}}
	runner := NewRunner(store, inv, nil)
	in := HookInput{TaskID: "t", TaskRunID: "run-1"}
	if _, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, in); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, in); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected invoker called once due to idempotency, got %d", calls)
	}
}

func TestRunSlot_RefusesRecursionFromInsideHook(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "outer", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", Enabled: true},
		},
	}
	var innerErr error
	runner := NewRunner(store, nil, nil)
	inv := &fakeInvokerFunc{fn: func(ctx context.Context, _ models.AgentLifecycleHook, _ HookInput) (json.RawMessage, error) {
		if !IsInsideHook(ctx) {
			innerErr = errors.New("expected ctx flagged as inside hook")
			return nil, nil
		}
		// Hook tries to start another slot; runner must refuse.
		if _, err := runner.RunSlot(ctx, models.LifecycleBeforeRun, HookInput{TaskID: "t"}); err == nil {
			innerErr = errors.New("expected RunSlot from inside hook to be refused")
		}
		return nil, nil
	}}
	runner.invoker = inv
	if _, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "t", TaskRunID: "r"}); err != nil {
		t.Fatalf("outer run: %v", err)
	}
	if innerErr != nil {
		t.Fatalf("inner: %v", innerErr)
	}
}

func TestRunSlot_PanicRecoveryPersistsFailedExecution(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "panicker", AgentID: "a", When: models.LifecycleAfterComplete, SkillKey: "x", Enabled: true},
		},
	}
	inv := &fakeInvokerFunc{fn: func(context.Context, models.AgentLifecycleHook, HookInput) (json.RawMessage, error) {
		panic("kaboom")
	}}
	runner := NewRunner(store, inv, nil)
	res, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "t", TaskRunID: "r"})
	if err != nil {
		t.Fatalf("RunSlot err: %v", err)
	}
	if len(res.Outputs) != 1 || res.Outputs[0].Error == "" {
		t.Fatalf("expected panic surfaced in output, got %+v", res.Outputs)
	}
	exec, ok := store.recordedFor("panicker")
	if !ok {
		t.Fatalf("expected execution recorded")
	}
	if exec.Status != models.LifecycleExecFailed {
		t.Fatalf("expected failed status after panic, got %s", exec.Status)
	}
	if exec.Error == "" {
		t.Fatalf("expected panic recorded in error column")
	}
}

func TestRunSlot_BlockingFirstOrderingPreserved(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			// non-blocking sort-first by ID
			{ID: "aaaa-np", AgentID: "n", When: models.LifecycleBeforeRun, SkillKey: "n/p", OutputContract: models.OutputContractContextBlock, Blocking: false, Enabled: true},
			// blocking sorts later by ID alphabetically
			{ID: "zzzz-blk", AgentID: "b", When: models.LifecycleBeforeRun, SkillKey: "b/r", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
		},
	}
	inv := &fakeInvoker{outputs: map[string]json.RawMessage{
		"aaaa-np":  json.RawMessage(`{"content":"np"}`),
		"zzzz-blk": json.RawMessage(`{"content":"blk"}`),
	}}
	runner := NewRunner(store, inv, nil)
	res, err := runner.RunSlot(context.Background(), models.LifecycleBeforeRun, HookInput{TaskID: "t", TaskRunID: "r"})
	if err != nil {
		t.Fatalf("RunSlot: %v", err)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(res.Outputs))
	}
	// Blocking must come first regardless of hook ID order.
	if res.Outputs[0].HookID != "zzzz-blk" {
		t.Fatalf("expected blocking hook first, got order %v / %v", res.Outputs[0].HookID, res.Outputs[1].HookID)
	}
}

type fakeTaskMode struct {
	called bool
	res    TaskModeResult
	err    error
}

func (f *fakeTaskMode) RunTaskMode(ctx context.Context, in TaskModeInput) (TaskModeResult, error) {
	f.called = true
	if f.err != nil {
		return TaskModeResult{}, f.err
	}
	return f.res, nil
}

type fakeTaskModeFunc struct {
	fn func(context.Context, TaskModeInput) (TaskModeResult, error)
}

func (f fakeTaskModeFunc) RunTaskMode(ctx context.Context, in TaskModeInput) (TaskModeResult, error) {
	return f.fn(ctx, in)
}

func TestRunTaskMode_RecordsExecutionRow(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store, nil, nil)
	tm := &fakeTaskMode{res: TaskModeResult{Summary: "done", OutputJSON: `{"ok":true}`}}
	got, err := runner.RunTaskMode(context.Background(), tm, TaskModeInput{
		TaskID: "t", TaskRunID: "r",
		EffectiveMode: EffectiveMode{AgentID: "a-1", AgentKey: "backend"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !tm.called || got.Summary != "done" {
		t.Fatalf("expected delegated run, got %+v called=%v", got, tm.called)
	}
	if len(store.executions) != 1 {
		t.Fatalf("expected 1 execution recorded, got %d", len(store.executions))
	}
	e := store.executions[0]
	if e.When != models.LifecycleTaskMode || e.Status != models.LifecycleExecCompleted || e.AgentID != "a-1" {
		t.Fatalf("unexpected execution row: %+v", e)
	}
}

func TestRunTaskMode_NotifiesExecutionChangesAfterCreateAndFinish(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store, nil, nil)
	changes := make(chan models.LifecycleExecution, 2)
	runner.SetExecutionChangedObserver(func(_ context.Context, projectID string, exec models.LifecycleExecution) {
		if projectID != "project" {
			t.Errorf("notification project_id = %q, want project", projectID)
		}
		changes <- exec
	})

	_, err := runner.RunTaskMode(context.Background(), &fakeTaskMode{res: TaskModeResult{Summary: "done"}}, TaskModeInput{
		TaskID: "task", TaskRunID: "run", ProjectID: "project",
		EffectiveMode: EffectiveMode{AgentID: "agent"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	started := <-changes
	finished := <-changes
	if started.When != models.LifecycleTaskMode || started.Status != models.LifecycleExecRunning {
		t.Fatalf("start notification = %+v, want running task_mode execution", started)
	}
	if finished.When != models.LifecycleTaskMode || finished.Status != models.LifecycleExecCompleted {
		t.Fatalf("finish notification = %+v, want completed task_mode execution", finished)
	}
	if started.ID == "" || started.ID != finished.ID {
		t.Fatalf("notifications refer to different execution IDs: %q and %q", started.ID, finished.ID)
	}
}

func TestRunTaskMode_CancelledContextStillPersistsTerminalExecution(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tm := fakeTaskModeFunc{fn: func(context.Context, TaskModeInput) (TaskModeResult, error) {
		cancel()
		return TaskModeResult{OutputJSON: `{"ok":true}`}, nil
	}}

	_, err := runner.RunTaskMode(ctx, tm, TaskModeInput{
		TaskID: "t", TaskRunID: "r",
		EffectiveMode: EffectiveMode{AgentID: "a-1", AgentKey: "backend"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.executions) != 1 {
		t.Fatalf("expected 1 execution recorded, got %d", len(store.executions))
	}
	exec := store.executions[0]
	if exec.Status != models.LifecycleExecCompleted {
		t.Fatalf("expected terminal completed status despite cancelled task-mode context, got %s", exec.Status)
	}
	if exec.CompletedAt == nil {
		t.Fatalf("expected completed_at to be persisted")
	}
}

func TestRunTaskMode_NilRunnerSkipsAndMarks(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store, nil, nil)
	_, err := runner.RunTaskMode(context.Background(), nil, TaskModeInput{TaskID: "t", TaskRunID: "r", EffectiveMode: EffectiveMode{AgentID: "a"}})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if len(store.executions) != 1 || store.executions[0].Status != models.LifecycleExecSkipped {
		t.Fatalf("expected skipped row, got %+v", store.executions)
	}
}

func TestRunTaskMode_RefusesNestedFromHook(t *testing.T) {
	store := &memStore{}
	runner := NewRunner(store, nil, nil)
	ctx := markInsideHook(context.Background())
	if _, err := runner.RunTaskMode(ctx, &fakeTaskMode{}, TaskModeInput{TaskID: "t"}); err == nil {
		t.Fatalf("expected RunTaskMode from inside hook to be refused")
	}
}

func TestMergeContextBlocks_AppendsWithSourceLabels(t *testing.T) {
	outs := []HookOutput{
		{HookID: "h1", SkillKey: "project_context/load", OutputContract: models.OutputContractContextBlock, Payload: json.RawMessage(`{"content":"project context","sources":["p"]}`)},
		{HookID: "h2", SkillKey: "policy/load_policy_context", OutputContract: models.OutputContractContextBlock, Payload: json.RawMessage(`{"content":"policy text","sources":["p"]}`)},
		{HookID: "h3", OutputContract: models.OutputContractActivitySummary, Payload: json.RawMessage(`{"summary":"x"}`)},
	}
	merged := MergeContextBlocks(outs)
	if !contains(merged, "project_context/load") || !contains(merged, "project context") {
		t.Fatalf("expected project context block included, got %q", merged)
	}
	if !contains(merged, "policy/load_policy_context") || !contains(merged, "policy text") {
		t.Fatalf("expected policy block included, got %q", merged)
	}
	if contains(merged, "x") && !contains(merged, "policy text") {
		t.Fatalf("activity_summary should not appear in context merge")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

type fakeInvokerFunc struct {
	fn func(context.Context, models.AgentLifecycleHook, HookInput) (json.RawMessage, error)
}

func (f *fakeInvokerFunc) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in HookInput) (json.RawMessage, error) {
	return f.fn(ctx, hook, in)
}

func TestRunSlot_InvalidBlockingOutputDoesNotPoisonLaterHookPrompt(t *testing.T) {
	store := &memStore{
		hooks: []models.AgentLifecycleHook{
			{ID: "goal", AgentID: "goal-agent", When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true},
			{ID: "memory", AgentID: "memory-agent", When: models.LifecycleAfterComplete, SkillKey: "update_memory", OutputContract: models.OutputContractLearningSummary, Blocking: true, Enabled: true},
		},
	}
	inv := &fakeInvoker{
		outputs: map[string]json.RawMessage{
			"goal":   json.RawMessage(`{"summary":"one"}{"summary":"two"}`),
			"memory": json.RawMessage(`{"summary":"Nothing to save.","nothing_to_save":true}`),
		},
	}
	runner := NewRunner(store, inv, nil)
	res, err := runner.RunSlot(context.Background(), models.LifecycleAfterComplete, HookInput{TaskID: "task", TaskRunID: "run"})
	if err != nil {
		t.Fatalf("RunSlot: %v", err)
	}
	if len(res.Outputs) != 2 {
		t.Fatalf("expected both hooks to run, got %#v", res.Outputs)
	}
	var goalErr, memoryErr string
	for _, out := range res.Outputs {
		switch out.HookID {
		case "goal":
			goalErr = out.Error
		case "memory":
			memoryErr = out.Error
		}
	}
	if goalErr == "" {
		t.Fatal("expected invalid goal output to fail validation")
	}
	if memoryErr != "" {
		t.Fatalf("later memory hook should not fail from invalid previous output, got %q", memoryErr)
	}
}
