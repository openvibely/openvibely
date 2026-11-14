package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAutomationLiveModelDisplay(t *testing.T) {
	for _, tt := range []struct{ name, id, label, want string }{
		{"configured", "8e769f265f261dd8768b3cb39b9a22d9", "Coding · gpt-5", "Coding · gpt-5"},
		{"default", "default", "", "Project default"},
		{"empty", "", "", "Project default"},
		{"deleted", "missing-model-hash", "", "Unavailable model"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := models.AutomationLiveNode{AutomationNode: models.AutomationNode{NodeType: models.AutomationNodeAgentTask, ConfigJSON: `{"model_config_id":"` + tt.id + `"}`}, ModelLabel: tt.label}
			var shape, details strings.Builder
			if err := automationLiveNodeShape(node).Render(context.Background(), &shape); err != nil {
				t.Fatal(err)
			}
			if err := automationLiveDetailsPanel(models.AutomationLiveGraph{Nodes: []models.AutomationLiveNode{node}}, "project").Render(context.Background(), &details); err != nil {
				t.Fatal(err)
			}
			for name, output := range map[string]string{"graph": shape.String(), "details": details.String()} {
				if !strings.Contains(output, tt.want) {
					t.Fatalf("%s missing label %q: %s", name, tt.want, output)
				}
				if tt.id != "" && tt.id != "default" && strings.Contains(output, tt.id) {
					t.Fatalf("%s exposed model ID", name)
				}
			}
		})
	}
}
