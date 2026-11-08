package models

import "context"

// DiscordIngressIdentity is trusted service metadata, never decoded from tool input.
type DiscordIngressIdentity struct{ ProjectID, ExternalUserID, UserID string }
type discordIngressIdentityKey struct{}

func WithDiscordIngressIdentity(ctx context.Context, identity DiscordIngressIdentity) context.Context {
	return context.WithValue(ctx, discordIngressIdentityKey{}, identity)
}
func DiscordIngressIdentityFromContext(ctx context.Context) (DiscordIngressIdentity, bool) {
	identity, ok := ctx.Value(discordIngressIdentityKey{}).(DiscordIngressIdentity)
	return identity, ok
}
