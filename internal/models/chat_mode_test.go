package models

import "testing"

func TestNormalizeChatMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ChatMode
	}{
		{name: "plan", in: "plan", want: ChatModePlan},
		{name: "plan uppercase", in: "PLAN", want: ChatModePlan},
		{name: "trimmed", in: "  plan  ", want: ChatModePlan},
		{name: "default empty", in: "", want: ChatModeOrchestrate},
		{name: "default unknown", in: "random", want: ChatModeOrchestrate},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeChatMode(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeChatMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
