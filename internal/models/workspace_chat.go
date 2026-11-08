package models

import "context"

func WorkspaceChatScope(ctx context.Context, projectID string) string {
	if projectID == AllProjectsID {
		if p, ok := PrincipalFromContext(ctx); ok {
			return projectID + ":user:" + p.UserID
		}
	}
	return projectID
}
