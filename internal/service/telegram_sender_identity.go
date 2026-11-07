package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type telegramSenderResolver interface {
	ResolveApplicationUser(context.Context, int64) (string, error)
}

func (s *TelegramService) senderPrincipalContext(ctx context.Context, externalID int64) (context.Context, error) {
	if s.workerSvc == nil || s.workerSvc.runOrganizations == nil {
		return ctx, nil
	}
	userID := models.BootstrapUserID
	if !s.workerSvc.runAuthOff {
		resolver, ok := s.telegramAuthRepo.(telegramSenderResolver)
		if !ok || externalID <= 0 {
			return ctx, authz.ErrDenied
		}
		var err error
		userID, err = resolver.ResolveApplicationUser(ctx, externalID)
		if err != nil {
			return ctx, err
		}
		if original, ok := models.TelegramIngressIdentityFromContext(ctx); ok && (original.UserID != userID || original.ExternalUserID != externalID) {
			return ctx, authz.ErrDenied
		}
	}
	user, err := s.workerSvc.runOrganizations.UserByID(ctx, userID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || user.Role == "" {
		return ctx, authz.ErrDenied
	}
	ctx = models.WithPrincipal(ctx, user.Principal)
	if !s.workerSvc.runAuthOff {
		ctx = models.WithTelegramIngressIdentity(ctx, models.TelegramIngressIdentity{ExternalUserID: externalID, UserID: userID})
	}
	return ctx, nil
}
func (s *TelegramService) senderProjectContext(ctx context.Context, externalID int64, projectID string, permission authz.Permission) (context.Context, error) {
	ctx, err := s.senderPrincipalContext(ctx, externalID)
	if err != nil {
		return ctx, err
	}
	if s.workerSvc == nil || s.workerSvc.runAccess == nil {
		return ctx, nil
	}
	return ctx, (&authz.Authorizer{Store: s.workerSvc.runAccess}).Require(ctx, projectID, permission)
}

func (s *TelegramService) telegramVisibleProjects(userID int64) ([]models.Project, error) {
	ctx, err := s.senderPrincipalContext(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	return s.projectRepo.List(ctx)
}
