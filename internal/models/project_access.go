package models

// ProjectAccess is the application policy for one project. Filesystem isolation
// is a separate requirement before private projects may be enabled in production.
type ProjectAccess struct {
	ProjectID   string `json:"project_id"`
	OrgID       string `json:"org_id"`
	OwnerUserID string `json:"owner_user_id"`
	// Legacy policy metadata retained for schema compatibility; neither field grants access.
	Visibility string `json:"visibility"`
	OrgRole    string `json:"org_role"`
}
type ProjectMember struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}
type ProjectAuditEvent struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	ActorUserID string `json:"actor_user_id"`
	Action      string `json:"action"`
	TargetID    string `json:"target_id"`
	Details     string `json:"details"`
	CreatedAt   string `json:"created_at"`
}
