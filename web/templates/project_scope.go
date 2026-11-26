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

// ServerDisplay supplies machine labels and icons for remote projects.
type ServerDisplay struct {
	Name string
	Icon templ.Component
}

type serverDisplayKey struct{}

func WithServerDisplay(ctx context.Context, lookup func(string) ServerDisplay) context.Context {
	return context.WithValue(ctx, serverDisplayKey{}, lookup)
}

func projectServerDisplay(ctx context.Context, projectID string) ServerDisplay {
	server, _, remote := remoteid.Split(projectID)
	if !remote {
		return ServerDisplay{Name: "This computer"}
	}
	if lookup, ok := ctx.Value(serverDisplayKey{}).(func(string) ServerDisplay); ok {
		return lookup(server)
	}
	return ServerDisplay{}
}

func ProjectServerIcon(ctx context.Context, projectID string) templ.Component {
	if icon := projectServerDisplay(ctx, projectID).Icon; icon != nil {
		return icon
	}
	return templ.NopComponent
}

func ProjectServerName(ctx context.Context, projectID string) string {
	return projectServerDisplay(ctx, projectID).Name
}
