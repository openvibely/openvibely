package pages

import "github.com/openvibely/openvibely/internal/models"

func projectSharingHasTransferCandidate(users []models.OrgUser, ownerID string) bool {
	for _, user := range users {
		if !user.Disabled && user.UserID != ownerID {
			return true
		}
	}
	return false
}
