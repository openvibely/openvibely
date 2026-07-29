package prompt

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestCodexReasoningEffort_DefaultHigh(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "")
	if got := CodexReasoningEffort("gpt-5.3-codex", ""); got != "high" {
		t.Errorf("expected default effort %q, got %q", "high", got)
	}
}

func TestCodexReasoningEffort_EnvOverride(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "medium")
	if got := CodexReasoningEffort("gpt-5.3-codex", ""); got != "medium" {
		t.Errorf("expected overridden effort %q, got %q", "medium", got)
	}
}

func TestCodexReasoningEffort_ModelSpecificFallback(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "xhigh")
	if got := CodexReasoningEffort("gpt-5-codex", ""); got != "high" {
		t.Errorf("expected fallback effort %q for unsupported xhigh, got %q", "high", got)
	}
}

func TestCodexReasoningEffort_UnknownModelDropsXHigh(t *testing.T) {
	if got := CodexReasoningEffort("codex-1p-q-20251024-ev3", "xhigh"); got != "high" {
		t.Errorf("expected unknown model to fallback from xhigh to %q, got %q", "high", got)
	}
}

func TestCodexReasoningEffort_ConfiguredEffortWins(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "low")
	if got := CodexReasoningEffort("gpt-5.3-codex", "xhigh"); got != "xhigh" {
		t.Errorf("expected configured effort %q to override env, got %q", "xhigh", got)
	}
}

func TestCodexReasoningEffort_NewModelLevels(t *testing.T) {
	if got := CodexReasoningEffort("gpt-5.6-sol", "none"); got != "none" {
		t.Fatalf("expected Sol to preserve none, got %q", got)
	}
	if got := CodexReasoningEffort("gpt-5.6-sol", "max"); got != "max" {
		t.Fatalf("expected Sol to preserve max, got %q", got)
	}
	if got := CodexReasoningEffort("gpt-5.6-sol", "ultra"); got != "medium" {
		t.Fatalf("expected unsupported ultra to fall back to the model default, got %q", got)
	}
}

func TestCodexReasoningEffort_NewModelDefaults(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "")
	for _, tc := range []struct {
		model string
		want  string
	}{
		{model: "gpt-5.6-sol", want: "medium"},
		{model: "gpt-5.6-terra", want: "medium"},
		{model: "gpt-5.6-luna", want: "medium"},
		{model: "gpt-5.5", want: "medium"},
		{model: "gpt-5.5-pro", want: "medium"},
	} {
		if got := CodexReasoningEffort(tc.model, ""); got != tc.want {
			t.Errorf("CodexReasoningEffort(%q, empty) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestCodexExecArgs_IncludesReasoningOverride(t *testing.T) {
	t.Setenv("OPENVIBELY_CODEX_REASONING_EFFORT", "low")

	args := CodexExecArgs("gpt-5-codex", "", []string{"/tmp/a.png"})
	foundModelReasoning := false
	foundNestedReasoning := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == `model_reasoning_effort="low"` {
			foundModelReasoning = true
		}
		if args[i] == "-c" && args[i+1] == `reasoning.effort="low"` {
			foundNestedReasoning = true
		}
	}
	if !foundModelReasoning {
		t.Fatalf("expected args to include model_reasoning_effort override, got: %#v", args)
	}
	if !foundNestedReasoning {
		t.Fatalf("expected args to include reasoning.effort override, got: %#v", args)
	}
}

func TestCodexExecArgs_UsesExplicitModel(t *testing.T) {
	args := CodexExecArgs("gpt-5.1-codex-mini", "high", nil)

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-m" && args[i+1] == "gpt-5.1-codex-mini" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected args to include explicit -m model, got: %#v", args)
	}
}

func TestCodexExecArgs_DefaultsModelToCodexDefault(t *testing.T) {
	args := CodexExecArgs("", "", nil)

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-m" && args[i+1] == CodexDefaultModel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected args to default -m %s, got: %#v", CodexDefaultModel, args)
	}
}

func TestCodexModelOrDefault_UnsupportedFallsBackToDefault(t *testing.T) {
	if got := CodexModelOrDefault("gpt-unknown-99"); got != CodexDefaultModel {
		t.Fatalf("expected unsupported model to fallback to %q, got %q", CodexDefaultModel, got)
	}
}

func TestCodexExecArgs_DropsUnsupportedXHigh(t *testing.T) {
	args := CodexExecArgs("gpt-5-codex", "xhigh", nil)

	foundModelReasoning := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == `model_reasoning_effort="high"` {
			foundModelReasoning = true
			break
		}
	}
	if !foundModelReasoning {
		t.Fatalf("expected unsupported xhigh to fallback to high for gpt-5-codex, got: %#v", args)
	}
}

func TestCodexChatArgs_PlanModeSetsCollaborationMode(t *testing.T) {
	args := CodexChatArgs("gpt-5.3-codex", "high", nil, models.ChatModePlan)

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == `collaboration_mode="plan"` {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected plan mode arg in CodexChatArgs, got: %#v", args)
	}
}
