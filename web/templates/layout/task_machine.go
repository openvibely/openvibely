package layout

import (
	"context"
	"github.com/openvibely/openvibely/internal/machinetype"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"strings"
	"sync"
)

type TaskMachine struct{ Name, Kind, Icon string }
type taskMachinesKey struct{}

func WithTaskMachines(ctx context.Context, machines map[string]TaskMachine) context.Context {
	return WithTaskMachineLoader(ctx, func() map[string]TaskMachine { return machines })
}

func WithTaskMachineLoader(ctx context.Context, load func() map[string]TaskMachine) context.Context {
	machines := sync.OnceValue(load)
	ctx = templateui.WithServerDisplay(ctx, func(server string) templateui.ServerDisplay {
		m, ok := machines()[server]
		if !ok {
			return templateui.ServerDisplay{}
		}
		return templateui.ServerDisplay{Name: m.Name, Icon: ServerIcon(m.Icon, m.Kind)}
	})
	return context.WithValue(ctx, taskMachinesKey{}, machines)
}
func taskMachine(ctx context.Context, projectID string) TaskMachine {
	server, _, remote := remoteid.Split(projectID)
	if !remote {
		return TaskMachine{Name: "This computer", Kind: machinetype.Local()}
	}
	if machines, ok := ctx.Value(taskMachinesKey{}).(func() map[string]TaskMachine); ok {
		if m, found := machines()[server]; found {
			return m
		}
	}
	return TaskMachine{Name: "Remote server", Kind: "server"}
}
func taskProjectLabel(ctx context.Context, projectID, name string) string {
	// Combined selector names can include the machine; the badge owns that label.
	return strings.TrimSuffix(name, " · "+taskMachine(ctx, projectID).Name)
}
