package chatcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBuildRuntimeToolExecutor_UnknownToolFallsThrough(t *testing.T) {
	// Regression: grep_search, read_file, list_files are provider-native tools
	// that are NOT in the chatcontrol registry. The executor must return
	// handled=false so the provider's base executor can handle them.
	handlers := map[string]RuntimeActionHandler{}
	executor := BuildRuntimeToolExecutor(models.ChatModePlan, SurfaceWeb, handlers)

	for _, tool := range []string{"grep_search", "read_file", "list_files", "write_file", "edit_file", "bash"} {
		_, handled, _, err := executor(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil {
			t.Errorf("tool %q: unexpected error: %v", tool, err)
		}
		if handled {
			t.Errorf("tool %q: expected handled=false for non-registry tool so base executor handles it", tool)
		}
	}
}

func TestBuildRuntimeToolExecutor_ModeBlockedReturnsHandled(t *testing.T) {
	// Write actions blocked in plan mode should return handled=true with error.
	handlers := map[string]RuntimeActionHandler{
		"create_task": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "created", nil
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModePlan, SurfaceWeb, handlers)

	output, handled, isError, err := executor(context.Background(), "create_task", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for mode-blocked action")
	}
	if !isError {
		t.Fatal("expected isError=true for mode-blocked action")
	}
	if output == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestBuildRuntimeToolExecutorForActions_NonOwnedRegisteredActionFallsThrough(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{
		"send_message": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "sent", nil
		},
	}
	executor := BuildRuntimeToolExecutorForActions(models.ChatModeOrchestrate, SurfaceWeb, handlers, map[string]bool{"send_message": true})

	_, handled, isError, err := executor(context.Background(), "github_get_project_inbox", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled || isError {
		t.Fatalf("expected non-owned registered action to fall through, handled=%v isError=%v", handled, isError)
	}

	output, handled, isError, err := executor(context.Background(), "send_message", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected owned action error: %v", err)
	}
	if !handled || isError || output != "sent" {
		t.Fatalf("expected owned action to execute, handled=%v isError=%v output=%q", handled, isError, output)
	}
}

func TestBuildRuntimeToolExecutor_ListTasksDuplicateEmptyNoopIsGuarded(t *testing.T) {
	calls := 0
	handlers := map[string]RuntimeActionHandler{
		"list_tasks": func(_ context.Context, input json.RawMessage) (string, error) {
			calls++
			return `{"ok":true,"tasks":[],"count":0,"total":0,"limit":10,"offset":0,"has_more":false,"filter":{"query":"#999","category":"","status":""},"note":"empty"}`, nil
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)
	ctx := context.Background()

	out, handled, isError, err := executor(ctx, "list_tasks", json.RawMessage(`{"limit":10,"query":"#999"}`))
	if err != nil || !handled || isError || calls != 1 || strings.Contains(out, "duplicate_noop") {
		t.Fatalf("expected first list_tasks call to execute normally, calls=%d handled=%v isError=%v err=%v out=%s", calls, handled, isError, err, out)
	}

	out, handled, isError, err = executor(ctx, "list_tasks", json.RawMessage(`{"query":"#999","limit":10,"offset":0}`))
	if err != nil || !handled || isError || calls != 1 {
		t.Fatalf("expected duplicate list_tasks call to be guarded, calls=%d handled=%v isError=%v err=%v out=%s", calls, handled, isError, err, out)
	}
	if !strings.Contains(out, `"duplicate_noop":true`) || !strings.Contains(out, "Identical list_tasks parameters already returned no tasks") {
		t.Fatalf("expected duplicate noop marker and note, got %s", out)
	}

	out, handled, isError, err = executor(ctx, "list_tasks", json.RawMessage(`{"limit":10,"offset":10,"query":"#999"}`))
	if err != nil || !handled || isError || calls != 2 || !strings.Contains(out, `"has_more":false`) {
		t.Fatalf("expected changed pagination input to execute, calls=%d handled=%v isError=%v err=%v out=%s", calls, handled, isError, err, out)
	}
}

func TestBuildRuntimeToolExecutor_RegisteredActionExecutes(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{
		"list_models": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "models listed", nil
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)

	output, handled, isError, err := executor(context.Background(), "list_models", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for registered action")
	}
	if isError {
		t.Fatal("expected isError=false for successful execution")
	}
	if output != "models listed" {
		t.Fatalf("expected output='models listed', got %q", output)
	}
}

func TestBuildRuntimeToolExecutor_EmptyNameFallsThrough(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{}
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)

	_, handled, _, err := executor(context.Background(), "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Fatal("expected handled=false for empty tool name")
	}
}

func TestBuildRuntimeToolExecutor_AllowsGrantedGoalStatusActions(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{
		"mark_task_goal_achieved": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "marked", nil
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)

	output, handled, isError, err := executor(context.Background(), "mark_task_goal_achieved", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || isError || output != "marked" {
		t.Fatalf("expected goal status action to execute, handled=%v isError=%v output=%q", handled, isError, output)
	}
}

func TestBuildLifecycleRuntimeToolExecutor_AllowsLifecycleOnly(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{
		"mark_task_goal_achieved": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "marked", nil
		},
	}
	executor := BuildLifecycleRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)

	output, handled, isError, err := executor(context.Background(), "mark_task_goal_achieved", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || isError || output != "marked" {
		t.Fatalf("expected lifecycle action to execute, handled=%v isError=%v output=%q", handled, isError, output)
	}
}

func TestBuildRuntimeToolExecutor_PlanModeReadActionsWork(t *testing.T) {
	// Read-only chatcontrol actions (like list_models) should work in plan mode.
	handlers := map[string]RuntimeActionHandler{
		"list_models": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "models", nil
		},
		"get_chat_mode": func(_ context.Context, _ json.RawMessage) (string, error) {
			return "plan", nil
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModePlan, SurfaceWeb, handlers)

	for _, tool := range []string{"list_models", "get_chat_mode"} {
		output, handled, isError, err := executor(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil {
			t.Errorf("tool %q: unexpected error: %v", tool, err)
		}
		if !handled {
			t.Errorf("tool %q: expected handled=true", tool)
		}
		if isError {
			t.Errorf("tool %q: expected isError=false", tool)
		}
		if output == "" {
			t.Errorf("tool %q: expected non-empty output", tool)
		}
	}
}

func TestBuildRuntimeToolExecutor_NormalizesNameAndSurfacesHandlerErrors(t *testing.T) {
	wantErr := errors.New("boom")
	handlers := map[string]RuntimeActionHandler{
		"list_models": func(_ context.Context, input json.RawMessage) (string, error) {
			if string(input) != `{"ok":true}` {
				t.Fatalf("unexpected input: %s", input)
			}
			return "", wantErr
		},
	}
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, handlers)

	_, handled, isError, err := executor(context.Background(), " LIST_MODELS ", json.RawMessage(`{"ok":true}`))
	if !handled || !isError || !errors.Is(err, wantErr) {
		t.Fatalf("expected handler error to surface with handled=true/isError=true, handled=%v isError=%v err=%v", handled, isError, err)
	}
}

func TestBuildRuntimeToolExecutor_RegisteredMissingHandlerReturnsToolError(t *testing.T) {
	executor := BuildRuntimeToolExecutor(models.ChatModeOrchestrate, SurfaceWeb, map[string]RuntimeActionHandler{})

	out, handled, isError, err := executor(context.Background(), "list_models", json.RawMessage(`{}`))
	if err != nil || !handled || !isError {
		t.Fatalf("expected handled tool error for missing handler, handled=%v isError=%v err=%v out=%q", handled, isError, err, out)
	}
	if !strings.Contains(out, `"handler_missing"`) || !strings.Contains(out, `"list_models"`) {
		t.Fatalf("missing handler output did not describe failure: %s", out)
	}
}

func TestValidateHandlerCoverageIgnoresExternalMemoryTool(t *testing.T) {
	handlers := map[string]RuntimeActionHandler{}
	err := ValidateHandlerCoverage(models.ChatModePlan, SurfaceWeb, false, handlers)
	if err == nil || !strings.Contains(err.Error(), "list_models") {
		t.Fatalf("expected missing handler error for normal tools, got %v", err)
	}
	if strings.Contains(err.Error(), "memory_view") {
		t.Fatalf("memory_view is external and should be ignored, got %v", err)
	}

	fullHandlers := map[string]RuntimeActionHandler{}
	for _, def := range ToolDefsForContext(models.ChatModePlan, SurfaceWeb, false) {
		if isExternalRuntimeTool(def.Name) {
			continue
		}
		fullHandlers[def.Name] = func(context.Context, json.RawMessage) (string, error) { return "ok", nil }
	}
	if err := ValidateHandlerCoverage(models.ChatModePlan, SurfaceWeb, false, fullHandlers); err != nil {
		t.Fatalf("expected full handler coverage: %v", err)
	}
}
