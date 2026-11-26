package layout

import (
	"context"
	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/machinetype"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"strings"
)

type TaskMachine struct{ Name, Kind, Icon string }
type taskMachinesKey struct{}

func WithTaskMachines(ctx context.Context, machines map[string]TaskMachine) context.Context {
	icons := make(map[string]templ.Component, len(machines))
	names := make(map[string]string, len(machines))
	for id, machine := range machines {
		icons[id] = ServerIcon(machine.Icon, machine.Kind)
		names[id] = machine.Name
	}
	return context.WithValue(templateui.WithServerNames(templateui.WithServerIcons(ctx, icons), names), taskMachinesKey{}, machines)
}
func taskMachine(ctx context.Context, projectID string) TaskMachine {
	server, _, remote := remoteid.Split(projectID)
	if !remote {
		return TaskMachine{Name: "This computer", Kind: machinetype.Local()}
	}
	machines, _ := ctx.Value(taskMachinesKey{}).(map[string]TaskMachine)
	if m, ok := machines[server]; ok {
		return m
	}
	return TaskMachine{Name: "Remote server", Kind: "server"}
}
func taskProjectLabel(ctx context.Context, projectID, name string) string {
	// Combined selector names can include the machine; the badge owns that label.
	return strings.TrimSuffix(name, " · "+taskMachine(ctx, projectID).Name)
}
