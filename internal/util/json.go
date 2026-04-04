package util

import "strings"

// ExtractJSONObject strips markdown fences and finds first { to last } in the string.
func ExtractJSONObject(s string) string {
	s = StripMarkdownFences(s)

	first := strings.Index(s, "{")
	last := strings.LastIndex(s, "}")
	if first >= 0 && last > first {
		return s[first : last+1]
	}
	return ""
}

// ExtractJSONArray strips markdown fences and finds first [ to last ] in the string.
func ExtractJSONArray(s string) string {
	s = StripMarkdownFences(s)

	first := strings.Index(s, "[")
	last := strings.LastIndex(s, "]")
	if first >= 0 && last > first {
		return s[first : last+1]
	}
	return ""
}

// StripMarkdownFences removes ```json and ``` fences from a string.
func StripMarkdownFences(s string) string {
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")
	return strings.TrimSpace(s)
}
