package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/util"
)

func TestInsightsService_DetectBugPatterns(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()

	projectRepo := env.Projects
	taskRepo := env.Tasks
	insightsRepo := env.Insights
	llmConfigRepo := env.LLMConfigs
	execRepo := env.Executions

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo)

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

func TestInsightsService_RunAnalysisCreatesReportAcrossDetectors(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()

	projectRepo := env.Projects
	taskRepo := env.Tasks
	insightsRepo := env.Insights
	llmConfigRepo := env.LLMConfigs
	execRepo := env.Executions
	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "Run Analysis Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "analysis-agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	failingTask := &models.Task{ProjectID: project.ID, Title: "Deploy regression", Prompt: "deploy", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2, AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, failingTask); err != nil {
		t.Fatalf("create failing task: %v", err)
	}
	for i := 0; i < 11; i++ {
		exec := &models.Execution{TaskID: failingTask.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "deploy"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create failed exec: %v", err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "deploy timeout", 0, 0); err != nil {
			t.Fatalf("complete failed exec: %v", err)
		}
	}

	for i := 0; i < 6; i++ {
		task := &models.Task{ProjectID: project.ID, Title: fmt.Sprintf("Bug fix %d", i), Prompt: "fix", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, Tag: models.TagBug}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create bug task: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		task := &models.Task{ProjectID: project.ID, Title: fmt.Sprintf("Stale active %d", i), Prompt: "finish", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create stale task: %v", err)
		}
		if err := env.Fixtures.SetTaskCreatedAt(ctx, task.ID, time.Now().UTC().Add(-96*time.Hour)); err != nil {
			t.Fatalf("age stale task: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		task := &models.Task{ProjectID: project.ID, Title: fmt.Sprintf("Slow task %d", i), Prompt: "slow", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, AgentID: &agent.ID}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create slow task: %v", err)
		}
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "slow"}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("create slow exec: %v", err)
		}
		if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "done", "", 0, 0); err != nil {
			t.Fatalf("complete slow exec: %v", err)
		}
		completedAt := time.Now().UTC()
		if err := env.Fixtures.SetExecutionStartedAt(ctx, exec.ID, completedAt.Add(-10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := env.Fixtures.SetExecutionCompletedAt(ctx, exec.ID, completedAt); err != nil {
			t.Fatalf("slow exec timestamps: %v", err)
		}
	}

	report, err := svc.RunAnalysis(ctx, project.ID)
	if err != nil {
		t.Fatalf("RunAnalysis: %v", err)
	}
	if report.ID == "" || report.Summary == "" || report.AnalysisLog == "" {
		t.Fatalf("report missing expected fields: %#v", report)
	}
	for _, want := range []string{"Bug patterns: found 1", "Incomplete features: found 2", "Tech debt: found 1", "Optimizations: found 1"} {
		if !strings.Contains(report.AnalysisLog, want) {
			t.Fatalf("analysis log missing %q:\n%s", want, report.AnalysisLog)
		}
	}
	ids, err := report.ParseInsightIDs()
	if err != nil {
		t.Fatalf("ParseInsightIDs: %v", err)
	}
	if len(ids) != 5 {
		t.Fatalf("expected five detector insights, got %d (%v)", len(ids), ids)
	}

	duplicateReport, err := svc.RunAnalysis(ctx, project.ID)
	if err != nil {
		t.Fatalf("RunAnalysis duplicate: %v", err)
	}
	duplicateIDs, err := duplicateReport.ParseInsightIDs()
	if err != nil {
		t.Fatalf("ParseInsightIDs duplicate: %v", err)
	}
	if len(duplicateIDs) != 0 {
		t.Fatalf("duplicate analysis should not create duplicate insights: %v", duplicateIDs)
	}
}

func TestInsightsService_RunAnalysisStalePendingTasks(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		pending, running, recentPending, backlogPending int
		wantCount                                       int
	}{
		{name: "four old pending", pending: 4, wantCount: 4},
		{name: "four old running", running: 4},
		{name: "three old pending", pending: 3},
		{name: "mixed below threshold", pending: 3, running: 4},
		{name: "mixed above threshold", pending: 4, running: 4, wantCount: 4},
		{name: "recent pending excluded", pending: 3, recentPending: 4},
		{name: "backlog pending excluded", pending: 3, backlogPending: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := teststores.New(t)
			ctx := context.Background()
			projectRepo := env.Projects
			taskRepo := env.Tasks
			insightsRepo := env.Insights
			svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, env.LLMConfigs)
			project := &models.Project{Name: "Stale pending analysis"}
			if err := projectRepo.Create(ctx, project); err != nil {
				t.Fatalf("create project: %v", err)
			}
			for _, group := range []struct {
				count    int
				status   models.TaskStatus
				category models.TaskCategory
				age      time.Duration
			}{
				{tc.pending, models.StatusPending, models.CategoryActive, -96 * time.Hour},
				{tc.running, models.StatusRunning, models.CategoryActive, -96 * time.Hour},
				{tc.recentPending, models.StatusPending, models.CategoryActive, -48 * time.Hour},
				{tc.backlogPending, models.StatusPending, models.CategoryBacklog, -96 * time.Hour},
			} {
				for i := 0; i < group.count; i++ {
					task := &models.Task{ProjectID: project.ID, Title: fmt.Sprintf("%s %s %s task %d", group.category, group.status, group.age, i), Prompt: "finish", Category: group.category, Status: group.status, Priority: 2}
					if err := taskRepo.Create(ctx, task); err != nil {
						t.Fatalf("create task: %v", err)
					}
					if err := env.Fixtures.SetTaskCreatedAt(ctx, task.ID, time.Now().UTC().Add(group.age)); err != nil {
						t.Fatalf("age task: %v", err)
					}
				}
			}

			for run := 0; run < 2; run++ {
				report, err := svc.RunAnalysis(ctx, project.ID)
				if err != nil {
					t.Fatalf("RunAnalysis: %v", err)
				}
				wantNew := 0
				if tc.wantCount > 0 && run == 0 {
					wantNew = 1
				}
				if !strings.Contains(report.AnalysisLog, fmt.Sprintf("Incomplete features: found %d", wantNew)) {
					t.Fatalf("run %d: unexpected analysis log: %s", run, report.AnalysisLog)
				}
				ids, err := report.ParseInsightIDs()
				if err != nil || len(ids) != wantNew {
					t.Fatalf("run %d: insight IDs = %v, error = %v; want %d IDs", run, ids, err, wantNew)
				}
				insights, err := insightsRepo.ListByType(ctx, project.ID, models.InsightIncompleteFeature, 100)
				if err != nil {
					t.Fatalf("list insights: %v", err)
				}
				wantStored := 0
				if tc.wantCount > 0 {
					wantStored = 1
				}
				if len(insights) != wantStored {
					t.Fatalf("run %d: stored insights = %d, want %d", run, len(insights), wantStored)
				}
				for _, insight := range insights {
					if insight.Title != "Stale pending tasks detected" {
						t.Fatalf("unexpected insight: %s", insight.Title)
					}
					var evidence struct {
						StaleCount int `json:"stale_count"`
					}
					if err := json.Unmarshal([]byte(insight.Evidence), &evidence); err != nil {
						t.Fatalf("decode evidence: %v", err)
					}
					if evidence.StaleCount != tc.wantCount {
						t.Fatalf("stale count = %d, want %d", evidence.StaleCount, tc.wantCount)
					}
				}
			}
		})
	}
}

func TestInsightsService_GenerateProactiveSuggestionsParsesAndDeduplicates(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()

	projectRepo := env.Projects
	taskRepo := env.Tasks
	insightsRepo := env.Insights
	llmConfigRepo := env.LLMConfigs
	execRepo := env.Executions
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, env.Schedules, env.Attachments)
	mock := testutil.NewMockLLMCaller()
	mock.Response = `[{"title":"Split deployment checks","description":"Deployment work is failing often.","suggestion":"Split smoke tests from release steps.","impact":"Faster diagnosis","severity":"high","confidence":0.95},{"title":"Document retry limits","description":"Retries are implicit.","suggestion":"Write down retry boundaries.","impact":"Fewer duplicate side effects","severity":"medium","confidence":2}]`
	llmSvc.SetLLMCaller(mock)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo)
	svc.SetLLMService(llmSvc)
	project := &models.Project{Name: "Proactive Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "suggestion-agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	insights, err := svc.generateProactiveSuggestions(ctx, project, 3, 2, 1, 4)
	if err != nil {
		t.Fatalf("generateProactiveSuggestions: %v", err)
	}
	if len(insights) != 2 {
		t.Fatalf("insights len=%d, want 2", len(insights))
	}
	if insights[0].Type != models.InsightProactiveSuggestion || insights[0].Severity != models.InsightSeverityHigh || insights[0].Confidence != 0.95 {
		t.Fatalf("unexpected first suggestion: %#v", insights[0])
	}
	if insights[1].Confidence != 0.5 {
		t.Fatalf("out-of-range confidence should default to 0.5, got %#v", insights[1])
	}

	again, err := svc.generateProactiveSuggestions(ctx, project, 3, 2, 1, 4)
	if err != nil {
		t.Fatalf("generateProactiveSuggestions duplicate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate suggestions should be skipped, got %#v", again)
	}
}

func TestInsightsService_ListAndDeleteInsights(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	insightsRepo := env.Insights
	svc := NewInsightsService(insightsRepo, env.Tasks, env.Projects, env.LLMConfigs)
	project := &models.Project{Name: "Insights Project"}
	if err := env.Projects.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	manual := &models.Insight{ProjectID: project.ID, Type: models.InsightOptimization, Severity: models.InsightSeverityLow, Status: models.InsightStatusNew, Title: "Trim slow startup", Evidence: "{}", Confidence: 0.7}
	if err := insightsRepo.CreateInsight(ctx, manual); err != nil {
		t.Fatalf("create manual insight: %v", err)
	}
	gotInsight, err := svc.GetInsight(ctx, project.ID, manual.ID)
	if err != nil || gotInsight == nil || gotInsight.Title != manual.Title {
		t.Fatalf("GetInsight = %#v err=%v", gotInsight, err)
	}
	byType, err := svc.ListByType(ctx, project.ID, models.InsightOptimization)
	if err != nil || len(byType) != 1 {
		t.Fatalf("ListByType len=%d err=%v", len(byType), err)
	}
	if err := svc.DeleteInsight(ctx, project.ID, manual.ID); err != nil {
		t.Fatalf("DeleteInsight: %v", err)
	}
	reports, err := svc.ListReports(ctx, project.ID, 10)
	if err != nil || len(reports) != 0 {
		t.Fatalf("ListReports len=%d err=%v", len(reports), err)
	}
}

func TestInsightsService_UpdateInsightStatus(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()

	projectRepo := env.Projects
	taskRepo := env.Tasks
	insightsRepo := env.Insights
	llmConfigRepo := env.LLMConfigs

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo)

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
	if err := svc.UpdateInsightStatus(ctx, project.ID, insight.ID, models.InsightStatusAccepted); err != nil {
		t.Fatalf("accept insight: %v", err)
	}
	got, _ := insightsRepo.GetInsight(ctx, project.ID, insight.ID)
	if got.Status != models.InsightStatusAccepted {
		t.Errorf("status: got %q, want accepted", got.Status)
	}

	// Update to resolved
	if err := svc.UpdateInsightStatus(ctx, project.ID, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got, _ = insightsRepo.GetInsight(ctx, project.ID, insight.ID)
	if got.Status != models.InsightStatusResolved {
		t.Errorf("status: got %q, want resolved", got.Status)
	}
}

func TestInsightsService_ProjectScopedMutations(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()

	projectRepo := env.Projects
	insightsRepo := env.Insights
	taskRepo := env.Tasks
	llmConfigRepo := env.LLMConfigs
	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo)

	projectA := &models.Project{Name: "Project A"}
	projectB := &models.Project{Name: "Project B"}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}

	insight := &models.Insight{
		ProjectID:  projectB.ID,
		Type:       models.InsightOptimization,
		Severity:   models.InsightSeverityMedium,
		Status:     models.InsightStatusNew,
		Title:      "Project B insight",
		Evidence:   "{}",
		Confidence: 0.7,
	}
	if err := insightsRepo.CreateInsight(ctx, insight); err != nil {
		t.Fatalf("create insight: %v", err)
	}

	if foreign, err := svc.GetInsight(ctx, projectA.ID, insight.ID); err != nil || foreign != nil {
		t.Fatalf("foreign insight lookup = %#v, err=%v; want not found", foreign, err)
	}
	if err := svc.UpdateInsightStatus(ctx, projectA.ID, insight.ID, models.InsightStatusAccepted); !errors.Is(err, ErrInsightNotFound) {
		t.Fatalf("foreign accept error = %v, want %v", err, ErrInsightNotFound)
	}
	if err := svc.UpdateInsightStatus(ctx, projectA.ID, insight.ID, models.InsightStatusRejected); !errors.Is(err, ErrInsightNotFound) {
		t.Fatalf("foreign status update error = %v, want %v", err, ErrInsightNotFound)
	}
	unchanged, err := svc.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || unchanged == nil || unchanged.Status != models.InsightStatusNew || unchanged.ResolvedAt != nil {
		t.Fatalf("foreign insight changed after rejected service mutations: %#v err=%v", unchanged, err)
	}

	if err := svc.UpdateInsightStatus(ctx, projectB.ID, insight.ID, models.InsightStatusAccepted); err != nil {
		t.Fatalf("same-project accept: %v", err)
	}
	if err := svc.UpdateInsightStatus(ctx, projectB.ID, insight.ID, models.InsightStatusResolved); err != nil {
		t.Fatalf("same-project resolve: %v", err)
	}
	resolved, err := svc.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || resolved == nil || resolved.Status != models.InsightStatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("same-project resolved insight = %#v err=%v", resolved, err)
	}
	resolvedAt := *resolved.ResolvedAt
	if err := svc.UpdateInsightStatus(ctx, projectB.ID, insight.ID, models.InsightStatusRejected); err != nil {
		t.Fatalf("same-project reject: %v", err)
	}
	preserved, err := svc.GetInsight(ctx, projectB.ID, insight.ID)
	if err != nil || preserved == nil || preserved.Status != models.InsightStatusRejected || preserved.ResolvedAt == nil || !preserved.ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("same-project resolved_at preservation failed: %#v err=%v", preserved, err)
	}

	if err := svc.DeleteInsight(ctx, projectA.ID, insight.ID); !errors.Is(err, ErrInsightNotFound) {
		t.Fatalf("foreign insight delete error = %v, want %v", err, ErrInsightNotFound)
	}
	if stillThere, err := svc.GetInsight(ctx, projectB.ID, insight.ID); err != nil || stillThere == nil {
		t.Fatalf("foreign insight disappeared after rejected service delete: %#v err=%v", stillThere, err)
	}

	if err := svc.DeleteInsight(ctx, projectB.ID, insight.ID); err != nil {
		t.Fatalf("same-project insight delete: %v", err)
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
