package service

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"testing"
)

func TestWorkflowJSONFences(t *testing.T) {
	raw := `{"name":"Report","instructions":"Open Reports"}`
	for _, input := range []string{raw, "  " + raw + "\n", "```json\n" + raw + "\n```", "```\n" + raw + "\n```", "```json\r\n" + raw + "\r\n```"} {
		var w models.ComputerWorkflow
		if err := json.Unmarshal([]byte(unwrapWorkflowJSON(input)), &w); err != nil || w.Name != "Report" {
			t.Fatalf("%q: %+v %v", input, w, err)
		}
	}
	for _, input := range []string{"```json\n" + raw, "Explanation\n" + raw, "```json\n" + raw + "\n```\ntrailing"} {
		var w models.ComputerWorkflow
		if json.Unmarshal([]byte(unwrapWorkflowJSON(input)), &w) == nil {
			t.Fatalf("accepted malformed response %q", input)
		}
	}
}
