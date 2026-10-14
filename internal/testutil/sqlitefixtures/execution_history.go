package sqlitefixtures

import (
	"context"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

func (f *fixtures) SeedExecutionHistory(ctx context.Context, entries []storefixture.ExecutionHistoryEntry) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO executions (id,task_id,agent_config_id,status,prompt_sent,output,error_message,tokens_used,duration_ms,started_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		var completed any
		if e.CompletedAt != nil {
			completed = e.CompletedAt.UTC().Format("2006-01-02 15:04:05")
		}
		if _, err = stmt.ExecContext(ctx, e.ID, e.TaskID, e.AgentConfigID, e.Status, e.PromptSent, e.Output, e.ErrorMessage, e.TokensUsed, e.DurationMs, e.StartedAt.UTC().Format("2006-01-02 15:04:05"), completed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetTaskPayload deliberately preserves status and timestamps for polling stability tests.
func (f *fixtures) SetTaskPayload(ctx context.Context, taskID string, payload storefixture.TaskPayload) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET prompt=?, chain_config=?, swarm_config=? WHERE id=?`, payload.Prompt, payload.ChainConfig, payload.SwarmConfig, taskID)
	return err
}
