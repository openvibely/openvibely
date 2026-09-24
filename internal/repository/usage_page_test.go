package repository

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestUsagePageConfigurationBreakdown(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('p','P');
	INSERT INTO agent_configs(id,name,provider,model,reasoning_effort) VALUES
	('a','Primary','openai','same','high'),('b','Backup','openai','same','medium');`)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewUsageRepo(db)
	for _, id := range []string{"a", "b", ""} {
		e := &models.LLMUsageEvent{Provider: "openai", Model: "same", AgentConfigID: id, ProjectID: "p", Operation: "task", InputTokens: 100, OutputTokens: 20, OccurredAt: time.Now()}
		if err := repo.RecordUsageEvent(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	view, err := repo.GetUsagePage(context.Background(), UsageFilter{ProjectID: "p", GroupBy: "day"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.ModelBreakdown) != 1 || len(view.ConfigurationBreakdown) != 3 {
		t.Fatalf("unexpected breakdowns: %+v", view)
	}
	got := map[string]models.ConfigurationUsagePoint{}
	for _, c := range view.ConfigurationBreakdown {
		got[c.ModelConfigID] = c
		if c.TotalTokens != 120 {
			t.Fatalf("wrong configuration tokens: %+v", c)
		}
	}
	if got["a"].ConfigName != "Primary" || got["a"].ReasoningEffort != "high" || got["b"].ConfigName != "Backup" || got["b"].ReasoningEffort != "medium" || got[""].ConfigName != "" {
		t.Fatalf("incorrect attribution: %+v", got)
	}
	if view.ModelBreakdown[0].TotalTokens != 360 || view.Totals.TotalTokens != 360 {
		t.Fatal("model totals changed")
	}
}

func TestUsagePageDailyAverageIncludesIdleDays(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('p','P')`); err != nil {
		t.Fatal(err)
	}
	repo := NewUsageRepo(db)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	filter := UsageFilter{ProjectID: "p", DateFrom: from, DateTo: from.AddDate(0, 0, 7), GroupBy: "week"}
	view, err := repo.GetUsagePage(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if view.UsageCalendarDays != 7 || view.AverageTokensPerDay == nil || *view.AverageTokensPerDay != 0 {
		t.Fatalf("empty selected range: %+v", view)
	}
	if err := repo.RecordUsageEvent(context.Background(), &models.LLMUsageEvent{ProjectID: "p", Provider: "openai", Model: "model", Operation: "task", InputTokens: 700, OccurredAt: from.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	view, err = repo.GetUsagePage(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if view.UsageCalendarDays != 7 || view.AverageTokensPerDay == nil || *view.AverageTokensPerDay != 100 {
		t.Fatalf("idle days must count: %+v", view)
	}
	view, err = repo.GetUsagePage(context.Background(), UsageFilter{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(time.Local)
	if view.UsageCalendarDays != usageCalendarDays(from, time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)) {
		t.Fatal("all time must include days since first usage")
	}
}

func TestUsageCalendarDaysRollingRange(t *testing.T) {
	from := time.Date(2026, 9, 1, 15, 30, 0, 0, time.Local)
	if got := usageCalendarDays(from, from.AddDate(0, 0, 30)); got != 30 {
		t.Fatalf("30-day range counted %d days", got)
	}
}

func TestUsagePageMatchesExistingMetrics(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('p','P')`); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	cost := 1.25
	for i := 0; i < 10; i++ {
		e := &models.LLMUsageEvent{Provider: "openai", Model: "one", ProjectID: "p", Operation: "task", InputTokens: 100, OutputTokens: 20, CachedInputTokens: 30, CacheCreationInputTokens: 10, CacheReadInputTokens: 20, OccurredAt: time.Date(2026, 9, 1+i, 2, 0, 0, 0, time.UTC)}
		if i%2 == 0 {
			e.Model = "two"
			e.CostUSD = &zero
		}
		if i == 3 {
			e.CostUSD = &cost
		}
		if err := repo.RecordUsageEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []string{"hour", "day", "week", "month"} {
		f := UsageFilter{ProjectID: "p", GroupBy: group, DateFrom: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
		got, err := repo.GetUsagePage(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		totals, err := repo.GetUsageTotals(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		rates, err := repo.GetUsageRateBuckets(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		modelRates, err := repo.GetUsageRateBucketsByModel(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		breakdown, err := repo.GetModelUsageBreakdown(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Totals, *totals) || !reflect.DeepEqual(got.UsageRate, rates) || !reflect.DeepEqual(got.UsageRateByModel, modelRates) || !reflect.DeepEqual(got.ModelBreakdown, breakdown) {
			t.Fatalf("%s optimized metrics differ", group)
		}
	}
}

// Opt-in profiling of a running installation, with a read-only connection.
func BenchmarkUsagePageLive(b *testing.B) {
	path := os.Getenv("OPENVIBELY_USAGE_PROFILE_DB")
	project := os.Getenv("OPENVIBELY_USAGE_PROFILE_PROJECT")
	if path == "" || project == "" {
		b.Skip("set read-only profiling database and project")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	repo := NewUsageRepo(db)
	ctx := context.Background()
	now := time.Now()
	f := UsageFilter{ProjectID: project, DateFrom: now.AddDate(0, 0, -30), DateTo: now, GroupBy: "day"}
	b.Run("previous", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := repo.GetUsageTotals(ctx, f); err != nil {
				b.Fatal(err)
			}
			if _, err := repo.GetDailyUsage(ctx, f); err != nil {
				b.Fatal(err)
			}
			if _, err := repo.GetDailyUsageByModel(ctx, f); err != nil {
				b.Fatal(err)
			}
			if _, err := repo.GetUsageRateBuckets(ctx, f); err != nil {
				b.Fatal(err)
			}
			if _, err := repo.GetUsageRateBucketsByModel(ctx, f); err != nil {
				b.Fatal(err)
			}
			if _, err := repo.GetModelUsageBreakdown(ctx, f); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("single_pass", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := repo.GetUsagePage(ctx, f); err != nil {
				b.Fatal(err)
			}
		}
	})
}
