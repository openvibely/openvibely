package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"golang.org/x/sync/singleflight"
)

// ErrReauthenticationRequired marks a refresh-token failure that cannot recover
// without reconnecting the model configuration.
var ErrReauthenticationRequired = errors.New("OAuth reauthentication required")

// TokenSet is a provider-neutral OAuth token refresh result.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	AccountID    string
}

// RefreshFunc refreshes the selected config's OAuth tokens using its current refresh token.
type RefreshFunc func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error)

// Manager coordinates OAuth refresh for provider adapters. It reloads the exact
// selected config and serializes refresh-token rotation in-process and across
// processes with a durable per-config lease.
type Manager struct {
	repo  *repository.LLMConfigRepo
	group singleflight.Group
}

func NewManager(repo *repository.LLMConfigRepo) *Manager {
	return &Manager{repo: repo}
}

func (m *Manager) EnsureFresh(ctx context.Context, cfg models.LLMConfig, minTTL time.Duration, refresh RefreshFunc) (models.LLMConfig, error) {
	if !cfg.IsOAuth() || strings.TrimSpace(cfg.OAuthAccessToken) == "" {
		return cfg, nil
	}
	if cfg.OAuthNeedsReauth {
		return cfg, ErrReauthenticationRequired
	}
	if cfg.OAuthExpiresAt >= time.Now().Add(minTTL).UnixMilli() {
		return cfg, nil
	}
	fresh, err := m.refreshSelected(ctx, cfg, "", minTTL, refresh)
	if err != nil {
		return cfg, err
	}
	return fresh, nil
}

// RecoverUnauthorized reloads/refreshes the exact selected config after a 401.
// It returns recovered=false when no token change was available.
func (m *Manager) RecoverUnauthorized(ctx context.Context, cfg models.LLMConfig, tokenUsed string, refresh RefreshFunc) (models.LLMConfig, bool, error) {
	if !cfg.IsOAuth() || strings.TrimSpace(tokenUsed) == "" {
		return cfg, false, nil
	}
	fresh, err := m.refreshSelected(ctx, cfg, tokenUsed, 0, refresh)
	if err != nil {
		return cfg, false, err
	}
	return fresh, fresh.OAuthAccessToken != "" && fresh.OAuthAccessToken != tokenUsed, nil
}

func (m *Manager) refreshSelected(ctx context.Context, cfg models.LLMConfig, tokenUsed string, minTTL time.Duration, refresh RefreshFunc) (models.LLMConfig, error) {
	if m == nil || m.repo == nil {
		return cfg, fmt.Errorf("OAuth recovery unavailable for model config %q (provider=%s model=%s)", cfg.Name, cfg.Provider, cfg.Model)
	}
	if strings.TrimSpace(cfg.ID) == "" {
		return cfg, fmt.Errorf("OAuth refresh requires selected model config id for %q", cfg.Name)
	}
	if refresh == nil {
		return cfg, fmt.Errorf("OAuth refresh not implemented for model config %q (provider=%s model=%s)", cfg.Name, cfg.Provider, cfg.Model)
	}

	key := string(cfg.Provider) + ":" + cfg.ID
	value, err, _ := m.group.Do(key, func() (any, error) {
		return m.refreshSelectedLocked(ctx, cfg, tokenUsed, minTTL, refresh)
	})
	if err != nil {
		return cfg, err
	}
	fresh, ok := value.(models.LLMConfig)
	if !ok {
		return cfg, fmt.Errorf("OAuth recovery internal type mismatch for model config %q", cfg.Name)
	}
	return fresh, nil
}

func (m *Manager) refreshSelectedLocked(ctx context.Context, cfg models.LLMConfig, tokenUsed string, minTTL time.Duration, refresh RefreshFunc) (models.LLMConfig, error) {
	owner, err := newRefreshLeaseOwner()
	if err != nil {
		return cfg, err
	}
	const leaseDuration = 45 * time.Second
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		loaded, loadErr := m.repo.GetByID(ctx, cfg.ID)
		if loadErr != nil {
			return cfg, fmt.Errorf("reload selected OAuth config %q (provider=%s model=%s): %w", cfg.Name, cfg.Provider, cfg.Model, loadErr)
		}
		if loaded == nil {
			return cfg, fmt.Errorf("selected OAuth config %q (provider=%s model=%s) no longer exists", cfg.Name, cfg.Provider, cfg.Model)
		}
		if loaded.Provider != cfg.Provider || loaded.AuthMethod != models.AuthMethodOAuth {
			return cfg, fmt.Errorf("selected OAuth config changed authentication context for %q", cfg.Name)
		}
		if loaded.OAuthNeedsReauth {
			return *loaded, ErrReauthenticationRequired
		}
		if strings.TrimSpace(tokenUsed) != "" && loaded.OAuthAccessToken != "" && loaded.OAuthAccessToken != tokenUsed {
			applog.Infof("[oauth-recovery] selected config already refreshed provider=%s model=%s", loaded.Provider, loaded.Model)
			return *loaded, nil
		}
		if strings.TrimSpace(tokenUsed) == "" && minTTL > 0 && loaded.OAuthAccessToken != "" && loaded.OAuthExpiresAt >= time.Now().Add(minTTL).UnixMilli() {
			return *loaded, nil
		}
		if strings.TrimSpace(loaded.OAuthRefreshToken) == "" {
			return *loaded, fmt.Errorf("OAuth refresh unavailable for model config %q (provider=%s model=%s): missing refresh token", loaded.Name, loaded.Provider, loaded.Model)
		}

		acquired, acquireErr := m.repo.TryAcquireOAuthRefreshLease(ctx, loaded.ID, owner, time.Now(), leaseDuration)
		if acquireErr != nil {
			return *loaded, fmt.Errorf("acquire OAuth refresh lease for model config %q: %w", loaded.Name, acquireErr)
		}
		if !acquired {
			select {
			case <-ctx.Done():
				return *loaded, ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = m.repo.ReleaseOAuthRefreshLease(releaseCtx, loaded.ID, owner)
		}()

		tokens, refreshErr := refresh(ctx, *loaded)
		if refreshErr != nil {
			if errors.Is(refreshErr, ErrReauthenticationRequired) {
				changed, markErr := m.repo.MarkOAuthNeedsReauthIfRevision(ctx, loaded.ID, loaded.OAuthConfigRevision, loaded.Provider)
				if markErr != nil {
					return *loaded, fmt.Errorf("persist OAuth reauthentication state for model config %q: %w", loaded.Name, markErr)
				}
				if !changed {
					return *loaded, fmt.Errorf("model config %q changed while OAuth refresh was in progress", loaded.Name)
				}
				loaded.OAuthNeedsReauth = true
				return *loaded, ErrReauthenticationRequired
			}
			return *loaded, fmt.Errorf("OAuth token refresh failed for model config %q (provider=%s model=%s): %w", loaded.Name, loaded.Provider, loaded.Model, refreshErr)
		}
		if strings.TrimSpace(tokens.AccessToken) == "" {
			return *loaded, fmt.Errorf("OAuth token refresh failed for model config %q (provider=%s model=%s): refresh response missing access token", loaded.Name, loaded.Provider, loaded.Model)
		}
		if strings.TrimSpace(tokens.RefreshToken) == "" {
			tokens.RefreshToken = loaded.OAuthRefreshToken
		}
		if tokens.ExpiresAt == 0 {
			tokens.ExpiresAt = loaded.OAuthExpiresAt
		}

		var changed bool
		if tokens.AccountID != "" {
			changed, err = m.repo.UpdateStandardOAuthTokensIfRevision(ctx, loaded.ID, loaded.OAuthConfigRevision, loaded.Provider, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt, tokens.AccountID)
		} else {
			changed, err = m.repo.UpdateStandardOAuthTokensIfRevision(ctx, loaded.ID, loaded.OAuthConfigRevision, loaded.Provider, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresAt)
		}
		if err != nil {
			return *loaded, fmt.Errorf("persist refreshed OAuth tokens for model config %q (provider=%s model=%s): %w", loaded.Name, loaded.Provider, loaded.Model, err)
		}
		if !changed {
			return *loaded, fmt.Errorf("model config %q changed while OAuth refresh was in progress", loaded.Name)
		}

		loaded.OAuthAccessToken = tokens.AccessToken
		loaded.OAuthRefreshToken = tokens.RefreshToken
		loaded.OAuthExpiresAt = tokens.ExpiresAt
		loaded.OAuthNeedsReauth = false
		if tokens.AccountID != "" {
			loaded.OAuthAccountID = tokens.AccountID
		}
		applog.Infof("[oauth-recovery] refreshed selected config provider=%s model=%s expires=%s", loaded.Provider, loaded.Model, time.UnixMilli(loaded.OAuthExpiresAt).Format(time.RFC3339))
		return *loaded, nil
	}
}

func newRefreshLeaseOwner() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate OAuth refresh lease owner: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
