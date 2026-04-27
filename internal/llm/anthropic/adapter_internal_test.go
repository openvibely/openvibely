package anthropic

import "testing"

func TestAnthropicThinkingBudgetTokens(t *testing.T) {
	tests := []struct {
		name   string
		effort string
		want   int
	}{
		{"blank keeps adaptive/default", "", 0},
		{"low", "low", 4096},
		{"medium", "medium", 8192},
		{"high uses internal budget ceiling", "high", 13107},
		{"max uses internal budget ceiling", "max", 13107},
		{"invalid keeps adaptive/default", "xhigh", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anthropicThinkingBudgetTokens(tt.effort); got != tt.want {
				t.Fatalf("anthropicThinkingBudgetTokens(%q) = %d, want %d", tt.effort, got, tt.want)
			}
		})
	}
}
