package persistence

import (
	"errors"
)

// ErrWebhookNotFound indicates that a webhook mutation matched no endpoint.
var ErrWebhookNotFound = errors.New("webhook not found")

// ListCardsByProjectPage returns one bounded, project-scoped webhook card page.
type WebhookCardFilter struct {
	Search  string
	Enabled *bool
	Sort    string
}
