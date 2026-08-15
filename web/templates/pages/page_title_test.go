package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
)

func TestPrimaryNavigationFragmentsIncludeAuthoritativePageTitles(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		component templ.Component
	}{
		{name: "chat", title: "Chat", component: ChatContent(nil, nil, "", nil, nil, false, false, 30)},
		{name: "tasks", title: "Tasks", component: TasksContent(nil, nil, nil, nil, "", "")},
		{name: "schedule", title: "Schedule", component: ScheduleContent(nil, nil, 0, nil, nil)},
		{name: "grades", title: "Proactive Insights", component: InsightsContent(nil, nil)},
		{name: "pulse", title: "Pulse", component: UpcomingContent(&models.Upcoming{}, "")},
		{name: "reflection", title: "Reflection", component: HistoryContent(&models.History{}, "")},
		{name: "analytics", title: "Analytics Dashboard", component: AnalyticsContent(nil)},
		{name: "alerts", title: "Alerts", component: AlertsContent(nil, "", 0)},
		{name: "models", title: "Models", component: ModelsContent(nil, nil, false)},
		{name: "agents", title: "Agents", component: AgentsContent(nil, nil)},
		{name: "skills", title: "Skills", component: SkillsContent(nil, false)},
		{name: "workers", title: "Workers", component: WorkerSettingsContent(0, 0, 0, 0, nil, nil)},
		{name: "personality", title: "Personality", component: AppSettingsContent("", "", nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tt.component.Render(context.Background(), &buf); err != nil {
				t.Fatalf("render fragment: %v", err)
			}
			output := buf.String()
			if !strings.HasPrefix(strings.TrimSpace(output), "<div") {
				t.Fatalf("fragment title marker must remain inside the existing swap root: %s", output)
			}
			if strings.Contains(output, "history.pushState") {
				t.Fatal("page fragment must use centralized HTMX-managed navigation instead of manual history.pushState")
			}
			expected := `data-openvibely-page-title="` + tt.title + ` - OpenVibely"`
			if !strings.Contains(output, expected) {
				t.Fatalf("fragment missing authoritative title marker %q", expected)
			}
		})
	}
}
