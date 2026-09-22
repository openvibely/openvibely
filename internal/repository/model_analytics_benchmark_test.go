package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil"
)

func BenchmarkModelAnalyticsHistory(b *testing.B) {
	db := testutil.NewTestDB(b)
	_, err := db.Exec(`
	INSERT INTO projects(id,name) VALUES ('bench','Bench');
	INSERT INTO agent_configs(id,name,provider,model,auth_method) VALUES ('m','Model','openai','model','oauth');
	WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<200)
	INSERT INTO tasks(id,project_id,title,status) SELECT 't'||x,'bench','Task '||x,'completed' FROM n;
	WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<20)
	INSERT INTO executions(id,task_id,agent_config_id,status,started_at,completed_at,history_order,prompt_sent,output)
	SELECT t.id||'-'||x,t.id,'m','completed','2026-09-01 10:00:00','2026-09-01 11:00:00',x,hex(zeroblob(4000)),hex(zeroblob(4000)) FROM tasks t CROSS JOIN n;
	INSERT INTO llm_usage_events(id,provider,project_id,task_id,execution_id,agent_config_id,model,total_tokens,cost_usd)
	SELECT id,'openai','bench',task_id,id,'m','model',1000,0.01 FROM executions;
	`)
	if err != nil {
		b.Fatal(err)
	}
	r := NewExecutionRepo(db)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := r.queryModelPerformance(context.Background(), AnalyticsDashboardFilter{ProjectID: "bench", View: "models"})
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 1 || rows[0].RunCount != 4000 {
			b.Fatalf("unexpected results: %+v", rows)
		}
	}
}
