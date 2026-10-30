package service

import (
	"encoding/json"
	"fmt"
)

// errMaxTokens is returned when the API response was truncated due to max_tokens.
var errMaxTokens = fmt.Errorf("response truncated: max_tokens limit reached (output budget exhausted before task completed)")

// toolSecondaryInfo extracts a short secondary label from tool input (e.g. filename, command).
// Returns empty string if nothing useful can be extracted.
// Used by legacy callAnthropic paths for streaming event markers.
func toolSecondaryInfo(name string, input json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch name {
	case "read_file", "write_file", "edit_file", "Read", "Write", "Edit":
		if fp, ok := m["file_path"].(string); ok {
			// Return just the filename, not full path
			parts := splitPath(fp)
			return parts[len(parts)-1]
		}
	case "bash", "Bash", "shell":
		if cmd, ok := m["command"].(string); ok {
			if len(cmd) > 60 {
				cmd = cmd[:60] + "…"
			}
			return "$ " + cmd
		}
	case "grep_search", "Grep":
		if p, ok := m["pattern"].(string); ok {
			if len(p) > 40 {
				p = p[:40] + "…"
			}
			return p
		}
	case "list_files", "Glob":
		if p, ok := m["path"].(string); ok {
			return p
		}
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

func splitPath(p string) []string {
	var parts []string
	for _, s := range []byte(p) {
		if s == '/' {
			parts = append(parts, "")
		} else if len(parts) == 0 {
			parts = append(parts, string(s))
		} else {
			parts[len(parts)-1] += string(s)
		}
	}
	if len(parts) == 0 {
		return []string{p}
	}
	return parts
}
