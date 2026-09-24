package repository

import (
	"context"
	"fmt"
)

type TaskRunPeriod struct {
	AverageRunMs    float64 `json:"average_run_ms"`
	DurationSamples int     `json:"duration_samples"`
	Period          string  `json:"period"`
	Runs            int     `json:"runs"`
	Completed       int     `json:"completed"`
	Failed          int     `json:"failed"`
	Cancelled       int     `json:"cancelled"`
}
type TaskRunModel struct {
	ModelConfigID   string          `json:"model_config_id"`
	ConfigName      string          `json:"config_name"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Runs            int             `json:"runs"`
	AverageRunMs    float64         `json:"average_run_ms"`
	DurationSamples int             `json:"duration_samples"`
	Trend           []TaskRunPeriod `json:"trend"`
}
type TaskRunActivity struct {
	Hours  [24]int        `json:"hours"`
	Models []TaskRunModel `json:"models"`
}

// GetTaskRunActivity counts actual task runs, attributed to the configuration
// that ran them. It excludes chat tasks and uses run-start date throughout.
func (r *ExecutionRepo) GetTaskRunActivity(ctx context.Context, f AnalyticsDashboardFilter) (TaskRunActivity, error) {
	out := TaskRunActivity{Models: []TaskRunModel{}}
	if f.ProjectID == "" {
		return out, fmt.Errorf("project_id is required")
	}
	window, args := analyticsWindowClause("e", f)
	period := analyticsPeriodExpression(f.GroupBy, "e.started_at")
	rows, err := r.db.QueryContext(ctx, `SELECT COALESCE(e.agent_config_id,''),COALESCE(a.name,'Unknown configuration'),COALESCE(a.provider,''),COALESCE(a.model,'Unknown'),COALESCE(a.reasoning_effort,''),`+period+`,CAST(strftime('%H',e.started_at,'localtime') AS INTEGER),COUNT(*),
	SUM(CASE WHEN e.status='completed' THEN 1 ELSE 0 END),SUM(CASE WHEN e.status='failed' THEN 1 ELSE 0 END),SUM(CASE WHEN e.status='cancelled' THEN 1 ELSE 0 END),
	COALESCE(SUM(CASE WHEN e.completed_at IS NOT NULL THEN MAX(0,(julianday(e.completed_at)-julianday(e.started_at))*86400000) END),0),COUNT(e.completed_at)
	FROM executions e JOIN tasks t ON t.id=e.task_id LEFT JOIN agent_configs a ON a.id=e.agent_config_id
	WHERE t.project_id=? AND COALESCE(t.category,'')<>'chat'`+window+` GROUP BY 1,6,7 ORDER BY 1,6,7`, append([]any{f.ProjectID}, args...)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	indices := map[string]int{}
	for rows.Next() {
		var m TaskRunModel
		var p TaskRunPeriod
		var hour, runs, n int
		var duration float64
		if err := rows.Scan(&m.ModelConfigID, &m.ConfigName, &m.Provider, &m.Model, &m.ReasoningEffort, &p.Period, &hour, &runs, &p.Completed, &p.Failed, &p.Cancelled, &duration, &n); err != nil {
			return out, err
		}
		idx, ok := indices[m.ModelConfigID]
		if !ok {
			idx = len(out.Models)
			indices[m.ModelConfigID] = idx
			m.Trend = []TaskRunPeriod{}
			out.Models = append(out.Models, m)
		}
		target := &out.Models[idx]
		p.Runs = runs
		p.AverageRunMs, p.DurationSamples = duration, n
		target.Runs += runs
		target.AverageRunMs += duration
		target.DurationSamples += n
		if hour >= 0 && hour < 24 {
			out.Hours[hour] += runs
		}
		last := len(target.Trend) - 1
		if last >= 0 && target.Trend[last].Period == p.Period {
			target.Trend[last].AverageRunMs += duration
			target.Trend[last].DurationSamples += n
			target.Trend[last].Runs += p.Runs
			target.Trend[last].Completed += p.Completed
			target.Trend[last].Failed += p.Failed
			target.Trend[last].Cancelled += p.Cancelled
		} else {
			target.Trend = append(target.Trend, p)
		}
	}
	for i := range out.Models {
		for j := range out.Models[i].Trend {
			p := &out.Models[i].Trend[j]
			if p.DurationSamples > 0 {
				p.AverageRunMs /= float64(p.DurationSamples)
			}
		}
		if out.Models[i].DurationSamples > 0 {
			out.Models[i].AverageRunMs /= float64(out.Models[i].DurationSamples)
		}
	}
	return out, rows.Err()
}
