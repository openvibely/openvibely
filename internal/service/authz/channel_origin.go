package authz

import "github.com/openvibely/openvibely/internal/models"

// UnmappedChannelOrigin identifies channels whose ingress cannot yet establish a
// verified application-user identity. A saved installer/owner is not a sender
// binding. Authenticated execution must deny these origins until mappings and
// durable provenance are implemented; auth-off callers retain legacy behavior.
func UnmappedChannelOrigin(origin string) bool {
	switch origin {
	case models.TaskOriginTelegram, models.TaskOriginDiscord, models.TaskOriginEmail:
		return true
	default:
		return false
	}
}
