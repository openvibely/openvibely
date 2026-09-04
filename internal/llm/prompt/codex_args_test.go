package prompt

import (
	"testing"
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
	if got := CodexReasoningEffort("gpt-6-astra", "none"); got != "high" {
		t.Fatalf("expected Astra to reject none and use a supported fallback, got %q", got)
	}
	if got := CodexReasoningEffort("gpt-6-astra", "max"); got != "max" {
		t.Fatalf("expected Astra to preserve max, got %q", got)
	}
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
		{model: "gpt-6-astra", want: "medium"},
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

func TestCodexModelOrDefault_UnsupportedFallsBackToDefault(t *testing.T) {
	if got := CodexModelOrDefault("gpt-unknown-99"); got != CodexDefaultModel {
		t.Fatalf("expected unsupported model to fallback to %q, got %q", CodexDefaultModel, got)
	}
}
