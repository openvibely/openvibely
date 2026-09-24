package repository

import (
	"context"
	"github.com/openvibely/openvibely/internal/testutil"
	"math"
	"testing"
	"time"
)

func TestTaskRunActivityScopesAndAggregates(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := db.Exec(`INSERT INTO projects(id,name) VALUES ('p','P'),('other','Other');
	INSERT INTO agent_configs(id,name,provider,model) VALUES ('m','Model','openai','model');
	INSERT INTO tasks(id,project_id,title,category) VALUES ('t','p','Task','scheduled'),('chat','p','Chat','chat'),('other','other','Other','active');
	INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at) VALUES
	('1','t','m','completed','2026-09-01 10:00:00','2026-09-01 10:10:00'),
	('2','t','m','failed','2026-09-01 10:00:00','2026-09-01 10:20:00'),
	('3','t','m','cancelled','2026-09-01 10:00:00','2026-09-01 10:05:00'),
	('4','t','m','running','2026-09-01 10:00:00',NULL),
	('5','chat','m','completed','2026-09-01 10:00:00','2026-09-01 23:00:00'),
	('6','other','m','completed','2026-09-01 10:00:00','2026-09-01 23:00:00'),
	('7','t','m','completed','2026-10-01 00:00:00','2026-10-01 23:00:00');`)
	if err != nil {
		t.Fatal(err)
	}
	r := NewExecutionRepo(db)
	f := AnalyticsDashboardFilter{ProjectID: "p", DateFrom: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), DateTo: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), GroupBy: "day"}
	data, err := r.GetTaskRunActivity(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Models) != 1 {
		t.Fatalf("models: %+v", data)
	}
	m := data.Models[0]
	if m.Runs != 4 || m.DurationSamples != 3 || math.Abs(m.AverageRunMs-700000) > 1 || len(m.Trend) != 1 || m.Trend[0].Completed != 1 || m.Trend[0].Failed != 1 || m.Trend[0].Cancelled != 1 {
		t.Fatalf("model: %+v", m)
	}
	total := 0
	for _, n := range data.Hours {
		total += n
	}
	if total != 4 {
		t.Fatalf("hours: %+v", data.Hours)
	}
	f.ProjectID = "missing"
	empty, err := r.GetTaskRunActivity(context.Background(), f)
	if err != nil || len(empty.Models) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}
