package persistence

import "context"

// SwarmWriteIdentity pins the task identities validated by service admission.
// Persistence rechecks these snapshots in each guarded state-write transaction.
type SwarmWriteIdentity struct {
	TaskID           string
	ProjectID        string
	UserID           string
	AllowLegacyOwner bool
}
type swarmWriteKey struct{}

func WithSwarmWriteIdentities(ctx context.Context, identities []SwarmWriteIdentity) context.Context {
	return context.WithValue(ctx, swarmWriteKey{}, append([]SwarmWriteIdentity(nil), identities...))
}
func SwarmWriteIdentities(ctx context.Context) []SwarmWriteIdentity {
	identities, _ := ctx.Value(swarmWriteKey{}).([]SwarmWriteIdentity)
	return append([]SwarmWriteIdentity(nil), identities...)
}
