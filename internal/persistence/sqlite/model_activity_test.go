package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestModelActivityAttributesSharedTaskEffortWithoutDuplicatingCombinedTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := db.Exec(`
 INSERT INTO projects(id,name) VALUES ('p','Project'),('other','Other');
 INSERT INTO agent_configs(id,name,provider,model) VALUES ('a','Astra','openai','astra'),('b','Opus','anthropic','opus');
 INSERT INTO tasks(id,project_id,title,status,merge_status) VALUES ('shared','p','Shared','completed','merged'),('single','p','Single','completed',''),('foreign','other','Foreign','completed','');
 INSERT INTO task_goals(task_id,goal_id,objective,status) VALUES ('shared','goal','Deliver','achieved');
 INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,is_followup) VALUES
 ('old','shared','b','completed','2026-08-01 10:00:00','2026-08-01 10:01:00',1),
 ('a1','shared','a','completed','2026-09-01 10:00:00','2026-09-01 10:01:00',1),
 ('a2','shared','a','failed','2026-09-01 10:02:00','2026-09-01 10:03:00',1),
 ('b1','shared','b','completed','2026-09-01 10:04:00','2026-09-01 10:07:00',0),
 ('a3','single','a','completed','2026-09-01 10:08:00','2026-09-01 10:09:00',0),
 ('foreign-run','foreign','a','completed','2026-09-01 10:00:00','2026-09-01 11:00:00',1);
 INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,operation,total_tokens,cost_usd,occurred_at) VALUES
 ('ua','openai','p','shared','a1','a','astra','task_followup',100,0.1,'2026-09-01 10:01:00'),
 ('ub','anthropic','p','shared','b1','b','opus','task',200,0.2,'2026-09-01 10:07:00'),
 ('us','openai','p','single','a3','a','astra','task',300,0.3,'2026-09-01 10:09:00'),
 ('old-usage','anthropic','p','shared','old','b','opus','task',999,9,'2026-08-01 10:01:00'),
 ('chat-usage','openai','p','shared',NULL,'a','astra','chat',999,9,'2026-09-01 10:01:00');`)
	if err != nil {
		t.Fatal(err)
	}
	filter := persistence.AnalyticsDashboardFilter{ProjectID: "p", View: "models", GroupBy: "month", DateFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	dashboard, err := NewExecutionRepo(db).GetAnalyticsDashboard(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	activity := map[string]models.ModelPerformance{}
	for _, row := range dashboard.ModelActivity {
		activity[row.ModelConfigID] = row
	}
	a, b := activity["a"], activity["b"]
	if len(activity) != 2 || a.TasksUsed != 2 || a.RunCount != 3 || a.TotalFollowUps != 2 || a.TotalDurationMs < 179998 || a.TotalDurationMs > 180002 || a.TotalTokens != 400 || a.TokenCoveredTasks != 2 || a.AverageFollowUps != 1 || a.KnownCostUSD == nil || *a.KnownCostUSD != 0.4 {
		t.Fatalf("Astra activity: %+v", a)
	}
	if a.MedianDurationMs < 89998 || a.MedianDurationMs > 90002 {
		t.Fatalf("Astra time includes another model: %d", a.MedianDurationMs)
	}
	if b.TasksUsed != 1 || b.RunCount != 1 || b.TotalTokens != 200 || b.AverageFollowUps != 0 || b.MedianDurationMs < 179998 || b.MedianDurationMs > 180002 {
		t.Fatalf("Opus activity: %+v", b)
	}
	for _, row := range dashboard.ModelActivity {
		if row.GoalAchievement.Denominator != 0 || row.MergeCompletion.Denominator != 0 {
			t.Fatalf("activity claimed shared outcome: %+v", row)
		}
	}
	if dashboard.TaskSummary.TasksWorkedOn != 2 || dashboard.TaskSummary.GoalAchievement.Numerator != 1 {
		t.Fatalf("duplicated summary: %+v", dashboard.TaskSummary)
	}
	var mixed models.ModelPerformance
	for _, row := range dashboard.Models {
		if row.MixedModels {
			mixed = row
		}
	}
	if mixed.GoalAchievement.Numerator != 1 || mixed.MergeCompletion.Numerator != 1 {
		t.Fatalf("lost shared outcomes: %+v", mixed)
	}
	if len(dashboard.ModelEffortTrend) != 1 {
		t.Fatalf("combined trend: %+v", dashboard.ModelEffortTrend)
	}
	combined := dashboard.ModelEffortTrend[0]
	if combined.Tasks != 2 || combined.TokenSamples != 2 || combined.Tokens != 600 || combined.FollowUps != 2 || combined.DurationSamples != 2 || combined.MedianDurationMs < 179998 || combined.MedianDurationMs > 180002 {
		t.Fatalf("combined must count each task once: %+v", combined)
	}
}

func TestModelEffortTrendsUseActivityDates(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := db.Exec(`
 INSERT INTO projects(id,name) VALUES ('p','Project');
 INSERT INTO agent_configs(id,name,provider,model) VALUES ('a','Astra','openai','astra'),('b','Opus','anthropic','opus');
 INSERT INTO tasks(id,project_id,title,status) VALUES ('t','p','Spans days','completed');
 INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,is_followup) VALUES
 ('a1','t','a','completed','2026-09-01 12:00:00','2026-09-01 13:00:00',0),
 ('a2','t','a','completed','2026-09-02 12:00:00','2026-09-02 14:00:00',1),
 ('b2','t','b','failed','2026-09-02 14:00:00','2026-09-02 15:00:00',1),
 ('running','t','a','running','2026-09-04 12:00:00',NULL,1);
 INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,operation,total_tokens,occurred_at) VALUES
 ('u1','openai','p','t','a1','a','astra','task',100,'2026-09-01 13:00:00'),
 ('u2','openai','p','t','a2','a','astra','task_followup',200,'2026-09-02 14:00:00'),
 ('ub','anthropic','p','t','b2','b','opus','task_followup',300,'2026-09-02 15:00:00'),
 ('late','openai','p','t',NULL,'a','astra','task_followup',50,'2026-09-03 12:00:00');`)
	if err != nil {
		t.Fatal(err)
	}
	filter := persistence.AnalyticsDashboardFilter{ProjectID: "p", View: "models", GroupBy: "day", DateFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)}
	repo := NewExecutionRepo(db)
	dashboard, err := repo.GetAnalyticsDashboard(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]models.ModelPerformance{}
	for _, row := range dashboard.ModelActivity {
		byID[row.ModelConfigID] = row
	}
	a := byID["a"]
	if len(a.EffortTrend) != 4 {
		t.Fatalf("work collapsed onto latest date: %+v", a.EffortTrend)
	}
	check := func(p models.ModelEffortTrend, day string, tokens int64, followups, samples int, duration int64) {
		t.Helper()
		if p.Period != day || p.Tasks != 1 || p.Tokens != tokens || p.FollowUps != followups || p.DurationSamples != samples || p.MedianDurationMs < duration-2 || p.MedianDurationMs > duration+2 {
			t.Fatalf("wrong period activity: %+v", p)
		}
	}
	check(a.EffortTrend[0], "2026-09-01", 100, 0, 1, 3600000)
	check(a.EffortTrend[1], "2026-09-02", 200, 1, 1, 7200000)
	check(a.EffortTrend[2], "2026-09-03", 50, 0, 0, 0)
	check(a.EffortTrend[3], "2026-09-04", 0, 1, 0, 0)
	if a.EffortTrend[3].TokenSamples != 0 || a.EffortTrend[2].TokenSamples != 1 {
		t.Fatal("missing usage must remain missing")
	}
	check(byID["b"].EffortTrend[0], "2026-09-02", 300, 1, 1, 3600000)
	if len(dashboard.ModelEffortTrend) != 4 {
		t.Fatalf("combined dates: %+v", dashboard.ModelEffortTrend)
	}
	check(dashboard.ModelEffortTrend[1], "2026-09-02", 500, 2, 1, 10800000)
	if a.TasksUsed != 1 || a.TotalTokens != 350 || a.RunCount != 3 || a.MedianDurationMs < 10799998 || a.MedianDurationMs > 10800002 {
		t.Fatalf("period comparison changed: %+v", a)
	}
	filter.GroupBy = "month"
	monthly, err := repo.GetAnalyticsDashboard(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(monthly.ModelEffortTrend) != 1 {
		t.Fatalf("monthly: %+v", monthly.ModelEffortTrend)
	}
	check(monthly.ModelEffortTrend[0], "2026-09", 650, 3, 1, 14400000)
	// Half-open date filtering must omit earlier runs without moving their work to later days.
	filter.GroupBy = "day"
	filter.DateFrom = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	filter.DateTo = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	narrowed, err := repo.GetAnalyticsDashboard(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.ModelEffortTrend) != 1 {
		t.Fatalf("range: %+v", narrowed.ModelEffortTrend)
	}
	check(narrowed.ModelEffortTrend[0], "2026-09-02", 500, 2, 1, 10800000)
}
