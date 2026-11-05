package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/openvibely/openvibely/internal/pageintegration"
)

// PageConversationRepo persists conversations for any authorized OV page.
type PageConversationRepo struct{ db *sql.DB }

func NewPageConversationRepo(db *sql.DB) *PageConversationRepo { return &PageConversationRepo{db: db} }
func (r *PageConversationRepo) ForPage(ref pageintegration.PageRef) pageintegration.ConversationStore {
	return &pageConversationStore{r.db, ref}
}

type pageConversationStore struct {
	db  *sql.DB
	ref pageintegration.PageRef
}

func (s *pageConversationStore) TaskID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM page_conversations WHERE project_id=? AND kind=? AND instance_id=? AND page_id=?`, s.ref.ProjectID, s.ref.Kind, s.ref.InstanceID, s.ref.PageID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (s *pageConversationStore) BindTask(ctx context.Context, taskID string) error {
	result, err := execBoundSQLite(ctx, s.db, `INSERT INTO page_conversations(project_id,kind,instance_id,page_id,task_id) SELECT ?,?,?,?,id FROM tasks WHERE id=? AND project_id=? ON CONFLICT(project_id,kind,instance_id,page_id) DO NOTHING`, s.ref.ProjectID, s.ref.Kind, s.ref.InstanceID, s.ref.PageID, taskID, s.ref.ProjectID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		id, err := s.TaskID(ctx)
		if err != nil {
			return err
		}
		if id != taskID {
			return errors.New("page conversation already bound or task belongs to another project")
		}
	}
	return nil
}
func (s *pageConversationStore) SaveSnapshot(ctx context.Context, execID, raw string) error {
	result, err := execBoundSQLite(ctx, s.db, `INSERT INTO page_turn_contexts(execution_id,snapshot_json) SELECT e.id,? FROM executions e JOIN page_conversations c ON c.task_id=e.task_id WHERE e.id=? AND c.project_id=? AND c.kind=? AND c.instance_id=? AND c.page_id=?`, raw, execID, s.ref.ProjectID, s.ref.Kind, s.ref.InstanceID, s.ref.PageID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("execution does not belong to this page conversation")
	}
	return nil
}
