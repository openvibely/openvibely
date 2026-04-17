package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAlertsContent_DeleteActionsDoNotDependOnHxConfirm(t *testing.T) {
	alerts := []models.Alert{{ID: "alert-1", Title: "Disk full", ProjectID: "project-1"}}

	var buf bytes.Buffer
	err := AlertsContent(alerts, "project-1", 1).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render alerts content: %v", err)
	}

	html := buf.String()
	if !strings.Contains(html, `data-delete-url="/alerts?project_id=project-1"`) {
		t.Fatal("expected delete-all alerts action to provide delete URL via dataset")
	}
	if !strings.Contains(html, `data-delete-url="/alerts/alert-1?project_id=project-1"`) {
		t.Fatal("expected per-alert delete action to provide delete URL via dataset")
	}
	if !strings.Contains(html, `onclick="return deleteAlertsFromDataset(this)"`) {
		t.Fatal("expected delete-all action to call dataset-based delete helper")
	}
	if !strings.Contains(html, `deleteAlertsFromDataset(this)`) {
		t.Fatal("expected per-alert action to call dataset-based delete helper")
	}
	if strings.Contains(html, `hx-confirm="Delete all alerts? This action cannot be undone."`) {
		t.Fatal("delete-all should not depend on hx-confirm in desktop webview")
	}
	if strings.Contains(html, `hx-confirm="Delete this alert?"`) {
		t.Fatal("per-alert delete should not depend on hx-confirm in desktop webview")
	}
	if strings.Contains(html, `Delete all alerts? This action cannot be undone.`) {
		t.Fatal("alerts delete-all should not include confirmation copy")
	}
	if strings.Contains(html, `Delete this alert?`) {
		t.Fatal("per-alert delete should not include confirmation copy")
	}
	if strings.Contains(html, `function confirmAndDeleteAlerts(`) {
		t.Fatal("alerts template should not define confirmation-based delete helper")
	}
	if !strings.Contains(html, `function deleteAlerts(url, target)`) {
		t.Fatal("expected direct delete helper in alerts template")
	}
}
