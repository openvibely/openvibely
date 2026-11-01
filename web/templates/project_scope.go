package templates

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

type projectNamesKey struct{}
type projectOptionsKey struct{}

// WithProjectNames adds ownership labels to existing components in aggregate views.
func WithProjectNames(ctx context.Context, projects []models.Project) context.Context {
	names := make(map[string]string, len(projects))
	for _, project := range projects {
		names[project.ID] = project.Name
	}
	return context.WithValue(context.WithValue(ctx, projectOptionsKey{}, projects), projectNamesKey{}, names)
}

func ProjectLabel(ctx context.Context, projectID string) string {
	names, _ := ctx.Value(projectNamesKey{}).(map[string]string)
	return names[projectID]
}

func ProjectOptions(ctx context.Context) []models.Project {
	projects, _ := ctx.Value(projectOptionsKey{}).([]models.Project)
	return projects
}
