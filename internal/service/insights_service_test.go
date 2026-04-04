package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/util"
)

func TestInsightsService_GetDashboard(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "Dashboard Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create some insights
	for _, title := range []string{"Bug 1", "Optimization 1"} {
		i := &models.Insight{
			ProjectID:  project.ID,
			Type:       models.InsightBugPattern,
			Severity:   models.InsightSeverityMedium,
			Status:     models.InsightStatusNew,
			Title:      title,
			Evidence:   "{}",
			Confidence: 0.8,
		}
		if err := insightsRepo.CreateInsight(ctx, i); err != nil {
			t.Fatalf("create insight: %v", err)
		}
	}

	dashboard, err := svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if dashboard == nil {
		t.Fatal("expected dashboard data")
	}
	if len(dashboard.NewInsights) != 2 {
		t.Errorf("new insights: got %d, want 2", len(dashboard.NewInsights))
	}
	if dashboard.Stats.TotalInsights != 2 {
		t.Errorf("total insights: got %d, want 2", dashboard.Stats.TotalInsights)
	}
	if dashboard.Stats.NewCount != 2 {
		t.Errorf("new count: got %d, want 2", dashboard.Stats.NewCount)
	}
}

func TestInsightsService_DetectBugPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "Bug Pattern Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create an agent config
	agent := &models.LLMConfig{
		Name:      "test-agent",
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 4096,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Create a task that fails repeatedly
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Flaky deployment task",
		Prompt:    "Deploy to production",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Priority:  2,
		Tag:       models.TagNone,
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create multiple failed executions
	for i := 0; i < 3; i++ {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    "Deploy to production",
		}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create execution: %v", err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "connection timeout to deployment server", 0, 0); err != nil {
			t.Fatalf("complete execution: %v", err)
		}
	}

	insights, err := svc.detectBugPatterns(ctx, project)
	if err != nil {
		t.Fatalf("detect bug patterns: %v", err)
	}
	if len(insights) != 1 {
		t.Fatalf("expected 1 bug pattern insight, got %d", len(insights))
	}
	if insights[0].Type != models.InsightBugPattern {
		t.Errorf("type: got %q, want bug_pattern", insights[0].Type)
	}

	// Running again should not create duplicates
	insights2, err := svc.detectBugPatterns(ctx, project)
	if err != nil {
		t.Fatalf("detect bug patterns (2nd): %v", err)
	}
	if len(insights2) != 0 {
		t.Errorf("expected 0 duplicates, got %d", len(insights2))
	}
}

func TestInsightsService_UpdateAndAcceptInsight(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "Accept Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insight := &models.Insight{
		ProjectID:  project.ID,
		Type:       models.InsightTechDebt,
		Severity:   models.InsightSeverityMedium,
		Status:     models.InsightStatusNew,
		Title:      "Refactor needed",
		Evidence:   "{}",
		Confidence: 0.8,
	}
	if err := insightsRepo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}

	// Accept without task
	if err := svc.AcceptInsight(ctx, insight.ID, nil); err != nil {
		t.Fatalf("accept insight: %v", err)
	}
	got, _ := insightsRepo.GetInsight(ctx, insight.ID)
	if got.Status != models.InsightStatusAccepted {
		t.Errorf("status: got %q, want accepted", got.Status)
	}

	// Update to resolved
	if err := svc.UpdateInsightStatus(ctx, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = insightsRepo.GetInsight(ctx, insight.ID)
	if got.Status != models.InsightStatusResolved {
		t.Errorf("status: got %q, want resolved", got.Status)
	}
}

func TestInsightsService_ExtractJSONArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean array",
			input: `[{"title": "test"}]`,
			want:  `[{"title": "test"}]`,
		},
		{
			name:  "with markdown fences",
			input: "```json\n[{\"title\": \"test\"}]\n```",
			want:  `[{"title": "test"}]`,
		},
		{
			name:  "with surrounding text",
			input: "Here are the suggestions:\n[{\"title\": \"test\"}]\nEnd of suggestions.",
			want:  `[{"title": "test"}]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := util.ExtractJSONArray(tc.input)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInsightsService_ParseHealthCheckResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		grade   string
	}{
		{
			name:  "clean JSON object",
			input: `{"grade":"A-","strengths":"Good velocity","improvements":"Backlog growing","assessment":"Solid work","how_to_improve":"Clear the backlog"}`,
			grade: "A-",
		},
		{
			name:  "with markdown fences",
			input: "```json\n{\"grade\":\"B+\",\"strengths\":\"ok\",\"improvements\":\"ok\",\"assessment\":\"ok\",\"how_to_improve\":\"ok\"}\n```",
			grade: "B+",
		},
		{
			name:  "with surrounding text",
			input: "Here is my analysis:\n{\"grade\":\"C\",\"strengths\":\"some\",\"improvements\":\"many\",\"assessment\":\"needs work\",\"how_to_improve\":\"lots\"}\nDone!",
			grade: "C",
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hc, err := parseHealthCheckResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hc.Grade != tc.grade {
				t.Errorf("grade: got %q, want %q", hc.Grade, tc.grade)
			}
		})
	}
}

func TestInsightsService_ExtractJSONObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean object",
			input: `{"grade": "A"}`,
			want:  `{"grade": "A"}`,
		},
		{
			name:  "with markdown fences",
			input: "```json\n{\"grade\": \"B\"}\n```",
			want:  `{"grade": "B"}`,
		},
		{
			name:  "with surrounding text",
			input: "Here is the result:\n{\"grade\": \"C\"}\nEnd.",
			want:  `{"grade": "C"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := util.ExtractJSONObject(tc.input)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInsightsService_HealthCheckDashboardIntegration(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "Health Dashboard Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Dashboard with no health check yet
	dashboard, err := svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if dashboard.LatestHealthCheck != nil {
		t.Error("expected nil health check for fresh project")
	}

	// Create a health check directly via repo
	hc := &models.HealthCheck{
		ProjectID:        project.ID,
		Grade:            "B",
		Strengths:        "Good work",
		Improvements:     "More tests",
		Assessment:       "On track",
		HowToImprove:     "Add tests",
		TasksTotal:       10,
		TasksCompleted:   7,
		TasksFailed:      1,
		TasksPending:     2,
		BacklogSize:      5,
		AvgCompletionPct: 70.0,
	}
	if err := insightsRepo.CreateHealthCheck(ctx, hc); err != nil {
		t.Fatalf("create health check: %v", err)
	}

	// Dashboard should now include health check
	dashboard, err = svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard 2: %v", err)
	}
	if dashboard.LatestHealthCheck == nil {
		t.Fatal("expected latest health check in dashboard")
	}
	if dashboard.LatestHealthCheck.Grade != "B" {
		t.Errorf("grade: got %q, want B", dashboard.LatestHealthCheck.Grade)
	}
	if len(dashboard.HealthHistory) != 1 {
		t.Errorf("health history: got %d, want 1", len(dashboard.HealthHistory))
	}
}

func TestInsightsService_ParseProactiveSuggestions(t *testing.T) {
	input := `[{"title":"Improve test coverage","description":"Several modules lack tests","suggestion":"Add unit tests","impact":"Better reliability","severity":"medium","confidence":0.8}]`

	suggestions, err := parseProactiveSuggestions(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(suggestions) != 1 {
		t.Fatalf("count: got %d, want 1", len(suggestions))
	}
	if suggestions[0].Title != "Improve test coverage" {
		t.Errorf("title: got %q", suggestions[0].Title)
	}
	if suggestions[0].Confidence != 0.8 {
		t.Errorf("confidence: got %f", suggestions[0].Confidence)
	}
}

func TestInsightsService_ParseIdeaGradeResponse(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantErr   bool
		grade     string
		nextGrade string
	}{
		{
			name:      "clean JSON object",
			input:     `{"grade":"B+","next_grade":"A-","summary":"Good ideas","strengths":"Clear tasks","improvements":"More detail","how_to_next_grade":"Add specifics","clarity_score":75,"ambition_score":80,"follow_through":70,"diversity_score":85,"strategy_score":65}`,
			grade:     "B+",
			nextGrade: "A-",
		},
		{
			name:      "with markdown fences",
			input:     "```json\n{\"grade\":\"A\",\"next_grade\":\"A+\",\"summary\":\"ok\",\"strengths\":\"ok\",\"improvements\":\"ok\",\"how_to_next_grade\":\"ok\",\"clarity_score\":90,\"ambition_score\":90,\"follow_through\":90,\"diversity_score\":90,\"strategy_score\":90}\n```",
			grade:     "A",
			nextGrade: "A+",
		},
		{
			name:      "with surrounding text",
			input:     "Here is the grade:\n{\"grade\":\"C\",\"next_grade\":\"C+\",\"summary\":\"needs work\",\"strengths\":\"some\",\"improvements\":\"many\",\"how_to_next_grade\":\"lots\",\"clarity_score\":40,\"ambition_score\":50,\"follow_through\":30,\"diversity_score\":45,\"strategy_score\":35}\nDone!",
			grade:     "C",
			nextGrade: "C+",
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ig, err := parseIdeaGradeResponse(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ig.Grade != tc.grade {
				t.Errorf("grade: got %q, want %q", ig.Grade, tc.grade)
			}
			if ig.NextGrade != tc.nextGrade {
				t.Errorf("next_grade: got %q, want %q", ig.NextGrade, tc.nextGrade)
			}
		})
	}
}

func TestInsightsService_IdeaGradeDashboardIntegration(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

	project := &models.Project{Name: "Idea Grade Dashboard Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Dashboard with no idea grade yet
	dashboard, err := svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	if dashboard.LatestIdeaGrade != nil {
		t.Error("expected nil idea grade for fresh project")
	}

	// Create an idea grade directly via repo
	ig := &models.IdeaGrade{
		ProjectID:      project.ID,
		Grade:          "B",
		Summary:        "Decent ideas",
		Strengths:      "Good variety",
		Improvements:   "More detail needed",
		HowToNextGrade: "Add specifics",
		NextGrade:      "B+",
		TasksEvaluated: 15,
		ClarityScore:   70.0,
		AmbitionScore:  65.0,
		FollowThrough:  75.0,
		DiversityScore: 80.0,
		StrategyScore:  60.0,
	}
	if err := insightsRepo.CreateIdeaGrade(ctx, ig); err != nil {
		t.Fatalf("create idea grade: %v", err)
	}

	// Dashboard should now include idea grade
	dashboard, err = svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("get dashboard 2: %v", err)
	}
	if dashboard.LatestIdeaGrade == nil {
		t.Fatal("expected latest idea grade in dashboard")
	}
	if dashboard.LatestIdeaGrade.Grade != "B" {
		t.Errorf("grade: got %q, want B", dashboard.LatestIdeaGrade.Grade)
	}
	if len(dashboard.IdeaGradeHistory) != 1 {
		t.Errorf("idea grade history: got %d, want 1", len(dashboard.IdeaGradeHistory))
	}
}
