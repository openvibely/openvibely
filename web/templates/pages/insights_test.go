package pages

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

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

func TestInsightsContent_EmptyGradeCardsKeepDistinctActionsAndCopy(t *testing.T) {
	project := &models.Project{ID: "project-insights", Name: "Insights Project"}
	body := renderInsightsComponentForTest(t, InsightsContent(project, &models.InsightDashboardData{}))

	for _, expected := range []string{
		`id="health-check-content"`,
		`hx-post="/insights/health-check?project_id=project-insights"`,
		`hx-target="#health-check-content"`,
		`hx-indicator="#health-check-spinner"`,
		`Evaluate My Project`,
		`Click &#34;Evaluate My Project&#34; to get your personalized project health grade and feedback.`,
		`id="idea-grade-content"`,
		`hx-post="/history/grade-ideas?project_id=project-insights"`,
		`hx-target="#idea-grade-content"`,
		`hx-indicator="#idea-grade-spinner"`,
		`Grade My Ideas`,
		`Click &#34;Grade My Ideas&#34; to get an AI evaluation of your task quality and clarity.`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected empty Insights content to contain %q\n%s", expected, body)
		}
	}
}

func TestInsightsContent_RendersHealthDisplayAndIdeaEmptyStateWithoutLeakage(t *testing.T) {
	project := &models.Project{ID: "project-health", Name: "Health Project"}
	created := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	body := renderInsightsComponentForTest(t, InsightsContent(project, &models.InsightDashboardData{
		LatestHealthCheck: &models.HealthCheck{
			Grade:            "B+",
			Strengths:        "Health strengths",
			Improvements:     "Health improvements",
			Assessment:       "Health assessment",
			HowToImprove:     "Health next steps",
			TasksTotal:       12,
			TasksCompleted:   8,
			TasksFailed:      2,
			BacklogSize:      4,
			AvgCompletionPct: 67,
			CreatedAt:        created,
		},
	}))

	for _, expected := range []string{"Project Grade", "B+", "Tasks", "12", "Completed", "8", "Failed", "2", "Backlog", "4", "Completion", "67%", "Health strengths", "Health improvements", "Health assessment", "Health next steps", `text-success/80`, "Grade My Ideas"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected health-only Insights content to contain %q\n%s", expected, body)
		}
	}
	for _, unexpected := range []string{"Idea Quality", "Tasks Evaluated", "Clarity", "Ambition", "Follow-Through"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("health-only Insights content leaked idea display %q\n%s", unexpected, body)
		}
	}
}

func TestInsightsContent_RendersIdeaDisplayAndHealthEmptyStateWithoutLeakage(t *testing.T) {
	project := &models.Project{ID: "project-ideas", Name: "Ideas Project"}
	created := time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC)
	body := renderInsightsComponentForTest(t, InsightsContent(project, &models.InsightDashboardData{
		LatestIdeaGrade: &models.IdeaGrade{
			Grade:          "C",
			Summary:        "Idea assessment",
			Strengths:      "Idea strengths",
			Improvements:   "Idea improvements",
			HowToNextGrade: "Idea next steps",
			NextGrade:      "B",
			TasksEvaluated: 9,
			ClarityScore:   71,
			AmbitionScore:  82,
			FollowThrough:  44,
			CreatedAt:      created,
		},
	}))

	for _, expected := range []string{"Evaluate My Project", "Idea Quality", "C", "Tasks Evaluated", "9", "Clarity", "71%", "Ambition", "82%", "Follow-Through", "44%", "Idea strengths", "Idea improvements", "Idea assessment", "Idea next steps", "Target grade:", "B", `text-warning`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected idea-only Insights content to contain %q\n%s", expected, body)
		}
	}
	for _, unexpected := range []string{"Project Grade", "Health strengths", "Health improvements"} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("idea-only Insights content leaked health display %q\n%s", unexpected, body)
		}
	}
}

func TestInsightsGradeDisplays_RenderHistoryNewestFirstWithGradeColors(t *testing.T) {
	newest := time.Date(2026, time.August, 16, 9, 0, 0, 0, time.UTC)
	older := newest.AddDate(0, 0, -1)

	healthBody := renderInsightsComponentForTest(t, HealthCheckDisplay(&models.HealthCheck{Grade: "A", CreatedAt: newest}, []models.HealthCheck{
		{Grade: "A", CreatedAt: newest},
		{Grade: "C", CreatedAt: older},
	}))
	assertOrderedContains(t, healthBody, "A", "C")
	for _, expected := range []string{`text-success`, `text-warning`, "Aug 16", "Aug 15"} {
		if !strings.Contains(healthBody, expected) {
			t.Fatalf("expected health history to contain %q\n%s", expected, healthBody)
		}
	}

	ideaBody := renderInsightsComponentForTest(t, IdeaGradeDisplay(&models.IdeaGrade{Grade: "B-", CreatedAt: newest}, []models.IdeaGrade{
		{Grade: "B-", CreatedAt: newest},
		{Grade: "D", CreatedAt: older},
	}))
	assertOrderedContains(t, ideaBody, "B-", "D")
	for _, expected := range []string{`text-info`, `text-warning/80`, "Aug 16", "Aug 15"} {
		if !strings.Contains(ideaBody, expected) {
			t.Fatalf("expected idea history to contain %q\n%s", expected, ideaBody)
		}
	}
}

func TestInsightsGradeColorClassUnchanged(t *testing.T) {
	cases := map[string]string{
		"A+": "text-success",
		"A":  "text-success",
		"A-": "text-success/80",
		"B+": "text-success/80",
		"B":  "text-info",
		"B-": "text-info",
		"C+": "text-warning",
		"C":  "text-warning",
		"C-": "text-warning/80",
		"D+": "text-warning/80",
		"D":  "text-warning/80",
		"F":  "text-error",
	}
	for grade, want := range cases {
		if got := gradeColorClass(grade); got != want {
			t.Fatalf("gradeColorClass(%q) = %q, want %q", grade, got, want)
		}
	}
}

func assertOrderedContains(t *testing.T, body string, first string, second string) {
	t.Helper()
	firstIndex := strings.Index(body, first)
	secondIndex := strings.Index(body, second)
	if firstIndex == -1 || secondIndex == -1 || firstIndex > secondIndex {
		t.Fatalf("expected %q before %q\n%s", first, second, body)
	}
}
