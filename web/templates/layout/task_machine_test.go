package layout

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/components"
	"strings"
	"testing"
)

func TestTaskMachineRows(t *testing.T) {
	server := "0123456789abcdef"
	remote := remoteid.ID(server, "project")
	ctx := WithTaskMachines(context.Background(), map[string]TaskMachine{server: {Name: "oldmac", Kind: "laptop"}})
	tasks := []models.Task{{ID: "a", ProjectID: "local", Title: "Local task"}, {ID: "b", ProjectID: remote, Title: "Remote task"}}
	for _, scope := range []string{"local", "__repo_group", models.AllProjectsID} {
		var out strings.Builder
		err := TaskListPanelContentWithProjects(scope, []TaskListGroup{{Label: "Active", Tasks: tasks}}, map[string]string{"local": "Foo · This computer", remote: "Foo · oldmac"}).Render(ctx, &out)
		if err != nil {
			t.Fatal(err)
		}
		s := out.String()
		if strings.Contains(s, `data-tip="This computer"`) || strings.Count(s, `class="task-machine-badge"`) != 1 {
			t.Fatal("only remote tasks should have machine badges")
		}
		for _, want := range []string{`data-tip="oldmac"`, `M3 19l18 0`, `tabindex="0"`} {
			if !strings.Contains(s, want) {
				t.Errorf("missing %s", want)
			}
		}
		if strings.Contains(s, "data-task-list-project=") != (scope == models.AllProjectsID) {
			t.Errorf("incorrect project label for %s", scope)
		}
		if scope == models.AllProjectsID && !strings.Contains(s, ">Foo</span>") {
			t.Fatal("project name absent")
		}
	}
	if taskMachine(context.Background(), remote).Kind != "server" {
		t.Fatal("older server fallback")
	}
}

func TestTaskMachineCustomIcon(t *testing.T) {
	server := "0123456789abcdef"
	project := remoteid.ID(server, "project")
	ctx := WithTaskMachines(context.Background(), map[string]TaskMachine{server: {Name: "oldmac", Kind: "laptop", Icon: "spider"}})
	var out, icon strings.Builder
	if err := TaskListPanelContentWithProjects(project, []TaskListGroup{{Label: "Active", Tasks: []models.Task{{ID: "task", ProjectID: project, Title: "Remote task"}}}}, nil).Render(ctx, &out); err != nil {
		t.Fatal(err)
	}
	ServerIcon("spider", "server").Render(ctx, &icon)
	if !strings.Contains(out.String(), icon.String()) || !strings.Contains(out.String(), `data-tip="oldmac"`) {
		t.Fatal("custom icon or server tooltip missing")
	}
}

func TestTaskMachineComposerDestination(t *testing.T) {
	server := "0123456789abcdef"
	project := remoteid.ID(server, "project")
	ctx := WithTaskMachines(context.Background(), map[string]TaskMachine{server: {Name: "oldmac", Kind: "laptop", Icon: "spider"}})
	var out, icon strings.Builder
	err := components.ChatInputForm(components.ChatInputFormConfig{FormID: "test", InputID: "input", ProjectChoices: []models.Project{{ID: "local", Name: "Local"}, {ID: project, Name: "oldmac"}}}).Render(ctx, &out)
	if err != nil {
		t.Fatal(err)
	}
	ServerIcon("spider", "server").Render(ctx, &icon)
	if strings.Count(out.String(), icon.String()) != 1 {
		t.Fatal("remote destination must render its saved icon once")
	}
}

func TestTaskCardMachineBadges(t *testing.T) {
	server := "0123456789abcdef"
	remote := remoteid.ID(server, "project")
	ctx := WithTaskMachines(context.Background(), map[string]TaskMachine{server: {Name: "oldmac", Kind: "laptop", Icon: "spider"}})
	ctx = templateui.WithProjectNames(ctx, []models.Project{{ID: "local", Name: "Foo · This computer"}, {ID: remote, Name: "Foo · oldmac"}})
	var icon strings.Builder
	if err := ServerIcon("spider", "server").Render(ctx, &icon); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"local", "__repo_group", models.AllProjectsID} {
		for _, project := range []string{"local", remote} {
			var out strings.Builder
			if err := components.TaskCard(models.Task{ID: "task", ProjectID: project, Title: "Example"}, scope, "backlog", nil, nil).Render(ctx, &out); err != nil {
				t.Fatal(err)
			}
			s := out.String()
			if strings.Contains(s, "This computer") || strings.Contains(s, "Foo ·") {
				t.Fatal("redundant server label on card")
			}
			if strings.Contains(s, "data-project-label") != (scope == models.AllProjectsID) {
				t.Fatalf("incorrect project label for %s", scope)
			}
			if scope == models.AllProjectsID && !strings.Contains(s, ">Foo</span>") {
				t.Fatal("missing project name")
			}
			if strings.Contains(s, `data-tip="oldmac"`) != (project == remote) {
				t.Fatal("incorrect server tooltip")
			}
			if project == remote && !strings.Contains(s, icon.String()) {
				t.Fatal("missing custom remote icon")
			}
		}
	}
}
