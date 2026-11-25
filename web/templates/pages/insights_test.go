package pages

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func renderInsightsComponentForTest(t *testing.T, component interface {
	Render(context.Context, io.Writer) error
}) string {
	t.Helper()
	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render component: %v", err)
	}
	return buf.String()
}

func TestInsightCardMutationURLsIncludeProjectContext(t *testing.T) {
	newInsight := &models.Insight{
		ID:         "insight-new",
		ProjectID:  "project-insights",
		Type:       models.InsightBugPattern,
		Severity:   models.InsightSeverityHigh,
		Status:     models.InsightStatusNew,
		Title:      "New insight",
		Confidence: 0.8,
	}
	newBody := renderInsightsComponentForTest(t, InsightCard(newInsight))
	if !strings.Contains(newBody, `aria-label="Delete insight"`) {
		t.Fatal("delete action needs an accessible name")
	}
	for _, expected := range []string{
		`hx-patch="/insights/insight-new/status?project_id=project-insights"`,
		`hx-delete="/insights/insight-new?project_id=project-insights"`,
	} {
		if !strings.Contains(newBody, expected) {
			t.Fatalf("new insight card missing scoped action URL %q\n%s", expected, newBody)
		}
	}

	acceptedInsight := *newInsight
	acceptedInsight.ID = "insight-accepted"
	acceptedInsight.Status = models.InsightStatusAccepted
	acceptedBody := renderInsightsComponentForTest(t, InsightCard(&acceptedInsight))
	if !strings.Contains(acceptedBody, `hx-patch="/insights/insight-accepted/status?project_id=project-insights"`) || !strings.Contains(acceptedBody, `hx-delete="/insights/insight-accepted?project_id=project-insights"`) {
		t.Fatalf("accepted insight card missing scoped action URL\n%s", acceptedBody)
	}
}
