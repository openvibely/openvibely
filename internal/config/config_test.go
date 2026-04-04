package config

import "testing"

func TestResolveEnableLocalRepoPath(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		want     bool
	}{
		{name: "explicit true", explicit: "true", want: true},
		{name: "explicit false", explicit: "false", want: false},
		{name: "unset defaults false", explicit: "", want: false},
		{name: "invalid defaults false", explicit: "maybe", want: false},
		{name: "numeric true", explicit: "1", want: true},
		{name: "numeric false", explicit: "0", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEnableLocalRepoPath(tt.explicit)
			if got != tt.want {
				t.Fatalf("ResolveEnableLocalRepoPath(%q) = %v, want %v", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestResolveEnableTaskChangesMergeOptions(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		want     bool
	}{
		{name: "explicit true", explicit: "true", want: true},
		{name: "explicit false", explicit: "false", want: false},
		{name: "unset defaults false", explicit: "", want: false},
		{name: "invalid defaults false", explicit: "maybe", want: false},
		{name: "numeric true", explicit: "1", want: true},
		{name: "numeric false", explicit: "0", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveEnableTaskChangesMergeOptions(tt.explicit)
			if got != tt.want {
				t.Fatalf("ResolveEnableTaskChangesMergeOptions(%q) = %v, want %v", tt.explicit, got, tt.want)
			}
		})
	}
}
