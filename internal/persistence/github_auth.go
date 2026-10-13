package persistence

import (
	"strings"
)

// NormalizeGitHubLogin trims a GitHub login, strips a leading @ mention marker, and lowercases it for authorization and inbox matching.
func NormalizeGitHubLogin(login string) string {
	normalized := strings.TrimSpace(login)
	normalized = strings.TrimPrefix(normalized, "@")
	normalized = strings.TrimSpace(normalized)
	return strings.ToLower(normalized)
}
