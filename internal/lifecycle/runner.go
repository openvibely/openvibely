package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// HookInput is the structured payload the runner gives every hook execution.
// SkillBody is set when the runner pre-loaded the configured skill markdown.
type HookInput struct {
	When            models.LifecycleWhen           `json:"when"`
	TaskID          string                         `json:"task_id"`
	TaskRunID       string                         `json:"task_run_id"`
	ProjectID       string                         `json:"project_id,omitempty"`
	TaskTitle       string                         `json:"task_title,omitempty"`
	TaskPrompt      string                         `json:"task_prompt,omitempty"`
	WorkDir         string                         `json:"work_dir,omitempty"`
	ActiveModeAgent string                         `json:"active_mode_agent,omitempty"`
	SkillKey        string                         `json:"skill_key,omitempty"`
	SkillBody       string                         `json:"skill_body,omitempty"`
	PromptOverride  string                         `json:"prompt_override,omitempty"`
	OutputContract  models.LifecycleOutputContract `json:"output_contract,omitempty"`
	PreviousOutputs []HookOutput                   `json:"previous_outputs,omitempty"`
	Extras          map[string]any                 `json:"extras,omitempty"`
}

// HookOutput is what a hook returns. The runner validates Payload against the
// configured OutputContract before persisting/applying it.
type HookOutput struct {
	HookID         string                         `json:"hook_id"`
	AgentID        string                         `json:"agent_id"`
	SkillKey       string                         `json:"skill_key"`
	OutputContract models.LifecycleOutputContract `json:"output_contract"`
	Payload        json.RawMessage                `json:"payload"`
	Error          string                         `json:"error,omitempty"`
	// ExecutionID is the lifecycle_executions row ID for this output, used by
	// callers that need to patch the stored output_json (e.g. always-use merge).
	ExecutionID string `json:"execution_id,omitempty"`
}

const lifecycleExecutionFinalStatusTimeout = 10 * time.Second

// HookInvoker runs one agent skill and returns its raw output for validation.
// Implementations typically dispatch to an LLM (for system agents) or to a
// pre-registered Go executor (for tests and built-in skills).
type HookInvoker interface {
	Invoke(ctx context.Context, hook models.AgentLifecycleHook, input HookInput) (json.RawMessage, error)
}

// SkillResolver loads the skill body for a lifecycle hook. Hook skills are
// resolved under the hook owner's agent-owned skill folder; task-turn skill
// selection uses a separate scoped catalog before the normal agent run.
type SkillResolver interface {
	ResolveSkill(ctx context.Context, hook models.AgentLifecycleHook) (body string, err error)
}

// HookStore persists lifecycle hook configuration and lifecycle executions.
//
// FindExecutionByIdempotencyKey lets the runner deduplicate retried hook runs;
// implementations may return sql.ErrNoRows (or any error) to indicate no row
// is present, in which case the runner proceeds normally.
type HookStore interface {
	HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error)
	CreateExecution(ctx context.Context, e *models.LifecycleExecution) error
	UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error
	FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error)
}

// EffectiveMode is the resolved mode handed to the task runner for the current
// turn. Auto-routed modes are transient and should not become the task's saved
// assigned agent.
type EffectiveMode struct {
	AgentID      string         `json:"agent_id"`
	AgentKey     string         `json:"agent_key"`
	DisplayName  string         `json:"display_name,omitempty"`
	SystemPrompt string         `json:"system_prompt,omitempty"`
	Source       SelectedMode   `json:"source"`
	Overlays     map[string]any `json:"overlays,omitempty"`
}

// Runner owns lifecycle timing. It does not start new `when` values from
// hook executions, which prevents recursion (see runbook).
type HookInputCustomizer func(ctx context.Context, hook models.AgentLifecycleHook, input HookInput) HookInput

type HookExecutionStartedObserver func(ctx context.Context, hook models.AgentLifecycleHook, input HookInput, exec models.LifecycleExecution)

// LifecycleExecutionChangedObserver receives a project-scoped notification after
// a lifecycle execution row is durably created or finalized.
type LifecycleExecutionChangedObserver func(ctx context.Context, projectID string, exec models.LifecycleExecution)

type Runner struct {
	store                    HookStore
	invoker                  HookInvoker
	resolver                 SkillResolver
	logger                   *log.Logger
	inputCustomizer          HookInputCustomizer
	executionStartedObserver HookExecutionStartedObserver
	executionChangedObserver LifecycleExecutionChangedObserver
}

// NewRunner constructs a runner. invoker, resolver, and modes may be nil for
// tests that do not need to drive hook bodies or resolve modes.
func NewRunner(store HookStore, invoker HookInvoker, resolver SkillResolver) *Runner {
	return &Runner{
		store:    store,
		invoker:  invoker,
		resolver: resolver,
		logger:   log.Default(),
	}
}

func (r *Runner) SetInputCustomizer(customizer HookInputCustomizer) {
	if r == nil {
		return
	}
	r.inputCustomizer = customizer
}

func (r *Runner) SetExecutionStartedObserver(observer HookExecutionStartedObserver) {
	if r == nil {
		return
	}
	r.executionStartedObserver = observer
}

// SetExecutionChangedObserver registers a callback for durable lifecycle
// execution creation and finalization notifications.
func (r *Runner) SetExecutionChangedObserver(observer LifecycleExecutionChangedObserver) {
	if r == nil {
		return
	}
	r.executionChangedObserver = observer
}

func (r *Runner) notifyExecutionChanged(ctx context.Context, projectID string, exec models.LifecycleExecution) {
	if r == nil || r.executionChangedObserver == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Printf("[lifecycle] execution changed observer panic: %v", rec)
		}
	}()
	r.executionChangedObserver(ctx, projectID, exec)
}

// SlotResult bundles every hook output produced inside one `when` slot.
type SlotResult struct {
	When    models.LifecycleWhen
	Outputs []HookOutput
}

// insideHookKey is set on the context by runHook so that any nested attempt
// to start another `when` value can be rejected. Only the task runner enters
// `when` values; child agent executions never start new ones (runbook §
// Execution And Queueing).
type insideHookCtxKey struct{}

func markInsideHook(ctx context.Context) context.Context {
	return context.WithValue(ctx, insideHookCtxKey{}, true)
}

// IsInsideHook reports whether ctx is currently executing inside a lifecycle
// hook. Callers that dispatch nested work can use this to refuse recursion.
func IsInsideHook(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(insideHookCtxKey{}).(bool)
	return v
}

// RunSlot executes all enabled hooks for the supplied `when` value. Blocking
// hooks are awaited; non-blocking hooks run in parallel and the runner waits
// for them so the caller observes a consistent SlotResult. Failures are
// surfaced per-hook in HookOutput.Error and do not abort the slot unless the
// runner is explicitly asked to fail (see runbook failure-policy guidance).
//
// Hook executions never themselves trigger another `when` value; if RunSlot
// is called while ctx is already inside a hook, the call is refused.
func (r *Runner) RunSlot(ctx context.Context, when models.LifecycleWhen, input HookInput) (SlotResult, error) {
	return r.RunSlotFiltered(ctx, when, input, nil)
}

// RunSlotFiltered is RunSlot with an optional per-hook predicate. Hooks rejected
// by include are not invoked or recorded. A nil predicate includes every hook.
func (r *Runner) RunSlotFiltered(ctx context.Context, when models.LifecycleWhen, input HookInput, include func(models.AgentLifecycleHook) bool) (SlotResult, error) {
	if r == nil {
		return SlotResult{When: when}, errors.New("lifecycle: nil runner")
	}
	if IsInsideHook(ctx) {
		return SlotResult{When: when}, fmt.Errorf("lifecycle: cannot start %s slot from inside another hook", when)
	}
	hooks, err := r.store.HooksForWhen(ctx, when)
	if err != nil {
		return SlotResult{When: when}, err
	}
	if include != nil {
		filtered := make([]models.AgentLifecycleHook, 0, len(hooks))
		for _, hook := range hooks {
			if include(hook) {
				filtered = append(filtered, hook)
			}
		}
		hooks = filtered
	}
	if len(hooks) == 0 {
		r.logger.Printf("[lifecycle] slot=%s task=%s hooks=0", when, input.TaskID)
	}
	// Deterministic ordering: blocking hooks first, then by agent/created order.
	sort.SliceStable(hooks, func(i, j int) bool {
		if hooks[i].Blocking != hooks[j].Blocking {
			return hooks[i].Blocking
		}
		return hooks[i].ID < hooks[j].ID
	})
	input.When = when

	var (
		mu      sync.Mutex
		results []HookOutput
		wg      sync.WaitGroup
	)

	runOne := func(hook models.AgentLifecycleHook) HookOutput {
		out := r.runHook(ctx, hook, input)
		mu.Lock()
		results = append(results, out)
		mu.Unlock()
		return out
	}

	for _, hook := range hooks {
		if !hook.Enabled {
			continue
		}
		hook := hook
		if hook.Blocking {
			runOne(hook)
			// Make this output available to later hooks in the same slot.
			input.PreviousOutputs = append([]HookOutput(nil), results...)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// runHook recovers panics internally so the execution row is
			// always persisted with the failure reason.
			runOne(hook)
		}()
	}
	wg.Wait()
	// Preserve blocking-first order: blocking hook outputs land in `results`
	// synchronously before any non-blocking ones can race in, so a stable sort
	// on (blocking desc, hook id asc) gives a deterministic order without
	// destroying the blocking-first invariant MergeContextBlocks relies on.
	hookOrder := make(map[string]int, len(hooks))
	for i, h := range hooks {
		hookOrder[h.ID] = i
	}
	sort.SliceStable(results, func(i, j int) bool {
		return hookOrder[results[i].HookID] < hookOrder[results[j].HookID]
	})
	return SlotResult{When: when, Outputs: results}, nil
}

// runHook performs the per-hook lifecycle: prepare input, record execution,
// invoke skill, validate output, persist result. The hook invoker runs with a
// context flagged as "inside hook" so any RunSlot call from inside is refused.
//
// A panic inside the invoker is recovered, recorded as a failed execution,
// and returned as a HookOutput with Error set; the slot continues.
func (r *Runner) runHook(ctx context.Context, hook models.AgentLifecycleHook, input HookInput) (out HookOutput) {
	hookInput := input
	if r.inputCustomizer != nil {
		hookInput = r.inputCustomizer(ctx, hook, hookInput)
	}
	hookInput.SkillKey = hook.SkillKey
	hookInput.PromptOverride = hook.PromptOverride
	hookInput.OutputContract = hook.OutputContract

	inputJSON := ""
	if raw, err := json.Marshal(sanitizeHookInputForStorage(hookInput)); err == nil {
		inputJSON = string(raw)
	}
	idempotencyKey := buildIdempotencyKey(input.TaskRunID, hook.ID, inputJSON)

	// If we already have a completed execution for this (task_run, hook,
	// input) tuple, return it instead of invoking again. Failed/running rows
	// fall through so a retry can produce a fresh attempt.
	if idempotencyKey != "" {
		if existing, err := r.store.FindExecutionByIdempotencyKey(ctx, idempotencyKey); err == nil && existing != nil {
			if existing.Status == models.LifecycleExecCompleted {
				r.logger.Printf("[lifecycle] reuse completed execution task=%s when=%s hook=%s agent=%s skill=%s exec=%s", input.TaskID, hook.When, hook.ID, hook.AgentID, hook.SkillKey, existing.ID)
				return HookOutput{
					HookID:         hook.ID,
					AgentID:        hook.AgentID,
					SkillKey:       hook.SkillKey,
					OutputContract: hook.OutputContract,
					Payload:        json.RawMessage(existing.OutputJSON),
				}
			}
		}
	}

	r.logger.Printf("[lifecycle] hook start task=%s when=%s hook=%s agent=%s skill=%s blocking=%t contract=%s", input.TaskID, hook.When, hook.ID, hook.AgentID, hook.SkillKey, hook.Blocking, hook.OutputContract)

	exec := models.LifecycleExecution{
		TaskID:          input.TaskID,
		TaskRunID:       input.TaskRunID,
		AgentID:         hook.AgentID,
		When:            hook.When,
		LifecycleHookID: stringPtr(hook.ID),
		SkillKey:        hook.SkillKey,
		OutputContract:  hook.OutputContract,
		Status:          models.LifecycleExecRunning,
		AttemptCount:    1,
		InputJSON:       inputJSON,
		IdempotencyKey:  idempotencyKey,
	}
	if err := r.store.CreateExecution(ctx, &exec); err != nil {
		r.logger.Printf("[lifecycle] create execution failed: %v", err)
	} else {
		r.notifyExecutionChanged(ctx, hookInput.ProjectID, exec)
		if r.executionStartedObserver != nil {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						r.logger.Printf("[lifecycle] execution observer panic: %v", rec)
					}
				}()
				r.executionStartedObserver(ctx, hook, hookInput, exec)
			}()
		}
	}
	var traceRecorder *TraceRecorder
	if eventStore, ok := r.store.(ExecutionEventAppender); ok {
		traceRecorder = NewTraceRecorder(eventStore, exec.ID, r.logger)
	}
	if traceRecorder != nil {
		traceRecorder.Record(ctx, "execution_started", map[string]any{
			"task_id":           exec.TaskID,
			"task_run_id":       exec.TaskRunID,
			"agent_id":          exec.AgentID,
			"when":              string(exec.When),
			"lifecycle_hook_id": hook.ID,
			"skill_key":         exec.SkillKey,
			"output_contract":   string(exec.OutputContract),
		})
		if inputJSON != "" {
			traceRecorder.Record(ctx, "input_snapshot", map[string]any{"input_json": inputJSON})
		}
	}

	// Recover panics inside the invoker so the slot can continue and the
	// execution row is never left stuck in "running".
	defer func() {
		if rec := recover(); rec != nil {
			panicErr := fmt.Errorf("hook panic: %v", rec)
			out = r.finishHook(ctx, &exec, hook, hookInput.ProjectID, nil, panicErr)
		}
	}()

	if r.resolver != nil && hook.SkillKey != "" {
		body, err := r.resolver.ResolveSkill(ctx, hook)
		if err != nil {
			return r.finishHook(ctx, &exec, hook, hookInput.ProjectID, nil, fmt.Errorf("resolve skill: %w", err))
		}
		hookInput.SkillBody = body
	}

	if r.invoker == nil {
		return r.finishHook(ctx, &exec, hook, hookInput.ProjectID, nil, errors.New("no hook invoker configured"))
	}

	// Mark the context as "inside hook" so any RunSlot call from inside the
	// invoker is rejected. Attach the trace recorder after the execution row has
	// an ID so hidden reviewer/tool activity can be inspected from the task UI.
	hookCtx := markInsideHook(ctx)
	hookCtx = WithTraceRecorder(hookCtx, traceRecorder)
	hookCtx = llmcontracts.WithRuntimeToolTraceRecorder(hookCtx, traceRecorder)
	raw, err := r.invoker.Invoke(hookCtx, hook, hookInput)
	if err != nil {
		return r.finishHook(hookCtx, &exec, hook, hookInput.ProjectID, raw, err)
	}
	if err := ValidateOutput(hook.OutputContract, raw); err != nil {
		RecordTraceEvent(hookCtx, "validation_failed", map[string]any{
			"output_contract": string(hook.OutputContract),
			"error":           err.Error(),
		})
		return r.finishHook(hookCtx, &exec, hook, hookInput.ProjectID, raw, fmt.Errorf("validate output: %w", err))
	}
	RecordTraceEvent(hookCtx, "validation_passed", map[string]any{"output_contract": string(hook.OutputContract)})
	return r.finishHook(hookCtx, &exec, hook, hookInput.ProjectID, raw, nil)
}

// buildIdempotencyKey returns the runbook's task_run_id + hook_id + snapshot
// hash. Empty inputs return "" so we never collide on missing data.
func buildIdempotencyKey(taskRunID, hookID, snapshot string) string {
	if taskRunID == "" || hookID == "" {
		return ""
	}
	h := sha256.Sum256([]byte(snapshot))
	return taskRunID + ":" + hookID + ":" + hex.EncodeToString(h[:8])
}

func (r *Runner) finishHook(ctx context.Context, exec *models.LifecycleExecution, hook models.AgentLifecycleHook, projectID string, raw json.RawMessage, hookErr error) HookOutput {
	now := time.Now().UTC()
	duration := time.Duration(0)
	if !exec.StartedAt.IsZero() {
		duration = now.Sub(exec.StartedAt)
	}
	exec.CompletedAt = &now
	if hookErr != nil {
		exec.Status = models.LifecycleExecFailed
		exec.Error = hookErr.Error()
	} else {
		exec.Status = models.LifecycleExecCompleted
	}
	if len(raw) > 0 {
		exec.OutputJSON = string(raw)
	}
	RecordTraceEvent(ctx, "execution_finished", map[string]any{
		"status":       string(exec.Status),
		"error":        exec.Error,
		"output_json":  exec.OutputJSON,
		"duration_ms":  duration.Milliseconds(),
		"completed_at": now,
	})
	updateCtx, cancel := context.WithTimeout(context.Background(), lifecycleExecutionFinalStatusTimeout)
	defer cancel()
	if err := r.store.UpdateExecution(updateCtx, exec); err != nil {
		r.logger.Printf("[lifecycle] update execution failed task=%s when=%s hook=%s exec=%s: %v", exec.TaskID, hook.When, hook.ID, exec.ID, err)
	} else {
		r.notifyExecutionChanged(updateCtx, projectID, *exec)
	}
	if hookErr != nil {
		r.logger.Printf("[lifecycle] hook finish task=%s when=%s hook=%s agent=%s skill=%s exec=%s status=%s duration=%s error=%q", exec.TaskID, hook.When, hook.ID, hook.AgentID, hook.SkillKey, exec.ID, exec.Status, duration, hookErr.Error())
	} else {
		r.logger.Printf("[lifecycle] hook finish task=%s when=%s hook=%s agent=%s skill=%s exec=%s status=%s duration=%s output_bytes=%d", exec.TaskID, hook.When, hook.ID, hook.AgentID, hook.SkillKey, exec.ID, exec.Status, duration, len(raw))
	}
	out := HookOutput{
		HookID:         hook.ID,
		AgentID:        hook.AgentID,
		SkillKey:       hook.SkillKey,
		OutputContract: hook.OutputContract,
		Payload:        raw,
		ExecutionID:    exec.ID,
	}
	if hookErr != nil {
		out.Error = hookErr.Error()
	}
	return out
}

// ValidateOutput dispatches to the named contract validator. An empty
// contract is accepted (the hook is treated as side-effect only).
func ValidateOutput(contract models.LifecycleOutputContract, raw json.RawMessage) error {
	if contract == "" {
		return nil
	}
	switch contract {
	case models.OutputContractSelectedMode:
		_, err := ValidateSelectedMode(raw)
		return err
	case models.OutputContractSelectedSkills:
		_, err := ValidateSelectedSkills(raw)
		return err
	case models.OutputContractSelectedMemories:
		_, err := ValidateSelectedMemories(raw)
		return err
	case models.OutputContractContextBlock:
		_, err := ValidateContextBlock(raw)
		return err
	case models.OutputContractActivitySummary:
		_, err := ValidateActivitySummary(raw)
		return err
	case models.OutputContractLearningSummary:
		_, err := ValidateLearningSummary(raw)
		return err
	case models.OutputContractLibraryUpdateSummary:
		_, err := ValidateLibraryUpdateSummary(raw)
		return err
	default:
		return fmt.Errorf("unknown output contract %q", contract)
	}
}

// MergeContextBlocks combines multiple context_block outputs deterministically
// with source labels per the runbook ("append them with source labels").
func MergeContextBlocks(outputs []HookOutput) string {
	pieces := make([]string, 0, len(outputs))
	for _, o := range outputs {
		if o.OutputContract != models.OutputContractContextBlock || len(o.Payload) == 0 {
			continue
		}
		cb, err := ValidateContextBlock(o.Payload)
		if err != nil || cb.Content == "" {
			continue
		}
		label := o.SkillKey
		if label == "" {
			label = o.AgentID
		}
		pieces = append(pieces, fmt.Sprintf("[%s]\n%s", label, cb.Content))
	}
	return joinNonEmpty(pieces, "\n\n")
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func sanitizeHookInputForStorage(input HookInput) HookInput {
	input.PreviousOutputs = sanitizeHookOutputsForPrompt(input.PreviousOutputs)
	return input
}

func stringPtr(s string) *string { return &s }

// TaskModeRunner runs the active effective agent for the task_mode slot.
// Implementations execute the real model turn and return its summary. The
// runner records start/finish as a lifecycle execution row so task_mode
// activity is visible alongside route_task/before_run/after_complete rows.
type TaskModeRunner interface {
	RunTaskMode(ctx context.Context, task TaskModeInput) (TaskModeResult, error)
}

// TaskModeInput is the prepared payload the task runner hands to the active
// effective agent. PreparedContext is the merged context_block output from
// before_run hooks plus any other context the runner injects.
type TaskModeInput struct {
	TaskID          string
	TaskRunID       string
	ProjectID       string
	EffectiveMode   EffectiveMode
	PreparedContext string
}

// TaskModeResult is the output of one task_mode run. Summary is recorded on
// the lifecycle_executions row; OutputJSON, when present, is stored verbatim.
type TaskModeResult struct {
	Summary    string
	OutputJSON string
}

// RunTaskMode records the start/finish of the active task_mode execution as a
// lifecycle execution row. It does not impose its own output contract; the
// task runner is responsible for the actual work. Returns the result the
// supplied TaskModeRunner produced.
//
// If runner is nil (tests, or callers that don't want bookkeeping), this
// returns a zero TaskModeResult and no error.
func (r *Runner) RunTaskMode(ctx context.Context, runner TaskModeRunner, in TaskModeInput) (TaskModeResult, error) {
	if r == nil {
		return TaskModeResult{}, errors.New("lifecycle: nil runner")
	}
	if IsInsideHook(ctx) {
		return TaskModeResult{}, errors.New("lifecycle: cannot start task_mode from inside a hook")
	}
	exec := models.LifecycleExecution{
		TaskID:       in.TaskID,
		TaskRunID:    in.TaskRunID,
		AgentID:      in.EffectiveMode.AgentID,
		When:         models.LifecycleTaskMode,
		Status:       models.LifecycleExecRunning,
		AttemptCount: 1,
	}
	if err := r.store.CreateExecution(ctx, &exec); err != nil {
		r.logger.Printf("[lifecycle] create task_mode execution failed: %v", err)
	} else {
		r.notifyExecutionChanged(ctx, in.ProjectID, exec)
	}
	if runner == nil {
		// No task-mode runner supplied: mark skipped so the row is closed.
		now := time.Now().UTC()
		exec.CompletedAt = &now
		exec.Status = models.LifecycleExecSkipped
		updateCtx, cancel := context.WithTimeout(context.Background(), lifecycleExecutionFinalStatusTimeout)
		defer cancel()
		if err := r.store.UpdateExecution(updateCtx, &exec); err != nil {
			r.logger.Printf("[lifecycle] update task_mode execution failed: %v", err)
		} else {
			r.notifyExecutionChanged(updateCtx, in.ProjectID, exec)
		}
		return TaskModeResult{}, nil
	}
	result, err := runner.RunTaskMode(ctx, in)
	now := time.Now().UTC()
	exec.CompletedAt = &now
	if err != nil {
		exec.Status = models.LifecycleExecFailed
		exec.Error = err.Error()
	} else {
		exec.Status = models.LifecycleExecCompleted
		exec.OutputJSON = result.OutputJSON
	}
	updateCtx, cancel := context.WithTimeout(context.Background(), lifecycleExecutionFinalStatusTimeout)
	defer cancel()
	if err := r.store.UpdateExecution(updateCtx, &exec); err != nil {
		r.logger.Printf("[lifecycle] update task_mode execution failed: %v", err)
	} else {
		r.notifyExecutionChanged(updateCtx, in.ProjectID, exec)
	}
	return result, err
}
