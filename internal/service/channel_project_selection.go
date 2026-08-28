package service

import "github.com/openvibely/openvibely/internal/models"

func fallbackProjectID(projects []models.Project) string {
	if len(projects) == 0 {
		return ""
	}
	for _, project := range projects {
		if project.IsDefault {
			return project.ID
		}
	}
	return projects[0].ID
}
