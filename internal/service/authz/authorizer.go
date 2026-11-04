package authz

import (
	"context"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

var ErrDenied = persistence.ErrProjectDenied
var ErrSandboxRequired = errors.New("private projects require project sandboxing, which is not available yet")

type Permission string

const (
	View   Permission = "view"
	Edit   Permission = "edit"
	Manage Permission = "manage"
	Own    Permission = "own"
)

func Rank(role string) int {
	switch role {
	case "viewer":
		return 1
	case "editor":
		return 2
	case "maintainer":
		return 3
	case "owner":
		return 4
	}
	return 0
}
func Minimum(p Permission) int {
	switch p {
	case View:
		return 1
	case Edit:
		return 2
	case Manage:
		return 3
	case Own:
		return 4
	}
	return 100
}

type Authorizer struct {
	Store persistence.ProjectAccessStore
}

func (a *Authorizer) Require(ctx context.Context, projectID string, permission Permission) error {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return ErrDenied
	}
	role, err := a.Store.Role(ctx, p.UserID, projectID)
	if err != nil {
		return err
	}
	if Rank(role) < Minimum(permission) {
		return ErrDenied
	}
	return nil
}
