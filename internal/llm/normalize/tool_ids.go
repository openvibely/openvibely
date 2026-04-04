package normalize

import (
	"regexp"
	"strings"
	"unicode"
)

var toolIDJSONPattern = regexp.MustCompile(`(?i)("(?:tool_use_id|tool_call_id|call_id)"\s*:\s*")([^"]+)(")`)

// NormalizeToolCallIDsInText rewrites known tool-call id fields into a conservative
// cross-provider-safe charset (letters, digits, _ and -).
func NormalizeToolCallIDsInText(text string) string {
	if text == "" {
		return text
	}
	if !strings.Contains(text, "call_id") && !strings.Contains(text, "tool_call_id") && !strings.Contains(text, "tool_use_id") {
		return text
	}
	return toolIDJSONPattern.ReplaceAllStringFunc(text, func(m string) string {
		sub := toolIDJSONPattern.FindStringSubmatch(m)
		if len(sub) != 4 {
			return m
		}
		return sub[1] + sanitizeToolCallID(sub[2]) + sub[3]
	})
}

func sanitizeToolCallID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "call"
	}

	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}

	s := strings.Trim(b.String(), "_")
	if s == "" {
		s = "call"
	}
	if len(s) > 96 {
		s = s[:96]
	}
	return s
}
