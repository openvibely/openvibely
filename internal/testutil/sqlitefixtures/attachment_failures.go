package sqlitefixtures

import "context"

func (f *fixtures) FailChatAttachmentCreation(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_chat_attachment_failure BEFORE INSERT ON chat_attachments BEGIN SELECT RAISE(ABORT, 'injected attachment metadata failure'); END`)
	return err
}
func (f *fixtures) FailTaskClaim(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_task_claim_failure BEFORE UPDATE ON tasks WHEN NEW.status = 'running' BEGIN SELECT RAISE(ABORT, 'injected direct claim failure'); END`)
	return err
}
func (f *fixtures) RetiredAttachmentSessions(ctx context.Context) ([]string, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT session_id FROM retired_attachment_sessions ORDER BY session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
