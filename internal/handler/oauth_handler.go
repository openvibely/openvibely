package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
)

const (
	oauthAuthorizeURL   = "https://claude.ai/oauth/authorize"
	oauthTokenURL       = "https://platform.claude.com/v1/oauth/token"
	oauthClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthScope          = "user:profile user:inference user:sessions:claude_code user:mcp_servers"
	anthropicAPIVersion = "2024-11-21"

	openAIOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openAIOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	// Matches Codex CLI's built-in OAuth client id.
	openAIOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// Scopes supported by the Codex CLI OAuth client.
	openAIOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	openAIOriginator = "codex_cli_rs"
	openAIOAuthPort  = 1455
)

// oauthPendingFlow stores the PKCE verifier and model config ID for an in-progress OAuth flow.
type oauthPendingFlow struct {
	ConfigID     string
	Verifier     string
	State        string
	RedirectURI  string
	CreatedAt    time.Time
	Provider     models.LLMProvider
	ClientID     string // From model config (OpenAI) or hardcoded (Claude Max)
	ClientSecret string // Only used for OpenAI OAuth
	TokenURL     string // From model config (OpenAI) or hardcoded (Claude Max)
}

// oauthFlows tracks pending OAuth authorization flows (keyed by state).
var (
	oauthFlows   = make(map[string]*oauthPendingFlow)
	oauthFlowsMu sync.Mutex
)

// oauthRunningServer tracks a running OAuth callback server so it can be
// cancelled when a new flow is initiated for the same config.
type oauthRunningServer struct {
	ConfigID string
	ServerID string // unique per server instance, used to safely untrack
	Cancel   context.CancelFunc
}

// oauthServers tracks running callback servers keyed by config ID.
// When a new OAuth flow is initiated for a config that already has a running
// server, the old server is shut down first.
var (
	oauthServers   = make(map[string]*oauthRunningServer)
	oauthServersMu sync.Mutex

	// oauthServerTimeout controls how long a callback server waits before
	// auto-shutting down. Exported as a var so tests can shorten it.
	oauthServerTimeout = 10 * time.Minute
)

// OAuthInitiate starts the OAuth flow for a model config.
// It starts a temporary HTTP listener on a random port for the callback,
// since Anthropic's OAuth only accepts redirect URIs matching http://localhost:<port>/callback.
// Supports both Claude Max and OpenAI OAuth flows.
// GET /models/:id/oauth/initiate
func (h *Handler) OAuthInitiate(c echo.Context) error {
	id := c.Param("id")
	log.Printf("[handler] OAuthInitiate id=%s", id)

	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		log.Printf("[handler] OAuthInitiate fetch error: %v", err)
		return err
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "model not found")
	}
	if !agent.IsOAuth() {
		return echo.NewHTTPError(http.StatusBadRequest, "OAuth is only available for OAuth-configured models")
	}

	// Resolve OAuth endpoints based on provider
	var authURL, clientID, clientSecret, tokenEndpoint, scope string
	if agent.Provider == models.ProviderOpenAI {
		// OpenAI subscription OAuth mirrors Codex CLI defaults.
		// Ignore per-model OAuth endpoint/client fields to avoid stale UI-configured values
		// breaking the built-in subscription flow.
		authURL = openAIOAuthAuthorizeURL
		clientID = openAIOAuthClientID
		clientSecret = ""
		tokenEndpoint = openAIOAuthTokenURL
		scope = openAIOAuthScope
	} else {
		// Claude Max uses hardcoded Anthropic endpoints
		authURL = oauthAuthorizeURL
		clientID = oauthClientID
		tokenEndpoint = oauthTokenURL
		scope = oauthScope
	}

	// Generate PKCE verifier and challenge
	verifier, challenge, err := generatePKCE()
	if err != nil {
		log.Printf("[handler] OAuthInitiate PKCE error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate PKCE")
	}

	// Generate state
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate state")
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	// Cancel any previous callback server for this config before starting a new one.
	shutdownPreviousOAuthServer(id)

	// Start local callback listener.
	// Anthropic supports dynamic localhost ports; OpenAI/Codex uses fixed localhost:1455.
	listener, err := startOAuthCallbackListener(agent.Provider)
	if err != nil {
		log.Printf("[handler] OAuthInitiate listener error: %v", err)
		if agent.Provider == models.ProviderOpenAI {
			return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf("failed to start callback listener on localhost:%d (it may already be in use)", openAIOAuthPort))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start callback listener")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectPath := "/callback"
	// OpenAI OAuth uses /auth/callback in Codex CLI; matching this avoids provider-side flow errors.
	if agent.Provider == models.ProviderOpenAI {
		redirectPath = "/auth/callback"
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, redirectPath)

	// Store flow details
	oauthFlowsMu.Lock()
	// Clean up old flows (> 10 minutes)
	for k, v := range oauthFlows {
		if time.Since(v.CreatedAt) > 10*time.Minute {
			delete(oauthFlows, k)
		}
	}
	oauthFlows[state] = &oauthPendingFlow{
		ConfigID:     id,
		Verifier:     verifier,
		State:        state,
		RedirectURI:  redirectURI,
		CreatedAt:    time.Now(),
		Provider:     agent.Provider,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenEndpoint,
	}
	oauthFlowsMu.Unlock()

	// Determine the main app URL to redirect to after callback
	scheme := "http"
	if c.Request().TLS != nil {
		scheme = "https"
	}
	mainAppHost := c.Request().Host
	modelsURL := fmt.Sprintf("%s://%s/models", scheme, mainAppHost)

	// Start temporary callback server in background with timeout context.
	// The server auto-shuts down after handling one request, on timeout, or
	// when a new OAuth flow is initiated for the same config.
	serverCtx, serverCancel := context.WithTimeout(context.Background(), oauthServerTimeout)
	serverID := state // reuse the unique state token as server ID
	oauthServersMu.Lock()
	oauthServers[id] = &oauthRunningServer{ConfigID: id, ServerID: serverID, Cancel: serverCancel}
	oauthServersMu.Unlock()
	go func() {
		h.startOAuthCallbackServer(serverCtx, serverCancel, listener, modelsURL, id)
		// Clean up tracking after the server exits.
		untrackOAuthServer(id, serverID)
	}()

	// Build the authorization URL
	authURLFull := authURL +
		"?response_type=code" +
		"&client_id=" + url.QueryEscape(clientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&code_challenge=" + url.QueryEscape(challenge) +
		"&code_challenge_method=S256" +
		"&state=" + url.QueryEscape(state)
	if scope != "" {
		authURLFull += "&scope=" + url.QueryEscape(scope)
	}
	// Anthropic-specific parameter (for Anthropic OAuth, not OpenAI)
	if agent.Provider == models.ProviderAnthropic {
		authURLFull += "&code=true"
	} else if agent.Provider == models.ProviderOpenAI {
		authURLFull += "&id_token_add_organizations=true"
		authURLFull += "&codex_cli_simplified_flow=true"
		authURLFull += "&originator=" + url.QueryEscape(openAIOriginator)
	}

	log.Printf("[handler] OAuthInitiate redirecting to OAuth for config=%s provider=%s port=%d", id, agent.Provider, port)
	return c.Redirect(http.StatusTemporaryRedirect, authURLFull)
}

// startOAuthCallbackServer runs a temporary HTTP server that handles the OAuth callback
// on a localhost port. It shuts down after handling one request, when the context expires
// (timeout or cancellation from a new flow for the same config), or on /cancel.
func (h *Handler) startOAuthCallbackServer(ctx context.Context, cancel context.CancelFunc, listener net.Listener, modelsURL string, configID string) {
	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}

	// shutdownOnce ensures we only trigger shutdown once regardless of which
	// path fires first (callback, cancel endpoint, or context timeout).
	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		shutdownOnce.Do(func() {
			log.Printf("[handler] OAuth callback server shutting down config=%s reason=%s", configID, reason)
			cancel()
			srv.Shutdown(context.Background())
		})
	}

	handleCallback := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			go shutdown("callback_handled")
		}()

		// Check for OAuth errors
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			errDesc := r.URL.Query().Get("error_description")
			if errDesc == "" {
				errDesc = oauthErr
			}
			log.Printf("[handler] OAuthCallback error: %s - %s", oauthErr, errDesc)
			fmt.Fprintf(w, `<html><body>
				<h2>OAuth Failed</h2>
				<p>%s</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, errDesc, modelsURL)
			return
		}

		state := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")

		if state == "" || code == "" {
			fmt.Fprintf(w, `<html><body>
				<h2>Invalid Callback</h2>
				<p>Missing state or code parameter.</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, modelsURL)
			return
		}

		// Look up the pending flow
		oauthFlowsMu.Lock()
		flow, ok := oauthFlows[state]
		if ok {
			delete(oauthFlows, state)
		}
		oauthFlowsMu.Unlock()

		if !ok {
			log.Printf("[handler] OAuthCallback state not found or expired")
			fmt.Fprintf(w, `<html><body>
				<h2>Session Expired</h2>
				<p>The OAuth session has expired. Please try again.</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, modelsURL)
			return
		}

		// Exchange authorization code for tokens
		tokenBody, contentType := buildTokenExchangeBody(flow, code, state)
		tokenReq, _ := http.NewRequest("POST", flow.TokenURL, bytes.NewReader(tokenBody))
		tokenReq.Header.Set("Content-Type", contentType)
		if flow.Provider == models.ProviderAnthropic {
			tokenReq.Header.Set("anthropic-version", anthropicAPIVersion)
		}

		tokenResp, err := http.DefaultClient.Do(tokenReq)
		if err != nil {
			log.Printf("[handler] OAuthCallback token exchange request error: %v", err)
			fmt.Fprintf(w, `<html><body>
				<h2>Token Exchange Failed</h2>
				<p>Could not connect to OAuth provider: %s</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, err.Error(), modelsURL)
			return
		}
		defer tokenResp.Body.Close()

		if tokenResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(tokenResp.Body)
			log.Printf("[handler] OAuthCallback token exchange failed %d: %s", tokenResp.StatusCode, string(body))
			fmt.Fprintf(w, `<html><body>
				<h2>Token Exchange Failed</h2>
				<p>OAuth provider returned status %d</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, tokenResp.StatusCode, modelsURL)
			return
		}

		var tokenResult struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.NewDecoder(tokenResp.Body).Decode(&tokenResult); err != nil {
			log.Printf("[handler] OAuthCallback decode token error: %v", err)
			fmt.Fprintf(w, `<html><body>
				<h2>Token Exchange Failed</h2>
				<p>Could not parse token response.</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, modelsURL)
			return
		}

		expiresAt := resolveOAuthExpiryAt(tokenResult.ExpiresIn)
		openAIAccountID := ""
		if flow.Provider == models.ProviderOpenAI {
			openAIAccountID = extractOpenAIAccountIDFromIDToken(tokenResult.IDToken)
		}

		// Update the model config with OAuth tokens
		bgCtx := context.Background()
		var saveErr error
		if openAIAccountID != "" {
			saveErr = h.llmConfigRepo.UpdateOAuthTokens(bgCtx, flow.ConfigID, tokenResult.AccessToken, tokenResult.RefreshToken, expiresAt, openAIAccountID)
		} else {
			saveErr = h.llmConfigRepo.UpdateOAuthTokens(bgCtx, flow.ConfigID, tokenResult.AccessToken, tokenResult.RefreshToken, expiresAt)
		}
		if saveErr != nil {
			log.Printf("[handler] OAuthCallback save tokens error: %v", saveErr)
			fmt.Fprintf(w, `<html><body>
				<h2>Save Failed</h2>
				<p>Could not save OAuth tokens.</p>
				<p><a href="%s">Return to Models</a></p>
			</body></html>`, modelsURL)
			return
		}

		log.Printf("[handler] OAuthCallback success config=%s expires=%s", flow.ConfigID, time.UnixMilli(expiresAt).Format(time.RFC3339))

		// Redirect browser back to models page
		http.Redirect(w, r, modelsURL, http.StatusTemporaryRedirect)
	}

	mux.HandleFunc("/callback", handleCallback)
	// OpenAI/Codex flow uses this callback path.
	mux.HandleFunc("/auth/callback", handleCallback)
	// Allow replacing an existing pending flow on localhost:1455 (Codex-style reauth).
	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "cancelled")
		go shutdown("cancel_endpoint")
	})

	// Wait for context expiry (timeout or cancellation) and shut down the server.
	go func() {
		<-ctx.Done()
		shutdown("context_done")
	}()

	log.Printf("[handler] OAuth callback server started config=%s addr=%s timeout=%s", configID, listener.Addr().String(), oauthServerTimeout)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Printf("[handler] OAuth callback server error: %v", err)
	}
	log.Printf("[handler] OAuth callback server stopped config=%s", configID)
}

// OAuthCallback is kept as a no-op fallback. The actual callback is handled by the
// temporary localhost server started in OAuthInitiate.
// GET /models/oauth/callback
func (h *Handler) OAuthCallback(c echo.Context) error {
	return c.HTML(http.StatusOK, `<html><body>
		<h2>OAuth Callback</h2>
		<p>This endpoint is handled by the temporary callback server. If you see this, the flow may have timed out.</p>
		<p><a href="/models">Return to Models</a></p>
	</body></html>`)
}

// OAuthStatus returns the OAuth status for a model config.
// GET /models/:id/oauth/status
func (h *Handler) OAuthStatus(c echo.Context) error {
	id := c.Param("id")
	agent, err := h.llmConfigRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		return err
	}
	if agent == nil {
		return echo.NewHTTPError(http.StatusNotFound, "model not found")
	}

	status := "not_configured"
	if agent.IsOAuth() {
		if agent.HasValidOAuthToken() {
			status = "connected"
		} else if agent.OAuthAccessToken != "" {
			status = "expired"
		} else {
			status = "not_connected"
		}
	}

	return c.JSON(http.StatusOK, map[string]string{
		"status": status,
	})
}

func extractOpenAIAccountIDFromIDToken(idToken string) string {
	parts := bytes.Split([]byte(idToken), []byte("."))
	if len(parts) != 3 || len(parts[1]) == 0 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return ""
	}

	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	if claims.Auth.ChatGPTAccountID != "" {
		return claims.Auth.ChatGPTAccountID
	}
	return claims.ChatGPTAccountID
}

// buildTokenExchangeBody constructs the token exchange request body and content type.
// Anthropic expects JSON; OpenAI (Codex-compatible) expects application/x-www-form-urlencoded.
func buildTokenExchangeBody(flow *oauthPendingFlow, code, state string) ([]byte, string) {
	if flow.Provider == models.ProviderOpenAI {
		values := url.Values{}
		values.Set("grant_type", "authorization_code")
		values.Set("code", code)
		values.Set("redirect_uri", flow.RedirectURI)
		values.Set("client_id", flow.ClientID)
		values.Set("code_verifier", flow.Verifier)
		// Optional for backwards compatibility with custom OpenAI OAuth apps.
		if flow.ClientSecret != "" {
			values.Set("client_secret", flow.ClientSecret)
		}
		return []byte(values.Encode()), "application/x-www-form-urlencoded"
	}

	body := map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     flow.ClientID,
		"code":          code,
		"redirect_uri":  flow.RedirectURI,
		"code_verifier": flow.Verifier,
		"state":         state,
	}
	if flow.ClientSecret != "" {
		body["client_secret"] = flow.ClientSecret
	}
	data, _ := json.Marshal(body)
	return data, "application/json"
}

func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolveOAuthExpiryAt(expiresInSeconds int64) int64 {
	if expiresInSeconds > 0 {
		return time.Now().UnixMilli() + expiresInSeconds*1000
	}
	// Some providers may omit expires_in; keep token usable for connection status.
	const fallbackLifetime = 24 * time.Hour
	return time.Now().Add(fallbackLifetime).UnixMilli()
}

func startOAuthCallbackListener(provider models.LLMProvider) (net.Listener, error) {
	if provider != models.ProviderOpenAI {
		return net.Listen("tcp", "127.0.0.1:0")
	}

	addr := fmt.Sprintf("127.0.0.1:%d", openAIOAuthPort)
	listener, err := net.Listen("tcp", addr)
	if err == nil {
		return listener, nil
	}
	// OpenAI/Codex flow uses a fixed port. If it is occupied, try to cancel
	// the previous local login server and retry.
	if !errors.Is(err, syscall.EADDRINUSE) {
		return nil, err
	}
	_ = cancelOpenAIOAuthCallbackServer()
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(200 * time.Millisecond)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			return listener, nil
		}
	}
	return nil, err
}

// shutdownPreviousOAuthServer cancels any running callback server for the given
// config ID. This prevents orphaned servers when the user re-initiates OAuth.
func shutdownPreviousOAuthServer(configID string) {
	oauthServersMu.Lock()
	prev, ok := oauthServers[configID]
	if ok {
		delete(oauthServers, configID)
	}
	oauthServersMu.Unlock()
	if ok {
		log.Printf("[handler] Cancelling previous OAuth callback server for config=%s", configID)
		prev.Cancel()
	}
}

// untrackOAuthServer removes the server from tracking if the serverID
// still matches (i.e., it hasn't been replaced by a newer flow).
func untrackOAuthServer(configID, serverID string) {
	oauthServersMu.Lock()
	defer oauthServersMu.Unlock()
	if entry, ok := oauthServers[configID]; ok {
		// Only remove if the serverID matches — a newer flow may have replaced us.
		if entry.ServerID == serverID {
			delete(oauthServers, configID)
		}
	}
}

func cancelOpenAIOAuthCallbackServer() error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/cancel", openAIOAuthPort))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
