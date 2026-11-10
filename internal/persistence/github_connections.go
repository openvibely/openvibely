package persistence

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

// GitHubConnectionStore deliberately accepts no target user ID: every operation
// addresses the authenticated principal's connection, including for org admins.
// Background operations must restore their recorded principal before calling it.
type GitHubConnectionStore interface {
	GetOwn(context.Context) (*models.GitHubConnection, error)
	SaveOwn(context.Context, models.GitHubConnection, int64) (bool, error)
	DeleteOwn(context.Context, int64) (bool, error)
}
