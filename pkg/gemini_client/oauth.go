package geminiclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
)

const (
	baseOAuthScopes = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"
)

const (
	OAuthClientID             = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	OAuthClientSecret         = "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"
	BusinessOAuthClientID     = "884354919052-36trc1jjb3tguiac32ov6cod268c5blh.apps.googleusercontent.com"
	BusinessOAuthClientSecret = "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"
	BusinessOAuthScopes       = baseOAuthScopes + " https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs"
	OAuthScopes               = BusinessOAuthScopes + " https://www.googleapis.com/auth/aicode openid"
)

// Endpoints are variables so tests can use local servers.
var (
	OAuthAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	OAuthTokenURL     = "https://oauth2.googleapis.com/token"
	UserInfoURL       = "https://www.googleapis.com/oauth2/v2/userinfo"
	DefaultAPIBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	CodeAssistBaseURL = "https://cloudcode-pa.googleapis.com/v1internal"
)

// ErrOAuthReauthenticationRequired means the refresh token was revoked or expired.
var ErrOAuthReauthenticationRequired = errors.New("gemini OAuth reauthentication required")

var oauthHTTPClient = httpclient.New(httpclient.OAuthTimeout)

// OAuthClient identifies the Google OAuth client that issued a token.
type OAuthClient struct {
	ID          string
	Secret      string
	AccountType string
}

// BuiltInOAuthClient only accepts loopback redirect URIs.
func BuiltInOAuthClient() OAuthClient {
	return OAuthClient{ID: OAuthClientID, Secret: OAuthClientSecret}
}

// HostedOAuthClient returns the operator-registered web client used for
// public callbacks, if GEMINI_OAUTH_CLIENT_ID is set.
func BusinessOAuthClient() OAuthClient {
	return OAuthClient{ID: BusinessOAuthClientID, Secret: BusinessOAuthClientSecret}
}

func OAuthScopesForClient(id string) string {
	switch id {
	case BusinessOAuthClientID:
		return BusinessOAuthScopes
	default:
		return OAuthScopes
	}
}

func HostedOAuthClient() (OAuthClient, bool) {
	id := strings.TrimSpace(os.Getenv("GEMINI_OAUTH_CLIENT_ID"))
	if id == "" {
		return OAuthClient{}, false
	}
	return OAuthClient{ID: id, Secret: strings.TrimSpace(os.Getenv("GEMINI_OAUTH_CLIENT_SECRET"))}, true
}

type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// AuthorizationURL builds a PKCE consent URL that returns a refresh token.
func AuthorizationURL(clientID, redirectURI, state, codeChallenge string, scopes ...string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", OAuthScopesForClient(clientID))
	if len(scopes) > 0 && scopes[0] != "" {
		q.Set("scope", scopes[0])
	}
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return OAuthAuthorizeURL + "?" + q.Encode()
}

func ExchangeCode(ctx context.Context, client OAuthClient, code, redirectURI, codeVerifier string) (*Tokens, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)
	values.Set("code_verifier", codeVerifier)
	tokens, err := postToken(ctx, client, values, "")
	if err == nil {
		tokens.RefreshToken = encodeRefreshCredential(client.ID, tokens.RefreshToken, client.AccountType)
	}
	return tokens, err
}

const refreshCredentialPrefix = "ov-gemini-v1:"

type refreshCredential struct {
	ClientID    string `json:"client_id"`
	Token       string `json:"token"`
	AccountType string `json:"account_type,omitempty"`
}

func encodeRefreshCredential(id, token string, accountTypes ...string) string {
	if token == "" {
		return ""
	}
	credential := refreshCredential{ClientID: id, Token: token}
	if len(accountTypes) > 0 {
		credential.AccountType = accountTypes[0]
	}
	b, _ := json.Marshal(credential)
	return refreshCredentialPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// OAuthBaseURL selects the service for the credential's account type.
// A configured base URL remains authoritative for custom deployments and tests.
func OAuthBaseURL(token string) string {
	if CodeAssistBaseURL != "https://cloudcode-pa.googleapis.com/v1internal" {
		return CodeAssistBaseURL
	}
	if OAuthAuthMethod(token) == "consumer" {
		return "https://daily-cloudcode-pa.googleapis.com/v1internal"
	}
	return CodeAssistBaseURL
}

// OAuthUserAgent returns the client metadata used by model and account requests.
func OAuthUserAgent(token string) string { return codeAssistUserAgent(OAuthAuthMethod(token)) }

func OAuthAuthMethod(token string) string {
	encoded, ok := strings.CutPrefix(token, refreshCredentialPrefix)
	if !ok {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	var credential refreshCredential
	if err != nil || json.Unmarshal(raw, &credential) != nil {
		return ""
	}
	switch credential.AccountType {
	case "personal":
		return "consumer"
	case "business":
		return "gcp"
	}
	switch credential.ClientID {
	case OAuthClientID:
		return "consumer"
	case BusinessOAuthClientID:
		return "gcp"
	}
	return ""
}

// SetOAuthCredential selects protocol authentication metadata from the client
// that issued the refresh credential. It does not retain the credential.
func (c *Client) SetOAuthCredential(token string) {
	c.oauthAuthMethod = OAuthAuthMethod(token)
	if !c.codeAssistURLOverride {
		c.codeAssistURL = OAuthBaseURL(token)
	}
}

func RefreshTokenContext(ctx context.Context, refreshToken string) (*Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("%w: missing refresh token", ErrOAuthReauthenticationRequired)
	}
	encoded, ok := strings.CutPrefix(refreshToken, refreshCredentialPrefix)
	if !ok {
		return nil, fmt.Errorf("%w: reconnect Google account to replace an unsupported development credential", ErrOAuthReauthenticationRequired)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	var credential refreshCredential
	if err != nil || json.Unmarshal(raw, &credential) != nil || credential.Token == "" {
		return nil, fmt.Errorf("%w: invalid Gemini credential", ErrOAuthReauthenticationRequired)
	}
	clients := []OAuthClient{BuiltInOAuthClient(), BusinessOAuthClient()}
	if hosted, ok := HostedOAuthClient(); ok {
		clients = append(clients, hosted)
	}
	for _, client := range clients {
		if client.ID != credential.ClientID {
			continue
		}
		values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.Token}}
		tokens, err := postToken(ctx, client, values, credential.Token)
		if err != nil {
			return nil, err
		}
		tokens.RefreshToken = encodeRefreshCredential(client.ID, tokens.RefreshToken, credential.AccountType)
		return tokens, nil
	}
	return nil, fmt.Errorf("%w: OAuth client configuration changed; reconnect Google account", ErrOAuthReauthenticationRequired)
}

func postToken(ctx context.Context, client OAuthClient, values url.Values, fallbackRefresh string) (*Tokens, error) {
	values.Set("client_id", client.ID)
	if client.Secret != "" {
		values.Set("client_secret", client.Secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpretry.DoRequest(oauthHTTPClient, req)
	if err != nil {
		return nil, fmt.Errorf("gemini OAuth token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var providerError struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &providerError) == nil {
			switch providerError.Error {
			case "invalid_grant":
				return nil, fmt.Errorf("%w: OAuth token request failed with HTTP %d", ErrOAuthReauthenticationRequired, resp.StatusCode)
			case "unauthorized_client", "invalid_client":
				return nil, fmt.Errorf("%w: HTTP %d %s", ErrOAuthReauthenticationRequired, resp.StatusCode, providerError.Error)
			}
		}
		return nil, fmt.Errorf("gemini OAuth token request failed with HTTP %d", resp.StatusCode)
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode gemini OAuth token response: %w", err)
	}
	if result.AccessToken == "" {
		return nil, fmt.Errorf("gemini OAuth token response did not include access_token")
	}
	if result.RefreshToken == "" {
		result.RefreshToken = fallbackRefresh
	}
	if result.ExpiresIn <= 0 {
		result.ExpiresIn = 3600
	}
	return &Tokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UnixMilli(),
	}, nil
}

// UserInfo is the signed-in Google account. ID is Google's stable user ID.
type UserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func FetchUserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoURL, nil)
	if err != nil {
		return UserInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpretry.DoRequest(oauthHTTPClient, req)
	if err != nil {
		return UserInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return UserInfo{}, fmt.Errorf("gemini userinfo failed with HTTP %d", resp.StatusCode)
	}
	var info UserInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return UserInfo{}, err
	}
	info.ID, info.Email = strings.TrimSpace(info.ID), strings.TrimSpace(info.Email)
	return info, nil
}

// PrincipalHash identifies one Google user within one Code Assist project
// without storing the raw Google user ID.
func PrincipalHash(project, userID string) string {
	if project == "" || userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("gemini\x00" + project + "\x00" + userID))
	return hex.EncodeToString(sum[:])
}

func SetupGoogleAccountWithCredential(ctx context.Context, accessToken, projectHint, credential string) (string, error) {
	return setupCodeAssist(ctx, accessToken, projectHint, map[string]string{
		"ideType": "\x41\x4e\x54\x49\x47\x52\x41\x56\x49\x54\x59", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI",
	}, credential)
}

func setupCodeAssist(ctx context.Context, accessToken, projectHint string, metadata map[string]string, credential string) (string, error) {
	load := map[string]any{"metadata": withProject(metadata, projectHint)}
	if projectHint != "" {
		load["cloudaicompanionProject"] = projectHint
	}
	var loaded struct {
		CurrentTier *struct {
			ID string `json:"id"`
		} `json:"currentTier"`
		PaidTier *struct {
			ID string `json:"id"`
		} `json:"paidTier"`
		AllowedTiers []struct {
			ID                                 string `json:"id"`
			IsDefault                          bool   `json:"isDefault"`
			UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject"`
		} `json:"allowedTiers"`
		Project string `json:"cloudaicompanionProject"`
	}
	if err := codeAssistPost(ctx, accessToken, "loadCodeAssist", load, &loaded, credential); err != nil {
		return "", err
	}
	if loaded.CurrentTier != nil || loaded.PaidTier != nil {
		if loaded.Project != "" {
			return loaded.Project, nil
		}
		if projectHint != "" {
			return projectHint, nil
		}
		return "", fmt.Errorf("this Google account requires a Google Cloud project for Gemini Code Assist")
	}
	tier, userProject := "free-tier", false
	for _, t := range loaded.AllowedTiers {
		if t.IsDefault {
			tier, userProject = t.ID, t.UserDefinedCloudaicompanionProject
			break
		}
	}
	if userProject && projectHint == "" {
		return "", fmt.Errorf("this Google account requires a Google Cloud project for Gemini Code Assist")
	}
	onboard := map[string]any{"tierId": tier}
	if tier == "free-tier" {
		onboard["metadata"] = metadata
	} else {
		onboard["cloudaicompanionProject"] = projectHint
		onboard["metadata"] = withProject(metadata, projectHint)
	}
	for attempt := 0; attempt < 30; attempt++ {
		var op struct {
			Done     bool `json:"done"`
			Response struct {
				Project struct {
					ID string `json:"id"`
				} `json:"cloudaicompanionProject"`
			} `json:"response"`
		}
		if err := codeAssistPost(ctx, accessToken, "onboardUser", onboard, &op, credential); err != nil {
			return "", err
		}
		if op.Done {
			if op.Response.Project.ID != "" {
				return op.Response.Project.ID, nil
			}
			if projectHint != "" {
				return projectHint, nil
			}
			return "", fmt.Errorf("gemini Code Assist onboarding returned no project")
		}
		if err := httpretry.Wait(ctx, 2*time.Second); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("gemini Code Assist onboarding did not complete")
}

func withProject(metadata map[string]string, project string) map[string]string {
	out := make(map[string]string, len(metadata)+1)
	for k, v := range metadata {
		out[k] = v
	}
	if project != "" {
		out["duetProject"] = project
	}
	return out
}

func codeAssistPost(ctx context.Context, accessToken, method string, payload, out any, credential string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthBaseURL(credential)+":"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", OAuthUserAgent(credential))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpretry.DoRequest(oauthHTTPClient, req)
	if err != nil {
		return fmt.Errorf("gemini Code Assist %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gemini Code Assist %s failed with HTTP %d: %s", method, resp.StatusCode, boundedErrorText(raw))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode gemini Code Assist %s: %w", method, err)
	}
	return nil
}

func boundedErrorText(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	text := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
		text = envelope.Error.Message
	}
	if len(text) > 500 {
		text = text[:500] + "..."
	}
	return text
}
