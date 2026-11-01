package pages

import (
	"bytes"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestComputerPreviewLeadsDedicatedPanel(t *testing.T) {
	var out bytes.Buffer
	if err := ProjectComputerContent("project", "task").Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	preview := strings.Index(html, "data-computer-preview")
	toggle := strings.Index(html, "data-computer-preference=")
	controls := strings.Index(html, "data-computer-dialog")
	if preview < 0 || toggle < preview || controls < preview {
		t.Fatal("preview must precede toggle and controls")
	}
	for _, unwanted := range []string{`data-computer-action="takeover"`, `data-computer-action="release"`, `data-computer-expand>`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("unexpected viewer button %s", unwanted)
		}
	}
	out.Reset()
	if err := TaskDetailContent(&models.Task{ID: "task", ProjectID: "project"}, nil, nil, nil, nil, nil, nil, "details", nil).Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	_, details, _ := strings.Cut(out.String(), `id="tab-details"`)
	details, _, _ = strings.Cut(details, `id="tab-schedules"`)
	if strings.Contains(details, "data-computer-preference=") {
		t.Fatal("computer setting remains in Details")
	}
}
