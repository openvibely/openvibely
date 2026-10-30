package models

import "context"

const DefaultOrgID = "default"
const BootstrapUserID = "local-owner"

type Principal struct {
	UserID      string `json:"user_id"`
	OrgID       string `json:"org_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func (p Principal) IsOrgAdmin() bool { return p.Role == "owner" || p.Role == "admin" }

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type OrgUser struct {
	Principal
	PasswordHash string `json:"-"`
	Disabled     bool   `json:"disabled"`
}
type OrgInvitation struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedBy string `json:"created_by"`
	Used      bool   `json:"used"`
	Revoked   bool   `json:"revoked"`
}
