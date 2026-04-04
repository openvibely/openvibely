package util

import "testing"

func TestExtractJSONObject(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"json in markdown fences", "Here:\n```json\n{\"key\": \"val\"}\n```\nDone.", `{"key": "val"}`},
		{"raw json", `{"key": "val"}`, `{"key": "val"}`},
		{"json embedded in text", `Analysis complete. {"key": "val"} End.`, `{"key": "val"}`},
		{"no json", "No structured output", ""},
		{"nested objects", `{"a": {"b": 1}}`, `{"a": {"b": 1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractJSONObject(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractJSONObject(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestStripMarkdownFences(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"no fences", `{"key": "val"}`, `{"key": "val"}`},
		{"json fence", "```json\n{\"key\": \"val\"}\n```", `{"key": "val"}`},
		{"plain fence", "```\n{\"key\": \"val\"}\n```", `{"key": "val"}`},
		{"nested fences", "```json\n```inner```\n```", "inner"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripMarkdownFences(tt.input)
			if result != tt.expected {
				t.Errorf("StripMarkdownFences(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractJSONArray(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"fenced array", "```json\n[{\"a\": 1}]\n```", `[{"a": 1}]`},
		{"array with surrounding text", "Results:\n[{\"a\": 1}]\nDone.", `[{"a": 1}]`},
		{"no array", "just some text", ""},
		{"raw array", `[1, 2, 3]`, `[1, 2, 3]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractJSONArray(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractJSONArray(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
