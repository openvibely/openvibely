package persistence

import (
	netmail "net/mail"
	"strings"
)

// NormalizeEmailAddress extracts the mailbox address when possible, then trims and lowercases it for storage and matching.
func NormalizeEmailAddress(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	if addr, err := netmail.ParseAddress(email); err == nil && addr != nil && strings.TrimSpace(addr.Address) != "" {
		email = addr.Address
	}
	return strings.ToLower(strings.TrimSpace(email))
}
