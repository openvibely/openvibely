package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// OpenAIOAuthIdentity contains the provider-issued identity evidence needed to
// associate models with one OAuth account. PrincipalHash is irreversible and
// includes both the user subject and ChatGPT account so separate workspaces or
// separate users in one workspace are never treated as interchangeable.
type OpenAIOAuthIdentity struct {
	AccountID     string
	DisplayName   string
	PrincipalHash string
}

// ResolveOpenAIOAuthIdentity reads identity claims from a token returned by the
// OpenAI OAuth server. It deliberately requires both subject and account ID
// before producing credential-sharing evidence.
func ResolveOpenAIOAuthIdentity(token string) OpenAIOAuthIdentity {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[1] == "" {
		return OpenAIOAuthIdentity{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return OpenAIOAuthIdentity{}
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Auth    struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		Profile struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"https://api.openai.com/profile"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return OpenAIOAuthIdentity{}
	}
	accountID := strings.TrimSpace(claims.Auth.ChatGPTAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(claims.ChatGPTAccountID)
	}
	displayName := firstNonEmptyOAuthIdentityString(claims.Profile.Email, claims.Email, claims.Profile.Name, claims.Name)
	subject := strings.TrimSpace(claims.Subject)
	principalHash := ""
	if subject != "" && accountID != "" {
		sum := sha256.Sum256([]byte("openai\x00" + accountID + "\x00" + subject))
		principalHash = hex.EncodeToString(sum[:])
	}
	return OpenAIOAuthIdentity{AccountID: accountID, DisplayName: strings.TrimSpace(displayName), PrincipalHash: principalHash}
}

func firstNonEmptyOAuthIdentityString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
