package models

import "context"

type channelSenderOptionsKey struct{}
type ChannelSenderOptions struct {
	Users         []OrgUser
	Actor         Principal
	Authenticated bool
}

func WithChannelSenderOptions(ctx context.Context, options ChannelSenderOptions) context.Context {
	return context.WithValue(ctx, channelSenderOptionsKey{}, options)
}
func SenderOptions(ctx context.Context) ChannelSenderOptions {
	v, _ := ctx.Value(channelSenderOptionsKey{}).(ChannelSenderOptions)
	return v
}
func CanManageSender(ctx context.Context, userID string) bool {
	v := SenderOptions(ctx)
	return !v.Authenticated || v.Actor.IsOrgAdmin() || (userID != "" && userID == v.Actor.UserID)
}
