package sqlite

import (
	"context"
	"encoding/hex"

	"github.com/openvibely/openvibely/internal/models"
)

// accessibleProjectPredicate scopes aggregate queries to the caller's real
// projects before pagination or aggregation. column is an internal SQL identifier.
// No-principal server jobs retain their existing system-wide behavior.
func accessibleProjectPredicate(ctx context.Context, column string, edit bool) string {
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return "1=1"
	}
	// A hex SQL literal preserves the existing query's placeholder layout without
	// interpolating unescaped identity text. The subquery is evaluated by SQLite,
	// not by an extra authorization query per returned row.
	user := "CAST(X'" + hex.EncodeToString([]byte(principal.UserID)) + "' AS TEXT)"
	orgRole := "ap.visibility='org'"
	memberRole := "am.role IN ('viewer','editor','maintainer')"
	if edit {
		orgRole += " AND ap.org_role='editor'"
		memberRole = "am.role IN ('editor','maintainer')"
	}
	return column + ` IN (SELECT ap.project_id FROM project_organizations ap
 JOIN org_memberships om ON om.org_id=ap.org_id
 JOIN users au ON au.id=om.user_id AND au.disabled=0
 LEFT JOIN project_members am ON am.project_id=ap.project_id AND am.user_id=au.id
 WHERE au.id=` + user + ` AND (om.role IN ('owner','admin') OR ap.owner_user_id=au.id OR (` + orgRole + `) OR ` + memberRole + `))`
}

// Mutations require edit permission even when the virtual workspace is editable.
func editableProjectPredicate(ctx context.Context, column, projectID string) string {
	return "(" + analyticsProjectPredicate(ctx, column, projectID) + " AND " + accessibleProjectPredicate(ctx, column, true) + ")"
}
