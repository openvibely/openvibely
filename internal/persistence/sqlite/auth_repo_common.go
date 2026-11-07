package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// deleteByID deletes a single row identified by id from table and returns a
// not-found error (naming entityLabel) when no row matched.
func deleteByID(ctx context.Context, db *sql.DB, table, entityLabel, id string) error {
	result, err := execBoundSQLite(ctx, db, fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), id)
	if err != nil {
		return fmt.Errorf("delete %s: %w", entityLabel, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%s not found", entityLabel)
	}
	return nil
}

// deleteByIDForProject deletes a single row only when both its ID and persisted
// project owner match. A foreign ID is deliberately indistinguishable from a
// missing row to avoid exposing cross-project authorization metadata.
func deleteByIDForProject(ctx context.Context, db *sql.DB, table, entityLabel, projectID, id string) error {
	result, err := execBoundSQLite(ctx, db, fmt.Sprintf(`DELETE FROM %s WHERE id = ? AND project_id = ?`, table), id, projectID)
	if err != nil {
		return fmt.Errorf("delete %s: %w", entityLabel, err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("%s not found", entityLabel)
	}
	return nil
}

// countAny reports whether table contains any rows. errLabel is used only in
// the wrapped error message on query failure.
func countRows(ctx context.Context, db *sql.DB, table, errLabel string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", errLabel, err)
	}
	return count, nil
}

func countRowsByProject(ctx context.Context, db *sql.DB, table, errLabel, projectID string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id = ?`, table), projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", errLabel, err)
	}
	return count, nil
}

func countAny(ctx context.Context, db *sql.DB, table, errLabel string) (bool, error) {
	count, err := countRows(ctx, db, table, errLabel)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// upsertUserProject persists a single-key channel user's active project
// selection, refreshing updated_at on conflict. errLabel is used only in the
// wrapped error message on failure.
func upsertUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, projectID, errLabel string) error {
	query := fmt.Sprintf(
		`INSERT INTO %s (%s, project_id, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(%s) DO UPDATE
		 SET project_id = excluded.project_id, updated_at = datetime('now')`,
		table, keyCol, keyCol)
	if _, err := execBoundSQLite(ctx, db, query, keyVal, projectID); err != nil {
		return fmt.Errorf("set %s: %w", errLabel, err)
	}
	return nil
}

// getUserProject returns the active project ID for a single-key channel
// user, or "" if not set. errLabel is used only in the wrapped error message
// on query failure.
func getUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, errLabel string) (string, error) {
	query := fmt.Sprintf(`SELECT project_id FROM %s WHERE %s = ?`, table, keyCol)
	var projectID string
	err := db.QueryRowContext(ctx, query, keyVal).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get %s: %w", errLabel, err)
	}
	return projectID, nil
}

// deleteUserProject removes a single-key channel user's active project
// selection. errLabel is used only in the wrapped error message on failure.
func deleteUserProject(ctx context.Context, db *sql.DB, table, keyCol, keyVal, errLabel string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, table, keyCol)
	if _, err := execBoundSQLite(ctx, db, query, keyVal); err != nil {
		return fmt.Errorf("delete %s: %w", errLabel, err)
	}
	return nil
}

type taskContextScanner interface {
	Scan(dest ...any) error
}

type taskContextLifecycle[T any] struct {
	table           string
	errLabel        string
	metadataColumns []string
	selectColumns   string
	values          func(T) (string, []any)
	scan            func(taskContextScanner) (T, error)
}

func (h taskContextLifecycle[T]) Upsert(ctx context.Context, exec sqlExecutor, contextValue T) error {
	taskID, metadataValues := h.values(contextValue)
	if len(h.metadataColumns) != len(metadataValues) {
		return fmt.Errorf("upsert %s: metadata column/value mismatch", h.errLabel)
	}

	insertColumns := append([]string{"task_id"}, h.metadataColumns...)
	placeholders := make([]string, len(insertColumns))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	updates := make([]string, len(h.metadataColumns))
	for i, col := range h.metadataColumns {
		updates[i] = fmt.Sprintf("%s = excluded.%s", col, col)
	}
	updates = append(updates, "updated_at = datetime('now')")

	query := fmt.Sprintf(
		`INSERT INTO %s (%s, updated_at)
		 VALUES (%s, datetime('now'))
		 ON CONFLICT(task_id) DO UPDATE SET
		 %s`,
		h.table,
		strings.Join(insertColumns, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(updates, ",\n\t\t "),
	)
	args := append([]any{taskID}, metadataValues...)
	if _, err := exec.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("upsert %s: %w", h.errLabel, err)
	}
	return nil
}

func (h taskContextLifecycle[T]) GetByTaskID(ctx context.Context, db *sql.DB, taskID string) (*T, error) {
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE task_id = ?`, h.selectColumns, h.table)
	v, err := h.scan(db.QueryRowContext(ctx, query, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", h.errLabel, err)
	}
	return &v, nil
}

// deleteByTaskID removes a task-context row keyed by task_id from table.
// errLabel is used only in the wrapped error message on failure.
func deleteByTaskID(ctx context.Context, db *sql.DB, table, taskID, errLabel string) error {
	_, err := execBoundSQLite(ctx, db, fmt.Sprintf(`DELETE FROM %s WHERE task_id = ?`, table), taskID)
	if err != nil {
		return fmt.Errorf("delete %s: %w", errLabel, err)
	}
	return nil
}

// listAuthorizedUsers returns all rows from a channel-auth table ordered by
// added_at ASC, selecting the standard id/project_id/<userCol>/display_name/
// added_at/added_by column shape shared by Discord/Slack/Email auth repos.
// listErrLabel and scanErrLabel are used only in wrapped error messages.
func authorizedUsersListQuery(table, userCol string) string {
	return fmt.Sprintf(
		`SELECT id, project_id, %s, display_name, added_at, added_by, application_user_id, mapping_review, team_id
		 FROM %s
		 ORDER BY added_at ASC`, userCol, table)
}

func authorizedUsersListByProjectQuery(table, userCol string) string {
	return fmt.Sprintf(
		`SELECT id, project_id, %s, display_name, added_at, added_by, application_user_id, mapping_review, team_id
		 FROM %s
		 WHERE project_id = ?
		 ORDER BY added_at ASC`, userCol, table)
}

func listAuthorizedUsers[T any](ctx context.Context, db *sql.DB, table, userCol, listErrLabel, scanErrLabel string, scan func(rows *sql.Rows) (T, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, authorizedUsersListQuery(table, userCol))
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", listErrLabel, err)
	}
	defer rows.Close()

	var items []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", scanErrLabel, err)
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func listAuthorizedUsersByProject[T any](ctx context.Context, db *sql.DB, table, userCol, listErrLabel, scanErrLabel, projectID string, scan func(rows *sql.Rows) (T, error)) ([]T, error) {
	query := authorizedUsersListQuery(table, userCol)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", listErrLabel, err)
	}
	defer rows.Close()

	var items []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", scanErrLabel, err)
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

// getAuthorizedUserByID returns a single row from a channel-auth table by id,
// selecting the standard id/project_id/<userCol>/display_name/added_at/
// added_by column shape. It returns (nil, nil) when no row matches.
// errLabel is used only in the wrapped error message on query failure.
func getAuthorizedUserByID[T any](ctx context.Context, db *sql.DB, table, userCol, errLabel, id string, scan func(row *sql.Row) (T, error)) (*T, error) {
	query := fmt.Sprintf(
		`SELECT id, project_id, %s, display_name, added_at, added_by, application_user_id, mapping_review, team_id
		 FROM %s WHERE id = ?`, userCol, table)
	v, err := scan(db.QueryRowContext(ctx, query, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}
	return &v, nil
}

// countWhere reports whether table has any row matching whereClause/args.
// errLabel is used only in the wrapped error message on query failure.
func countWhere(ctx context.Context, db *sql.DB, table, errLabel, whereClause string, args ...any) (bool, error) {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, table, whereClause)
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false, fmt.Errorf("%s: %w", errLabel, err)
	}
	return count > 0, nil
}

type singleIdentifierAllowlist[T any] struct {
	db                          *sql.DB
	table                       string
	identityColumn              string
	conflictTarget              string
	matchClause                 string
	listErrLabel                string
	scanErrLabel                string
	getErrLabel                 string
	deleteEntityLabel           string
	countAnyErrLabel            string
	checkAnywhereErrLabel       string
	checkProjectErrLabel        string
	updateAddedByOnEmptyDisplay bool
	normalize                   func(string) string
	scan                        func(taskContextScanner) (T, error)
}

func (h singleIdentifierAllowlist[T]) normalizedIdentity(identity string) string {
	if h.normalize == nil {
		return identity
	}
	return h.normalize(identity)
}

func (h singleIdentifierAllowlist[T]) List(ctx context.Context) ([]T, error) {
	return listAuthorizedUsers(ctx, h.db, h.table, h.identityColumn, h.listErrLabel, h.scanErrLabel,
		func(rows *sql.Rows) (T, error) {
			return h.scan(rows)
		})
}

func (h singleIdentifierAllowlist[T]) ListByProject(ctx context.Context, projectID string) ([]T, error) {
	return listAuthorizedUsersByProject(ctx, h.db, h.table, h.identityColumn, h.listErrLabel, h.scanErrLabel, projectID,
		func(rows *sql.Rows) (T, error) {
			return h.scan(rows)
		})
}

func (h singleIdentifierAllowlist[T]) GetByID(ctx context.Context, id string) (*T, error) {
	return getAuthorizedUserByID(ctx, h.db, h.table, h.identityColumn, h.getErrLabel, id,
		func(row *sql.Row) (T, error) {
			return h.scan(row)
		})
}

func (h singleIdentifierAllowlist[T]) Delete(ctx context.Context, id string) error {
	return deleteChannelAllowlist(ctx, h.db, h.table, "", id)
}

func (h singleIdentifierAllowlist[T]) DeleteForProject(ctx context.Context, projectID, id string) error {
	return deleteChannelAllowlist(ctx, h.db, h.table, projectID, id)
}

func (h singleIdentifierAllowlist[T]) HasAny(ctx context.Context) (bool, error) {
	return countAny(ctx, h.db, h.table, h.countAnyErrLabel)
}

func (h singleIdentifierAllowlist[T]) Count(ctx context.Context) (int, error) {
	return countRows(ctx, h.db, h.table, h.countAnyErrLabel)
}

func (h singleIdentifierAllowlist[T]) IsAuthorizedAnywhere(ctx context.Context, identity string) (bool, error) {
	return countWhere(ctx, h.db, h.table, h.checkAnywhereErrLabel, h.matchClause, h.normalizedIdentity(identity))
}

func (h singleIdentifierAllowlist[T]) IsAuthorizedForProject(ctx context.Context, projectID, identity string) (bool, error) {
	return h.IsAuthorizedAnywhere(ctx, identity)
}

func (h singleIdentifierAllowlist[T]) Create(ctx context.Context, projectID, identity, displayName, addedBy string, mapping models.ChannelSenderMapping) (string, string, time.Time, error) {
	identity = h.normalizedIdentity(identity)
	var id string
	var addedAt time.Time
	err := withImmediateTx(ctx, h.db, func(tx sqlExecutor) error {
		var oldUser string
		lookup := tx.QueryRowContext(ctx, `SELECT id, application_user_id FROM `+h.table+` WHERE `+h.matchClause, identity).Scan(&id, &oldUser)
		if lookup != nil && lookup != sql.ErrNoRows {
			return lookup
		}
		actor, err := authorizeSenderMutation(ctx, tx, oldUser, mapping.ApplicationUserID, lookup == nil)
		if err != nil {
			return err
		}
		if actor != "" {
			addedBy = actor
		}
		if lookup == nil && displayName == "" && !h.updateAddedByOnEmptyDisplay {
			if err := tx.QueryRowContext(ctx, `SELECT added_by FROM `+h.table+` WHERE id=?`, id).Scan(&addedBy); err != nil {
				return err
			}
		}
		query := fmt.Sprintf(`INSERT INTO %s (%s,display_name,added_by,application_user_id,team_id) VALUES(?,?,?,?,?) ON CONFLICT(%s) DO UPDATE SET display_name=CASE WHEN excluded.display_name<>'' THEN excluded.display_name ELSE %s.display_name END, added_by=excluded.added_by,application_user_id=excluded.application_user_id,team_id=excluded.team_id,mapping_review=0 RETURNING id,added_at`, h.table, h.identityColumn, h.conflictTarget, h.table)
		if err := tx.QueryRowContext(ctx, query, identity, displayName, addedBy, mapping.ApplicationUserID, mapping.TeamID).Scan(&id, &addedAt); err != nil {
			return err
		}
		if err := syncSenderBinding(ctx, tx, h.table, id, mapping, actor); err != nil {
			return err
		}
		return senderAudit(ctx, tx, h.table, id, actor, "saved", oldUser, mapping.ApplicationUserID)
	})
	return identity, id, addedAt, err
}

// Table names are repository constants, never request input.
func deleteChannelAllowlist(ctx context.Context, db *sql.DB, table, projectID, id string) error {
	return withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT application_user_id FROM `+table+` WHERE id=?`, id).Scan(&old); err != nil {
			return err
		}
		actor, err := authorizeSenderMutation(ctx, tx, old, "", true)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=?`, id); err != nil {
			return err
		}
		return senderAudit(ctx, tx, table, id, actor, "removed", old, "")
	})
}
