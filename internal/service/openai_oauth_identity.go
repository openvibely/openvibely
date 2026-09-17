package service

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	openAIOAuthIssuer   = "https://auth.openai.com"
	openAIOAuthAudience = "https://api.openai.com/v1"
	openAIOAuthJWKSURL  = "https://auth.openai.com/.well-known/jwks.json"
	openAIJWKSMaxBytes  = 1 << 20
	openAIJWKSCacheTTL  = time.Hour
)

type OpenAIOAuthIdentity struct {
	AccountID     string
	DisplayName   string
	PrincipalHash string
}

type OpenAIOAuthIdentityResolver func(context.Context, string) (OpenAIOAuthIdentity, error)

type OpenAIOAuthIdentityVerifier struct {
	issuer, audience, jwksURL string
	client                    *http.Client
	mu                        sync.Mutex
	keys                      map[string]*rsa.PublicKey
	loadedAt                  time.Time
}

func NewOpenAIOAuthIdentityVerifier() *OpenAIOAuthIdentityVerifier {
	return &OpenAIOAuthIdentityVerifier{issuer: openAIOAuthIssuer, audience: openAIOAuthAudience, jwksURL: openAIOAuthJWKSURL, client: http.DefaultClient}
}

var defaultOpenAIOAuthIdentityVerifier = NewOpenAIOAuthIdentityVerifier()

func ResolveVerifiedOpenAIOAuthIdentity(ctx context.Context, token string) (OpenAIOAuthIdentity, error) {
	return defaultOpenAIOAuthIdentityVerifier.Resolve(ctx, token)
}

// Resolve verifies signature, issuer, and audience. Expiration is deliberately
// ignored because an expired provider-signed JWT can still identify its owner.
func (v *OpenAIOAuthIdentityVerifier) Resolve(ctx context.Context, token string) (OpenAIOAuthIdentity, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth token is not a JWT")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerJSON, &header) != nil || header.Algorithm != "RS256" || strings.TrimSpace(header.KeyID) == "" {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth JWT header is invalid")
	}
	key, err := v.signingKey(ctx, header.KeyID)
	if err != nil {
		return OpenAIOAuthIdentity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth JWT signature is invalid")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth JWT signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth JWT payload is invalid")
	}
	claims, err := parseOpenAIOAuthIdentityClaims(payload)
	if err != nil || claims.Issuer != v.issuer || !containsOpenAIAudience(claims.Audience, v.audience) {
		return OpenAIOAuthIdentity{}, fmt.Errorf("OpenAI OAuth JWT issuer or audience is invalid")
	}
	return claims.identity(), nil
}

type openAIOAuthClaims struct {
	Subject  string          `json:"sub"`
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Email    string          `json:"email"`
	Name     string          `json:"name"`
	Auth     struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
	Profile struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"https://api.openai.com/profile"`
	ChatGPTAccountID string `json:"chatgpt_account_id"`
}

func parseOpenAIOAuthIdentityClaims(payload []byte) (openAIOAuthClaims, error) {
	var claims openAIOAuthClaims
	err := json.Unmarshal(payload, &claims)
	return claims, err
}

func (c openAIOAuthClaims) identity() OpenAIOAuthIdentity {
	accountID := strings.TrimSpace(c.Auth.ChatGPTAccountID)
	if accountID == "" {
		accountID = strings.TrimSpace(c.ChatGPTAccountID)
	}
	displayName := firstNonEmptyOAuthIdentityString(c.Profile.Email, c.Email, c.Profile.Name, c.Name)
	subject := strings.TrimSpace(c.Subject)
	principalHash := ""
	if subject != "" && accountID != "" {
		sum := sha256.Sum256([]byte("openai\x00" + accountID + "\x00" + subject))
		principalHash = hex.EncodeToString(sum[:])
	}
	return OpenAIOAuthIdentity{AccountID: accountID, DisplayName: displayName, PrincipalHash: principalHash}
}

func containsOpenAIAudience(raw json.RawMessage, expected string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == expected
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, audience := range many {
		if audience == expected {
			return true
		}
	}
	return false
}

func (v *OpenAIOAuthIdentityVerifier) signingKey(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if key := v.keys[keyID]; key != nil && time.Since(v.loadedAt) < openAIJWKSCacheTTL {
		return key, nil
	}
	if err := v.loadKeys(ctx); err != nil {
		return nil, err
	}
	if key := v.keys[keyID]; key != nil {
		return key, nil
	}
	return nil, fmt.Errorf("OpenAI OAuth JWT signing key is unknown")
}

func (v *OpenAIOAuthIdentityVerifier) loadKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch OpenAI OAuth signing keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, openAIJWKSMaxBytes))
		return fmt.Errorf("fetch OpenAI OAuth signing keys: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, openAIJWKSMaxBytes+1))
	if err != nil || len(data) > openAIJWKSMaxBytes {
		return fmt.Errorf("OpenAI OAuth signing keys response is invalid")
	}
	keys := make(map[string]*rsa.PublicKey)
	var rawSet struct {
		Keys []struct {
			KeyID     string `json:"kid"`
			KeyType   string `json:"kty"`
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &rawSet); err != nil {
		return fmt.Errorf("OpenAI OAuth signing keys response is invalid")
	}
	for _, raw := range rawSet.Keys {
		if raw.KeyType != "RSA" || raw.Algorithm != "RS256" || raw.Use != "sig" || raw.KeyID == "" {
			continue
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(raw.Modulus)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(raw.Exponent)
		if modulusErr != nil || exponentErr != nil || len(modulus) == 0 || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		e := 0
		for _, b := range exponent {
			e = e<<8 | int(b)
		}
		n := new(big.Int).SetBytes(modulus)
		if e < 3 || e%2 == 0 || n.BitLen() < 2048 {
			continue
		}
		keys[raw.KeyID] = &rsa.PublicKey{N: n, E: e}
	}
	if len(keys) == 0 {
		return fmt.Errorf("OpenAI OAuth signing keys response contained no usable keys")
	}
	v.keys, v.loadedAt = keys, time.Now()
	return nil
}

func firstNonEmptyOAuthIdentityString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
