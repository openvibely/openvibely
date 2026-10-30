package sqlitefixtures

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"time"
)

// putTextDisplayOutput stores output as one plain-text display item, which the
// execution projection renders back byte-for-byte.
func putTextDisplayOutput(ctx context.Context, tx *sql.Tx, id, output string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_items WHERE execution_id=? AND lane='display'`, id); err != nil || output == "" {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_items(execution_id,generation,lane,item_id,position,operation,payload,mutation_id,display_bytes)
 VALUES(?1,0,'display','display:000000000000',0,'put',json_object('type','text','text',?2,'complete',json('true')),'fixture',length(CAST(?2 AS BLOB)))`, id, output)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE conversation_items SET snapshot_sequence=sequence WHERE execution_id=? AND lane='display'`, id)
	return err
}

func (f *fixtures) SeedExecutionHistory(ctx context.Context, entries []storefixture.ExecutionHistoryEntry) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO executions (id,task_id,agent_config_id,status,prompt_sent,error_message,tokens_used,duration_ms,started_at,completed_at) VALUES (?,?,NULLIF(?,''),?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range entries {
		var completed any
		if e.CompletedAt != nil {
			completed = e.CompletedAt.UTC().Format("2006-01-02 15:04:05")
		}
		if _, err = stmt.ExecContext(ctx, e.ID, e.TaskID, e.AgentConfigID, e.Status, e.PromptSent, e.ErrorMessage, e.TokensUsed, e.DurationMs, e.StartedAt.UTC().Format("2006-01-02 15:04:05"), completed); err != nil {
			return err
		}
		if err = putTextDisplayOutput(ctx, tx, e.ID, e.Output); err != nil {
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

func (f *fixtures) SeedPendingTasks(ctx context.Context, tasks []storefixture.PendingTask) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, task := range tasks {
		if _, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,title,prompt,category,status) VALUES(?,?,?,?,?,'pending')`, task.ID, task.ProjectID, task.Title, task.Prompt, task.Category); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) SetHistoricalExecutionCompletion(ctx context.Context, id string, at time.Time, duration int64) error {
	_, err := f.db.ExecContext(ctx, `UPDATE executions SET status='completed',duration_ms=?,completed_at=? WHERE id=?`, duration, at.UTC().Format("2006-01-02 15:04:05"), id)
	return err
}

// SetDanglingTaskAgent models legacy references while restoring enforcement on the same connection.
func (f *fixtures) SetDanglingTaskAgent(ctx context.Context, taskID, agentID string) (err error) {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		err = errors.Join(err, restoreErr)
		if restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	_, err = conn.ExecContext(ctx, `UPDATE tasks SET agent_definition_id=? WHERE id=?`, agentID, taskID)
	return err
}
