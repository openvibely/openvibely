package sqlite

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// accessibleProjectPredicate scopes aggregate queries to the caller's real
// projects before pagination or aggregation. column is an internal SQL identifier.
// No-principal server jobs retain their existing system-wide behavior.
func accessibleProjectPredicate(ctx context.Context, column string, edit bool) string {
	scope := scopeProjectsPredicate(ctx, column)
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return scope
	}
	// A hex SQL literal preserves the existing query's placeholder layout without
	// interpolating unescaped identity text. The subquery is evaluated by SQLite,
	// not by an extra authorization query per returned row.
	user := "CAST(X'" + hex.EncodeToString([]byte(principal.UserID)) + "' AS TEXT)"
	memberRole := "am.role IN ('viewer','editor','maintainer')"
	if edit {
		memberRole = "am.role IN ('editor','maintainer')"
	}
	return column + ` IN (SELECT ap.project_id FROM project_organizations ap
 JOIN org_memberships om ON om.org_id=ap.org_id
 JOIN users au ON au.id=om.user_id AND au.disabled=0
 LEFT JOIN project_members am ON am.project_id=ap.project_id AND am.user_id=au.id
 WHERE au.id=` + user + ` AND (om.role IN ('owner','admin') OR ap.owner_user_id=au.id OR ` + memberRole + `)) AND ` + scope
}

// aggregateProjectPredicate scopes an All projects or shared project read. A
// shared project without its resolved copies matches nothing.
func aggregateProjectPredicate(ctx context.Context, column, projectID string, edit bool) string {
	if _, ok := models.ScopeProjects(ctx); !ok && models.IsSharedProjectID(projectID) {
		return "0=1"
	}
	return accessibleProjectPredicate(ctx, column, edit)
}

// scopeProjectsPredicate narrows a shared project's reads to its copies here.
func scopeProjectsPredicate(ctx context.Context, column string) string {
	ids, ok := models.ScopeProjects(ctx)
	if !ok {
		return "1=1"
	}
	if len(ids) == 0 {
		return "0=1"
	}
	literals := make([]string, len(ids))
	for i, id := range ids {
		literals[i] = "CAST(X'" + hex.EncodeToString([]byte(id)) + "' AS TEXT)"
	}
	return column + " IN (" + strings.Join(literals, ",") + ")"
}

// Mutations require edit permission even when the virtual workspace is editable.
func editableProjectPredicate(ctx context.Context, column, projectID string) string {
	return "(" + analyticsProjectPredicate(ctx, column, projectID) + " AND " + accessibleProjectPredicate(ctx, column, true) + ")"
}

func stringArg(args []any, i int) string {
	if i >= len(args) {
		return ""
	}
	s, _ := args[i].(string)
	return s
}
