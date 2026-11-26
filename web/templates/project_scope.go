package templates

import (
	"context"
	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
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

// WithServerIcons supplies trusted, rendered server icons to shared composers.
func WithServerIcons(ctx context.Context, icons map[string]templ.Component) context.Context {
	return context.WithValue(ctx, serverIconsKey{}, icons)
}

type serverIconsKey struct{}

func ProjectServerIcon(ctx context.Context, projectID string) templ.Component {
	server, _, remote := remoteid.Split(projectID)
	if remote {
		icons, _ := ctx.Value(serverIconsKey{}).(map[string]templ.Component)
		if icon := icons[server]; icon != nil {
			return icon
		}
	}
	return templ.NopComponent
}

type serverNamesKey struct{}

func WithServerNames(ctx context.Context, names map[string]string) context.Context {
	return context.WithValue(ctx, serverNamesKey{}, names)
}
func ProjectServerName(ctx context.Context, projectID string) string {
	server, _, remote := remoteid.Split(projectID)
	if !remote {
		return "This computer"
	}
	names, _ := ctx.Value(serverNamesKey{}).(map[string]string)
	return names[server]
}
