package models

import "context"

type requiredReviewerIdentityKey struct{}

// WithRequiredReviewerIdentity is server-owned authorization policy, never
// decoded from user input. Authenticated recovery must not infer a reviewer
// identity for historical instructions from a newly created mapping.
func WithRequiredReviewerIdentity(ctx context.Context) context.Context {
	return context.WithValue(ctx, requiredReviewerIdentityKey{}, true)
}
func RequiredReviewerIdentity(ctx context.Context) bool {
	required, _ := ctx.Value(requiredReviewerIdentityKey{}).(bool)
	return required
}
