package sqlitefixtures

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

func (f *fixtures) SeedAnalyticsWorkload(ctx context.Context, kind storefixture.AnalyticsWorkload) (string, error) {
	var project, statement string
	switch kind {
	case storefixture.AnalyticsContention:
		project = "analytics-project"
		statement = `
		INSERT INTO projects(id,name) VALUES ('analytics-project','Analytics project');
		INSERT INTO tasks(id,project_id,title,category,status,created_at)
			VALUES ('analytics-task','analytics-project','Analytics task','backlog','completed',CURRENT_TIMESTAMP);
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<100000)
		INSERT INTO executions(id,task_id,status,started_at,completed_at,is_followup,history_order)
		SELECT printf('analytics-exec-%06d',n),'analytics-task',
			CASE WHEN n%3=0 THEN 'failed' ELSE 'completed' END,
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CASE WHEN n=1 THEN 0 ELSE 1 END,n
		FROM seq;
	`
	case storefixture.AnalyticsAgentEvidence:
		project = "analytics-agents-project"
		statement = `
		INSERT INTO projects(id,name) VALUES ('analytics-agents-project','Analytics Agents project');
		INSERT INTO agents(id,name,scope,enabled,created_at,updated_at)
			VALUES ('analytics-agent','Analytics Agent','project',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<120)
		INSERT INTO tasks(id,project_id,title,category,status,agent_definition_id,created_at)
		SELECT printf('analytics-agent-task-%03d',n),'analytics-agents-project',printf('Analytics Agent task %03d',n),'backlog','completed','analytics-agent',CURRENT_TIMESTAMP FROM seq;
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<6000)
		INSERT INTO executions(id,task_id,status,started_at,completed_at,is_followup,history_order,output)
		SELECT printf('analytics-agent-exec-%05d',n),printf('analytics-agent-task-%03d',((n-1)%120)+1),
			CASE WHEN n%5=0 THEN 'failed' ELSE 'completed' END,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
			CASE WHEN n<=120 THEN 0 ELSE 1 END,n,zeroblob(65536) FROM seq;
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<12000)
		INSERT INTO llm_usage_events(id,provider,project_id,task_id,model,operation,status,total_tokens,cost_usd,occurred_at)
		SELECT printf('analytics-agent-usage-%05d',n),'test','analytics-agents-project',printf('analytics-agent-task-%03d',((n-1)%120)+1),
			'test-model','completion','completed',100,0.01,CURRENT_TIMESTAMP FROM seq;
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<120)
		INSERT INTO skill_analytics_events(id,project_id,task_id,skill_handle,skill_scope,event_type,source,surface,created_at)
		SELECT printf('analytics-agent-skill-%05d',n),'analytics-agents-project',printf('analytics-agent-task-%03d',((n-1)%120)+1),
			'analytics-skill','project','selected','assigned_agent','task_thread',CURRENT_TIMESTAMP FROM seq;
	`
	case storefixture.AnalyticsOverview:
		project = "analytics-large-project"
		statement = `
		INSERT INTO projects(id,name) VALUES ('analytics-large-project','Analytics large project');
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<60)
		INSERT INTO tasks(id,project_id,title,category,status,created_at)
		SELECT printf('analytics-task-%03d',n),'analytics-large-project',printf('Analytics task %03d',n),'backlog','completed',CURRENT_TIMESTAMP FROM seq;
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<1200)
		INSERT INTO executions(id,task_id,status,started_at,completed_at,is_followup,history_order,output)
		SELECT printf('analytics-exec-%05d',n),printf('analytics-task-%03d',((n-1)%60)+1),
			CASE WHEN n%5=0 THEN 'failed' ELSE 'completed' END,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
			CASE WHEN n<=60 THEN 0 ELSE 1 END,n,zeroblob(65536) FROM seq;
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<6000)
		INSERT INTO llm_usage_events(id,provider,project_id,task_id,model,operation,status,total_tokens,cost_usd,occurred_at)
		SELECT printf('analytics-usage-%05d',n),'test','analytics-large-project',printf('analytics-task-%03d',((n-1)%60)+1),
			'test-model','completion','completed',100,0.01,CURRENT_TIMESTAMP FROM seq;
	`
	default:
		return "", fmt.Errorf("unknown analytics workload %q", kind)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, statement); err != nil {
		return "", err
	}
	return project, tx.Commit()
}
func (f *fixtures) SeedTaskFrequencyWorkload(ctx context.Context, projectID, modelID string, count int) ([]string, error) {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%s-frequency-%05d", projectID, i)
		ids = append(ids, id)
		if _, err = tx.ExecContext(ctx, `INSERT INTO tasks (id,project_id,title,prompt) VALUES (?,?,?,'')`, id, projectID, fmt.Sprintf("task title %05d", i)); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO executions (id,task_id,agent_config_id,status,prompt_sent,started_at,completed_at) VALUES (?,?,?,'completed','','2026-09-13 12:34:56','2026-09-13 12:34:56')`, id+"-execution", id, modelID); err != nil {
			return nil, err
		}
	}
	return ids, tx.Commit()
}
