package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const (
	oauthBackgroundRefreshInterval       = 15 * time.Minute
	oauthBackgroundRefreshMinTTL         = time.Hour
	oauthBackgroundIdentityLookupTimeout = 10 * time.Second
)

// OAuthRefreshService keeps built-in OAuth model credentials fresh while the
// models are idle. Credentials are always refreshed and persisted per config.
type OAuthRefreshService struct {
	repo             *repository.LLMConfigRepo
	manager          *llmoauth.Manager
	anthropicRefresh llmoauth.RefreshFunc
	openAIRefresh    llmoauth.RefreshFunc
	identityResolver func(context.Context, string) (AnthropicOAuthIdentity, error)
	identityTimeout  time.Duration
	interval         time.Duration
	wg               sync.WaitGroup
}

func NewOAuthRefreshService(repo *repository.LLMConfigRepo, manager *llmoauth.Manager) *OAuthRefreshService {
	if manager == nil {
		manager = llmoauth.NewManager(repo)
	}
	return &OAuthRefreshService{
		repo:             repo,
		manager:          manager,
		anthropicRefresh: llmoauth.AnthropicRefreshFunc(),
		openAIRefresh:    llmoauth.OpenAIRefreshFunc(),
		identityTimeout:  oauthBackgroundIdentityLookupTimeout,
		interval:         oauthBackgroundRefreshInterval,
	}
}

func (s *OAuthRefreshService) SetRefreshers(anthropicRefresh, openAIRefresh llmoauth.RefreshFunc) {
	if anthropicRefresh != nil {
		s.anthropicRefresh = anthropicRefresh
	}
	if openAIRefresh != nil {
		s.openAIRefresh = openAIRefresh
	}
}

func (s *OAuthRefreshService) SetAnthropicIdentityResolver(resolver func(context.Context, string) (AnthropicOAuthIdentity, error)) {
	s.identityResolver = resolver
}

func (s *OAuthRefreshService) Start(ctx context.Context) {
	if s == nil || s.repo == nil || s.manager == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runAndLog(ctx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runAndLog(ctx)
			}
		}
	}()
}

func (s *OAuthRefreshService) Stop() {
	if s != nil {
		s.wg.Wait()
	}
}

func (s *OAuthRefreshService) runAndLog(ctx context.Context) {
	if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		applog.Infof("[oauth-refresh] background refresh completed with errors: %v", err)
	}
}

func (s *OAuthRefreshService) RunOnce(ctx context.Context) error {
	if s == nil || s.repo == nil || s.manager == nil {
		return fmt.Errorf("OAuth refresh service is not configured")
	}
	configs, err := s.repo.ListRefreshableOAuth(ctx)
	if err != nil {
		return fmt.Errorf("list OAuth model configurations: %w", err)
	}
	var refreshErrors []error
	processedConnections := map[string]struct{}{}
	for _, cfg := range configs {
		current, loadErr := s.repo.GetByID(ctx, cfg.ID)
		if loadErr != nil {
			refreshErrors = append(refreshErrors, loadErr)
			continue
		}
		if current == nil {
			continue
		}
		cfg = *current
		if _, seen := processedConnections[cfg.OAuthConnectionID]; seen {
			continue
		}
		processedConnections[cfg.OAuthConnectionID] = struct{}{}
		if cfg.AuthMethod != models.AuthMethodOAuth || cfg.OAuthNeedsReauth || strings.TrimSpace(cfg.OAuthAccessToken) == "" || strings.TrimSpace(cfg.OAuthRefreshToken) == "" {
			continue
		}
		var refresh llmoauth.RefreshFunc
		switch cfg.Provider {
		case models.ProviderAnthropic:
			refresh = s.anthropicRefresh
		case models.ProviderOpenAI:
			refresh = s.openAIRefresh
		default:
			continue
		}
		fresh, refreshErr := s.manager.EnsureFresh(ctx, cfg, oauthBackgroundRefreshMinTTL, refresh)
		if refreshErr != nil {
			if errors.Is(refreshErr, llmoauth.ErrReauthenticationRequired) {
				continue
			}
			refreshErrors = append(refreshErrors, refreshErr)
			continue
		}
		if cfg.Provider == models.ProviderAnthropic && s.identityResolver != nil {
			identityCtx, cancelIdentity := context.WithTimeout(ctx, s.identityTimeout)
			identity, identityErr := s.identityResolver(identityCtx, fresh.OAuthAccessToken)
			cancelIdentity()
			if identityErr == nil {
				updated, updateErr := s.repo.UpdateLinkedOAuthConnectionProfileIfRevision(
					ctx, fresh.ID, fresh.OAuthConnectionID, fresh.OAuthConfigRevision, fresh.Provider,
					identity.AccountID, identity.DisplayName, identity.PrincipalHash,
				)
				if updateErr != nil {
					refreshErrors = append(refreshErrors, updateErr)
				} else if !updated {
					refreshErrors = append(refreshErrors, fmt.Errorf("OAuth connection changed while its provider profile was being resolved"))
				}
			}
		}
	}
	return errors.Join(refreshErrors...)
}
