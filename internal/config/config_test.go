package config

import (
	"strings"
	"testing"
)

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

func TestResolveAppBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "trim and strip trailing slash", raw: "  https://dubee.org/ ", want: "https://dubee.org"},
		{name: "allow path prefix", raw: "https://dubee.org/openvibely/", want: "https://dubee.org/openvibely"},
		{name: "http localhost", raw: "http://localhost:3001", want: "http://localhost:3001"},
		{name: "reject no scheme", raw: "dubee.org", want: ""},
		{name: "reject unsupported scheme", raw: "ftp://dubee.org", want: ""},
		{name: "reject query", raw: "https://dubee.org?x=1", want: ""},
		{name: "reject fragment", raw: "https://dubee.org#frag", want: ""},
		{name: "reject user info", raw: "https://user:pass@dubee.org", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveAppBaseURL(tt.raw); got != tt.want {
				t.Fatalf("ResolveAppBaseURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidateAppBaseURL(t *testing.T) {
	if err := ValidateAppBaseURL(""); err != nil {
		t.Fatalf("ValidateAppBaseURL empty returned unexpected error: %v", err)
	}
	if err := ValidateAppBaseURL("https://dubee.org"); err != nil {
		t.Fatalf("ValidateAppBaseURL valid URL returned unexpected error: %v", err)
	}
	err := ValidateAppBaseURL("dubee.org")
	if err == nil {
		t.Fatal("expected error for invalid APP_BASE_URL")
	}
	if !strings.Contains(err.Error(), "APP_BASE_URL") {
		t.Fatalf("expected APP_BASE_URL in validation error, got %q", err)
	}
}
