package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

const maxAnthropicOAuthProfileBytes = 1 << 20

// AnthropicOAuthIdentity contains only the provider profile fields safe for
// connection ownership and account selection. PrincipalHash is a one-way hash
// of provider-supplied user/account UUID evidence and is never serialized.
type AnthropicOAuthIdentity struct {
	AccountID     string
	DisplayName   string
	PrincipalHash string
}

// ResolveAnthropicOAuthIdentity obtains current identity evidence for one
// access token. Provider response bodies and private identity fields are never
// included in returned errors.
func ResolveAnthropicOAuthIdentity(ctx context.Context, accessToken string) (AnthropicOAuthIdentity, error) {
	endpoint := strings.TrimRight(anthropicclient.AnthropicAPIHost, "/") + "/api/oauth/profile"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AnthropicOAuthIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", anthropicclient.OAuthBetaHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AnthropicOAuthIdentity{}, fmt.Errorf("Anthropic OAuth profile request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnthropicOAuthProfileBytes))
		return AnthropicOAuthIdentity{}, fmt.Errorf("Anthropic OAuth profile returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnthropicOAuthProfileBytes+1))
	if err != nil || len(data) > maxAnthropicOAuthProfileBytes {
		return AnthropicOAuthIdentity{}, fmt.Errorf("Anthropic OAuth profile response was invalid")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return AnthropicOAuthIdentity{}, fmt.Errorf("Anthropic OAuth profile response was invalid")
	}
	profile := normalizeAnthropicOAuthProfile(raw)
	return AnthropicOAuthIdentity{
		AccountID:     profile.AccountID,
		DisplayName:   profile.DisplayName,
		PrincipalHash: profile.PrincipalHash,
	}, nil
}

func anthropicOAuthPrincipalHash(organizationID, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("anthropic\x00" + strings.TrimSpace(organizationID) + "\x00" + userID))
	return hex.EncodeToString(sum[:])
}
