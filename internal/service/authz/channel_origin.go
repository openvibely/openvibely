package authz

import "github.com/openvibely/openvibely/internal/models"

// UnmappedChannelOrigin identifies channels whose ingress cannot establish a
// verified application-user identity. A saved installer/owner is not a sender
// binding. Authenticated execution must deny these origins; auth-off callers
// retain legacy behavior. Discord is verified through durable provenance instead.
func UnmappedChannelOrigin(origin string) bool {
	switch origin {
	case models.TaskOriginEmail:
		return true
	default:
		return false
	}
}

// VerifiedChannelOrigin identifies channels whose authenticated tasks must carry
// a current admin-attested sender binding snapshot.
func VerifiedChannelOrigin(origin string) bool {
	return origin == models.TaskOriginDiscord || origin == models.TaskOriginTelegram
}
