package service

import (
	"context"
	"fmt"
	"strings"
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

func TestInsightsService_RunAnalysisCreatesReportAcrossDetectors(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)

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
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at = datetime('now', '-96 hours') WHERE id = ?`, task.ID); err != nil {
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
		if _, err := db.ExecContext(ctx, `UPDATE executions SET started_at = datetime('now', '-10 minutes'), completed_at = datetime('now') WHERE id = ?`, exec.ID); err != nil {
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

func TestInsightsService_GenerateProactiveSuggestionsParsesAndDeduplicates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	mock := testutil.NewMockLLMCaller()
	mock.Response = `[{"title":"Split deployment checks","description":"Deployment work is failing often.","suggestion":"Split smoke tests from release steps.","impact":"Faster diagnosis","severity":"high","confidence":0.95},{"title":"Document retry limits","description":"Retries are implicit.","suggestion":"Write down retry boundaries.","impact":"Fewer duplicate side effects","severity":"medium","confidence":2}]`
	llmSvc.SetLLMCaller(mock)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
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

func TestInsightsService_AIBackedWorkflowsPersistAndListResults(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	insightsRepo := repository.NewInsightsRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	llmSvc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	llmSvc.SetLLMCaller(mock)

	svc := NewInsightsService(insightsRepo, taskRepo, projectRepo, llmConfigRepo, execRepo)
	svc.SetLLMService(llmSvc)

	project := &models.Project{Name: "AI Insights Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := &models.LLMConfig{Name: "insights-agent", Provider: models.ProviderTest, Model: "test-model", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	for i := 0; i < 5; i++ {
		status := models.StatusCompleted
		category := models.CategoryCompleted
		tag := models.TagBug
		if i == 3 {
			status = models.StatusFailed
			category = models.CategoryActive
			tag = models.TagFeature
		}
		if i == 4 {
			status = models.StatusPending
			category = models.CategoryBacklog
			tag = models.TagFeature
		}
		task := &models.Task{ProjectID: project.ID, Title: fmt.Sprintf("AI insight task %d", i), Prompt: strings.Repeat("meaningful task prompt ", 12), Category: category, Status: status, Priority: i%5 + 1, Tag: tag, AgentID: &agent.ID}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
		if status == models.StatusCompleted {
			exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
			if err := execRepo.Create(ctx, exec); err != nil {
				t.Fatalf("create exec %d: %v", i, err)
			}
			if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "implemented", "", 12, 6); err != nil {
				t.Fatalf("complete exec %d: %v", i, err)
			}
		}
	}

	mock.Response = `{"grade":"B+","strengths":"consistent delivery","improvements":"trim backlog","assessment":"healthy and improving","how_to_improve":"ship smaller slices"}`
	health, err := svc.RunHealthCheck(ctx, project.ID)
	if err != nil {
		t.Fatalf("RunHealthCheck: %v", err)
	}
	if health.Grade != "B+" || health.TasksTotal != 5 || health.TasksCompleted != 3 || health.TasksFailed != 1 || health.BacklogSize != 1 {
		t.Fatalf("unexpected health check: %#v", health)
	}
	latestHealth, err := svc.GetLatestHealthCheck(ctx, project.ID)
	if err != nil || latestHealth == nil || latestHealth.ID != health.ID {
		t.Fatalf("latest health = %#v err=%v, want %s", latestHealth, err, health.ID)
	}
	healthHistory, err := svc.ListHealthChecks(ctx, project.ID, 10)
	if err != nil || len(healthHistory) != 1 {
		t.Fatalf("health history len=%d err=%v", len(healthHistory), err)
	}

	mock.Response = `{"grade":"B","next_grade":"B+","summary":"clear enough","strengths":"specific bug fixes","improvements":"more strategy","how_to_next_grade":"connect related work","clarity_score":80,"ambition_score":70,"follow_through":60,"diversity_score":75,"strategy_score":65}`
	grade, err := svc.GradeIdeas(ctx, project.ID)
	if err != nil {
		t.Fatalf("GradeIdeas: %v", err)
	}
	if grade.Grade != "B" || grade.NextGrade != "B+" || grade.TasksEvaluated != 5 || grade.ClarityScore != 80 {
		t.Fatalf("unexpected idea grade: %#v", grade)
	}
	latestGrade, err := svc.GetLatestIdeaGrade(ctx, project.ID)
	if err != nil || latestGrade == nil || latestGrade.ID != grade.ID {
		t.Fatalf("latest grade = %#v err=%v, want %s", latestGrade, err, grade.ID)
	}
	gradeHistory, err := svc.ListIdeaGrades(ctx, project.ID, 10)
	if err != nil || len(gradeHistory) != 1 {
		t.Fatalf("grade history len=%d err=%v", len(gradeHistory), err)
	}

	mock.Response = `[{"topic":"Retry policy","content":"Retries were bounded to avoid duplicate side effects.","tags":["retries","reliability"]},{"topic":"Attachment validation","content":"Files are sniffed before routing to vision models.","tags":["attachments","safety"]}]`
	knowledge, err := svc.ExtractKnowledge(ctx, project.ID)
	if err != nil {
		t.Fatalf("ExtractKnowledge: %v", err)
	}
	if len(knowledge) != 2 {
		t.Fatalf("knowledge len=%d, want 2", len(knowledge))
	}
	tags, err := knowledge[0].ParseTags()
	if err != nil || len(tags) == 0 {
		t.Fatalf("knowledge tags=%v err=%v", tags, err)
	}
	found, err := svc.SearchKnowledge(ctx, project.ID, "Retries")
	if err != nil || len(found) != 1 || found[0].Topic != "Retry policy" {
		t.Fatalf("SearchKnowledge returned %#v err=%v", found, err)
	}
	if err := svc.DeleteKnowledge(ctx, found[0].ID); err != nil {
		t.Fatalf("DeleteKnowledge: %v", err)
	}
	found, err = svc.SearchKnowledge(ctx, project.ID, "Retries")
	if err != nil || len(found) != 0 {
		t.Fatalf("knowledge should be deleted, got %#v err=%v", found, err)
	}

	manual := &models.Insight{ProjectID: project.ID, Type: models.InsightOptimization, Severity: models.InsightSeverityLow, Status: models.InsightStatusNew, Title: "Trim slow startup", Evidence: "{}", Confidence: 0.7}
	if err := insightsRepo.CreateInsight(ctx, manual); err != nil {
		t.Fatalf("create manual insight: %v", err)
	}
	gotInsight, err := svc.GetInsight(ctx, manual.ID)
	if err != nil || gotInsight == nil || gotInsight.Title != manual.Title {
		t.Fatalf("GetInsight = %#v err=%v", gotInsight, err)
	}
	byType, err := svc.ListByType(ctx, project.ID, models.InsightOptimization)
	if err != nil || len(byType) != 1 {
		t.Fatalf("ListByType len=%d err=%v", len(byType), err)
	}
	if err := svc.DeleteInsight(ctx, manual.ID); err != nil {
		t.Fatalf("DeleteInsight: %v", err)
	}
	reports, err := svc.ListReports(ctx, project.ID, 10)
	if err != nil || len(reports) != 0 {
		t.Fatalf("ListReports len=%d err=%v", len(reports), err)
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

func TestParseKnowledgeEntriesExtractsJSONArrays(t *testing.T) {
	entries, err := parseKnowledgeEntries(`Here you go:
[
  {"topic":"deploys","content":"Use staged rollouts","tags":["release","safety"]},
  {"topic":"tests","content":"Keep fixtures small","tags":[]}
]`)
	if err != nil {
		t.Fatalf("parseKnowledgeEntries: %v", err)
	}
	if len(entries) != 2 || entries[0].Topic != "deploys" || entries[0].Tags[1] != "safety" || entries[1].Content != "Keep fixtures small" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if _, err := parseKnowledgeEntries("not json"); err == nil {
		t.Fatal("expected invalid knowledge JSON to fail")
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
