package pages

import "github.com/openvibely/openvibely/internal/models"

func projectID(p *models.Project) string {
	if p == nil {
		return "default"
	}
	return p.ID
}
