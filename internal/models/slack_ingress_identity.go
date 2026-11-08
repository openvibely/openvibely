package models

import "context"

// SlackIngressIdentity is internal metadata binding a provider workspace and
// sender to the application user selected by an administrator.
type SlackIngressIdentity struct{ TeamID, ExternalUserID, ProjectID, UserID, EventKey string }
type slackIngressIdentityKey struct{}

func WithSlackIngressIdentity(ctx context.Context, identity SlackIngressIdentity) context.Context {
	return context.WithValue(ctx, slackIngressIdentityKey{}, identity)
}
func SlackIngressIdentityFromContext(ctx context.Context) (SlackIngressIdentity, bool) {
	identity, ok := ctx.Value(slackIngressIdentityKey{}).(SlackIngressIdentity)
	return identity, ok
}
