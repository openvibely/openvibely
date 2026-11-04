package models

import "context"

// XIngressIdentity binds a provider-supplied immutable sender ID to an
// administrator-attested application identity. It is internal admission metadata.
type XIngressIdentity struct{ InstallerUserID, ExternalUserID, ProjectID, UserID string }
type xIngressIdentityKey struct{}

func WithXIngressIdentity(ctx context.Context, identity XIngressIdentity) context.Context {
	return context.WithValue(ctx, xIngressIdentityKey{}, identity)
}
func XIngressIdentityFromContext(ctx context.Context) (XIngressIdentity, bool) {
	identity, ok := ctx.Value(xIngressIdentityKey{}).(XIngressIdentity)
	return identity, ok
}
