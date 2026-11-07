package sqlite

import (
	"context"
	"encoding/hex"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"strings"
)

// Workspace chat is personal. Empty ownership denotes pre-isolation history,
// which is visible only to current organization administrators. Server recovery
// without a principal may enumerate work, but restores the saved actor to run it.
func workspaceChatPredicate(ctx context.Context, projectColumn, ownerColumn string) string {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return "1=1"
	}
	user := "CAST(X'" + hex.EncodeToString([]byte(p.UserID)) + "' AS TEXT)"
	return "(" + projectColumn + " != '__all_projects__' OR (" + ownerColumn + " = " + user + " OR (" + ownerColumn + " = '' AND EXISTS (SELECT 1 FROM org_memberships m JOIN users u ON u.id=m.user_id WHERE u.id=" + user + " AND u.disabled=0 AND m.org_id='default' AND m.role IN ('owner','admin')))))"
}
func workspaceExecutionPredicate(ctx context.Context) string {
	return "EXISTS (SELECT 1 FROM tasks chat_task WHERE chat_task.id=e.task_id AND " + workspaceChatPredicate(ctx, "chat_task.project_id", "chat_task.chat_owner_user_id") + ")"
}

// ChatResourceAllowed applies to direct object URLs as well as subscriptions.
func (r *ProjectAccessRepo) ChatResourceAllowed(ctx context.Context, kind, id string) error {
	var query string
	switch kind {
	case "task":
		query = "SELECT COUNT(*) FROM tasks WHERE id=? AND " + workspaceChatPredicate(ctx, "project_id", "chat_owner_user_id")
	case "execution":
		query = "SELECT COUNT(*) FROM executions e WHERE e.id=? AND " + workspaceExecutionPredicate(ctx)
	case "input":
		query = "SELECT COUNT(*) FROM thread_inputs WHERE id=? AND " + workspaceChatPredicate(ctx, "project_id", "chat_owner_user_id")
	default:
		return persistence.ErrProjectDenied
	}
	var n int
	if err := r.db.QueryRowContext(ctx, query, id).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return persistence.ErrProjectDenied
	}
	return nil
}
func stampWorkspaceChatOwner(ctx context.Context, tx sqlExecutor, table, id, project string) error {
	if project != models.AllProjectsID {
		return nil
	}
	p, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return nil
	}
	_, err := tx.ExecContext(ctx, "UPDATE "+table+" SET chat_owner_user_id=? WHERE id=?", p.UserID, id)
	return err
}

func (r *ProjectAccessRepo) ChatResourceOwner(ctx context.Context, kind, id string) (string, error) {
	var q string
	switch kind {
	case "execution":
		q = "SELECT t.chat_owner_user_id FROM tasks t JOIN executions e ON e.task_id=t.id WHERE e.id=?"
	case "task":
		q = "SELECT chat_owner_user_id FROM tasks WHERE id=?"
	case "input":
		q = "SELECT chat_owner_user_id FROM thread_inputs WHERE id=?"
	default:
		return "", persistence.ErrProjectDenied
	}
	var owner string
	err := r.db.QueryRowContext(ctx, q, id).Scan(&owner)
	return owner, err
}

// The SELECT list contains ordered output subqueries; only modify the outer query.
func scopeWorkspaceExecutions(ctx context.Context, query string) string {
	i := strings.LastIndex(query, "ORDER BY")
	return query[:i] + " AND " + workspaceExecutionPredicate(ctx) + " " + query[i:]
}
