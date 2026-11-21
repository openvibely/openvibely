package geminiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
)

const (
	OAuthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	OAuthClientSecret = "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"
	OAuthScopes       = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"
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

type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// AuthorizationURL builds a PKCE consent URL that returns a refresh token.
func AuthorizationURL(redirectURI, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", OAuthClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", OAuthScopes)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return OAuthAuthorizeURL + "?" + q.Encode()
}

func ExchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (*Tokens, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("code", code)
	values.Set("redirect_uri", redirectURI)
	values.Set("code_verifier", codeVerifier)
	return postToken(ctx, values, "")
}

func RefreshTokenContext(ctx context.Context, refreshToken string) (*Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("%w: missing refresh token", ErrOAuthReauthenticationRequired)
	}
	values := url.Values{}
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refreshToken)
	return postToken(ctx, values, refreshToken)
}

func postToken(ctx context.Context, values url.Values, fallbackRefresh string) (*Tokens, error) {
	values.Set("client_id", OAuthClientID)
	values.Set("client_secret", OAuthClientSecret)
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
		if json.Unmarshal(body, &providerError) == nil && providerError.Error == "invalid_grant" {
			return nil, fmt.Errorf("%w: OAuth token request failed with HTTP %d", ErrOAuthReauthenticationRequired, resp.StatusCode)
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

// FetchUserEmail returns the Google account email for display.
func FetchUserEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := httpretry.DoRequest(oauthHTTPClient, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini userinfo failed with HTTP %d", resp.StatusCode)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", err
	}
	return info.Email, nil
}

var codeAssistMetadata = map[string]string{
	"ideType":    "IDE_UNSPECIFIED",
	"platform":   "PLATFORM_UNSPECIFIED",
	"pluginType": "GEMINI",
}

// SetupCodeAssist resolves the Code Assist project for a Google account,
// onboarding to the default tier when the account has none yet. projectHint
// is required for workspace/standard tiers and ignored by the free tier.
func SetupCodeAssist(ctx context.Context, accessToken, projectHint string) (string, error) {
	load := map[string]any{"metadata": withProject(codeAssistMetadata, projectHint)}
	if projectHint != "" {
		load["cloudaicompanionProject"] = projectHint
	}
	var loaded struct {
		CurrentTier *struct {
			ID string `json:"id"`
		} `json:"currentTier"`
		AllowedTiers []struct {
			ID                                 string `json:"id"`
			IsDefault                          bool   `json:"isDefault"`
			UserDefinedCloudaicompanionProject bool   `json:"userDefinedCloudaicompanionProject"`
		} `json:"allowedTiers"`
		Project string `json:"cloudaicompanionProject"`
	}
	if err := codeAssistPost(ctx, accessToken, "loadCodeAssist", load, &loaded); err != nil {
		return "", err
	}
	if loaded.CurrentTier != nil {
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
		onboard["metadata"] = codeAssistMetadata
	} else {
		onboard["cloudaicompanionProject"] = projectHint
		onboard["metadata"] = withProject(codeAssistMetadata, projectHint)
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
		if err := codeAssistPost(ctx, accessToken, "onboardUser", onboard, &op); err != nil {
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

func codeAssistPost(ctx context.Context, accessToken, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodeAssistBaseURL+":"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
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
