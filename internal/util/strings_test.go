package util

import "testing"

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
	}
	for _, tt := range tests {
		result := Truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestTruncateWithSuffix(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		suffix   string
		expected string
	}{
		{"hello", 10, "...", "hello"},
		{"hello world", 5, "... (truncated)", "hello... (truncated)"},
		{"", 5, "...", ""},
		{"abc", 3, "...", "abc"},
	}
	for _, tt := range tests {
		result := TruncateWithSuffix(tt.input, tt.maxLen, tt.suffix)
		if result != tt.expected {
			t.Errorf("TruncateWithSuffix(%q, %d, %q) = %q, want %q", tt.input, tt.maxLen, tt.suffix, result, tt.expected)
		}
	}
}
