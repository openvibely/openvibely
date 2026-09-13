package oauth

import (
	"context"
	"errors"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func AnthropicRefreshFunc() RefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		auth, err := anthropicclient.RefreshTokenContext(ctx, cfg.OAuthRefreshToken)
		if err != nil {
			if errors.Is(err, anthropicclient.ErrOAuthReauthenticationRequired) {
				return TokenSet{}, ErrReauthenticationRequired
			}
			return TokenSet{}, err
		}
		return TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt}, nil
	}
}

func OpenAIRefreshFunc() RefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (TokenSet, error) {
		auth, err := openaiclient.RefreshTokenContext(ctx, cfg.OAuthRefreshToken)
		if err != nil {
			if errors.Is(err, openaiclient.ErrOAuthReauthenticationRequired) {
				return TokenSet{}, ErrReauthenticationRequired
			}
			return TokenSet{}, err
		}
		accountID := strings.TrimSpace(cfg.OAuthAccountID)
		if accountID == "" {
			accountID = openaiclient.ExtractChatGPTAccountID(auth.Token)
		}
		return TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt, AccountID: accountID}, nil
	}
}
