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
	oauthBackgroundRefreshInterval = 15 * time.Minute
	oauthBackgroundRefreshMinTTL   = time.Hour
)

// OAuthRefreshService keeps built-in OAuth model credentials fresh while the
// models are idle. Credentials are always refreshed and persisted per config.
type OAuthRefreshService struct {
	repo             *repository.LLMConfigRepo
	manager          *llmoauth.Manager
	anthropicRefresh llmoauth.RefreshFunc
	openAIRefresh    llmoauth.RefreshFunc
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
	for _, cfg := range configs {
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
		if _, refreshErr := s.manager.EnsureFresh(ctx, cfg, oauthBackgroundRefreshMinTTL, refresh); refreshErr != nil {
			if errors.Is(refreshErr, llmoauth.ErrReauthenticationRequired) {
				continue
			}
			refreshErrors = append(refreshErrors, refreshErr)
		}
	}
	return errors.Join(refreshErrors...)
}
