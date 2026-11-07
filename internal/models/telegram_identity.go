package models

import "context"

type TelegramIngressIdentity struct {
	ExternalUserID int64
	UserID         string
}
type telegramIngressKey struct{}

func WithTelegramIngressIdentity(ctx context.Context, v TelegramIngressIdentity) context.Context {
	return context.WithValue(ctx, telegramIngressKey{}, v)
}
func TelegramIngressIdentityFromContext(ctx context.Context) (TelegramIngressIdentity, bool) {
	v, ok := ctx.Value(telegramIngressKey{}).(TelegramIngressIdentity)
	return v, ok
}
