package util

// Truncate truncates s to maxLen characters, appending "..." if truncated.
// The returned string may be up to maxLen+3 characters long.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TruncateWithSuffix truncates s to maxLen characters, appending the given suffix if truncated.
// The returned string may be up to maxLen+len(suffix) characters long.
func TruncateWithSuffix(s string, maxLen int, suffix string) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + suffix
}
