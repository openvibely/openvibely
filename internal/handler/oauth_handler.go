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
	"os"
	"strings"
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

	oauthRedirectModeAuto            = "auto"
	oauthRedirectModeHosted          = "hosted"
	oauthRedirectModeLocalhostManual = "localhost_manual"
	anthropicManualOAuthPort         = 53692
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

	redirectMode := resolveOAuthRedirectMode()
	publicBaseURL := resolveConfiguredAppBaseURL()
	if redirectMode == oauthRedirectModeHosted && publicBaseURL == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "OAUTH_REDIRECT_MODE=hosted requires APP_BASE_URL")
	}
	usePublicCallback := shouldUsePublicOAuthCallback(redirectMode, publicBaseURL)
	useLocalManualMode := isLocalManualOAuthMode(redirectMode)

	// Resolve OAuth endpoints and credentials based on provider + callback mode.
	var authURL, clientID, clientSecret, tokenEndpoint, scope string
	if agent.Provider == models.ProviderOpenAI {
		if usePublicCallback {
			// Hosted mode prefers explicit hosted OAuth settings, but falls back to
			// built-in Codex CLI defaults to avoid hard-failing configured deployments.
			authURL = firstNonEmpty(strings.TrimSpace(agent.OAuthAuthorizeURL), strings.TrimSpace(os.Getenv("OPENAI_OAUTH_AUTHORIZE_URL")), openAIOAuthAuthorizeURL)
			tokenEndpoint = firstNonEmpty(strings.TrimSpace(agent.OAuthTokenURL), strings.TrimSpace(os.Getenv("OPENAI_OAUTH_TOKEN_URL")), openAIOAuthTokenURL)
			clientID = firstNonEmpty(strings.TrimSpace(agent.OAuthClientID), strings.TrimSpace(os.Getenv("OPENAI_OAUTH_CLIENT_ID")), openAIOAuthClientID)
			clientSecret = firstNonEmpty(strings.TrimSpace(agent.OAuthClientSecret), strings.TrimSpace(os.Getenv("OPENAI_OAUTH_CLIENT_SECRET")))
			scope = firstNonEmpty(strings.TrimSpace(agent.OAuthScopes), strings.TrimSpace(os.Getenv("OPENAI_OAUTH_SCOPES")), openAIOAuthScope)
		} else {
			// Local/localhost-manual mode mirrors Codex CLI defaults (localhost:1455/auth/callback).
			authURL = openAIOAuthAuthorizeURL
			clientID = openAIOAuthClientID
			clientSecret = ""
			tokenEndpoint = openAIOAuthTokenURL
			scope = openAIOAuthScope
		}
	} else {
		if usePublicCallback {
			// Hosted mode prefers explicit Anthropic OAuth settings, but falls back to
			// built-in defaults so APP_BASE_URL alone does not hard-break OAuth start.
			authURL = firstNonEmpty(strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_AUTHORIZE_URL")), oauthAuthorizeURL)
			tokenEndpoint = firstNonEmpty(strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_TOKEN_URL")), oauthTokenURL)
			clientID = firstNonEmpty(strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_CLIENT_ID")), oauthClientID)
			clientSecret = firstNonEmpty(strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_CLIENT_SECRET")))
			scope = firstNonEmpty(strings.TrimSpace(os.Getenv("ANTHROPIC_OAUTH_SCOPES")), oauthScope)
		} else {
			// Local/localhost-manual mode uses built-in Claude Max client defaults.
			authURL = oauthAuthorizeURL
			clientID = oauthClientID
			clientSecret = ""
			tokenEndpoint = oauthTokenURL
			scope = oauthScope
		}
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

	modelsURL := buildAbsoluteURL(c, "/models")
	// Redirect paths must match what the OAuth providers accept for their
	// registered client IDs:
	//   Anthropic: /callback   (matches http://localhost:<port>/callback pattern)
	//   OpenAI:    /auth/callback (matches Codex CLI's http://localhost:1455/auth/callback)
	redirectPath := "/callback"
	if agent.Provider == models.ProviderOpenAI {
		redirectPath = "/auth/callback"
	}

	redirectURI := ""
	if usePublicCallback {
		redirectURI = strings.TrimRight(publicBaseURL, "/") + redirectPath
	} else {
		localPath := "/callback"
		if agent.Provider == models.ProviderOpenAI {
			localPath = "/auth/callback"
		}

		if useLocalManualMode {
			port := anthropicManualOAuthPort
			if agent.Provider == models.ProviderOpenAI {
				port = openAIOAuthPort
			}
			redirectURI = fmt.Sprintf("http://localhost:%d%s", port, localPath)
		} else {
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
			redirectURI = fmt.Sprintf("http://localhost:%d%s", port, localPath)

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
		}
	}

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

	portForLog := 0
	if u, parseErr := url.Parse(redirectURI); parseErr == nil {
		portForLog = parsePortOrDefault(u, agent.Provider)
	}

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

	log.Printf("[handler] OAuthInitiate redirecting to OAuth for config=%s provider=%s callback=%s port=%d public_callback=%t", id, agent.Provider, redirectURI, portForLog, usePublicCallback)
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
		h.handleOAuthCallbackResponse(w, r, modelsURL, func() {
			shutdown("callback_handled")
		})
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

// OAuthCallback handles OAuth provider callbacks for both hosted and localhost flows.
// GET /models/oauth/callback
func (h *Handler) OAuthCallback(c echo.Context) error {
	modelsURL := buildAbsoluteURL(c, "/models")
	h.handleOAuthCallbackResponse(c.Response().Writer, c.Request(), modelsURL, nil)
	return nil
}

// OAuthManualComplete finishes an OAuth flow by accepting a pasted localhost callback URL
// and replaying code/state through the same server-side completion logic.
// POST /models/oauth/manual-complete
func (h *Handler) OAuthManualComplete(c echo.Context) error {
	var req struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid JSON payload"})
	}
	callbackURL := strings.TrimSpace(req.CallbackURL)
	if callbackURL == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "callback_url is required"})
	}

	u, err := url.Parse(callbackURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "callback_url must be an absolute URL"})
	}

	values := u.Query()
	state := values.Get("state")
	code := values.Get("code")
	if state == "" || code == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "callback_url must include code and state"})
	}

	if oauthErr := values.Get("error"); oauthErr != "" {
		errDesc := values.Get("error_description")
		if errDesc == "" {
			errDesc = oauthErr
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": errDesc})
	}

	oauthFlowsMu.Lock()
	flow, ok := oauthFlows[state]
	if ok {
		delete(oauthFlows, state)
	}
	oauthFlowsMu.Unlock()
	if !ok {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "oauth session expired or invalid state"})
	}

	expiresAt, err := h.exchangeOAuthCodeAndSaveTokens(flow, code, state)
	if err != nil {
		log.Printf("[handler] OAuthManualComplete exchange/save error: %v", err)
		return c.JSON(http.StatusBadGateway, map[string]string{"error": "token exchange failed"})
	}
	log.Printf("[handler] OAuthManualComplete success config=%s provider=%s expires=%s", flow.ConfigID, flow.Provider, time.UnixMilli(expiresAt).Format(time.RFC3339))
	return c.JSON(http.StatusOK, map[string]string{"status": "connected"})
}

func (h *Handler) handleOAuthCallbackResponse(w http.ResponseWriter, r *http.Request, modelsURL string, onDone func()) {
	if onDone != nil {
		defer func() {
			go onDone()
		}()
	}

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

	expiresAt, err := h.exchangeOAuthCodeAndSaveTokens(flow, code, state)
	if err != nil {
		log.Printf("[handler] OAuthCallback exchange/save error: %v", err)
		fmt.Fprintf(w, `<html><body>
			<h2>Token Exchange Failed</h2>
			<p>Could not complete OAuth exchange.</p>
			<p><a href="%s">Return to Models</a></p>
		</body></html>`, modelsURL)
		return
	}

	log.Printf("[handler] OAuthCallback success config=%s provider=%s expires=%s", flow.ConfigID, flow.Provider, time.UnixMilli(expiresAt).Format(time.RFC3339))
	http.Redirect(w, r, modelsURL, http.StatusTemporaryRedirect)
}

func (h *Handler) exchangeOAuthCodeAndSaveTokens(flow *oauthPendingFlow, code, state string) (int64, error) {
	tokenBody, contentType := buildTokenExchangeBody(flow, code, state)
	tokenReq, err := http.NewRequest("POST", flow.TokenURL, bytes.NewReader(tokenBody))
	if err != nil {
		return 0, err
	}
	tokenReq.Header.Set("Content-Type", contentType)
	if flow.Provider == models.ProviderAnthropic {
		tokenReq.Header.Set("anthropic-version", anthropicAPIVersion)
	}

	tokenResp, err := http.DefaultClient.Do(tokenReq)
	if err != nil {
		return 0, err
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tokenResp.Body)
		return 0, fmt.Errorf("provider returned status %d: %s", tokenResp.StatusCode, string(body))
	}

	var tokenResult struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenResult); err != nil {
		return 0, err
	}

	expiresAt := resolveOAuthExpiryAt(tokenResult.ExpiresIn)
	openAIAccountID := ""
	if flow.Provider == models.ProviderOpenAI {
		openAIAccountID = extractOpenAIAccountIDFromIDToken(tokenResult.IDToken)
	}

	bgCtx := context.Background()
	if openAIAccountID != "" {
		if err := h.llmConfigRepo.UpdateOAuthTokens(bgCtx, flow.ConfigID, tokenResult.AccessToken, tokenResult.RefreshToken, expiresAt, openAIAccountID); err != nil {
			return 0, err
		}
	} else {
		if err := h.llmConfigRepo.UpdateOAuthTokens(bgCtx, flow.ConfigID, tokenResult.AccessToken, tokenResult.RefreshToken, expiresAt); err != nil {
			return 0, err
		}
	}

	return expiresAt, nil
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

func resolveOAuthRedirectMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("OAUTH_REDIRECT_MODE")))
	switch mode {
	case "", oauthRedirectModeAuto:
		return oauthRedirectModeAuto
	case oauthRedirectModeHosted:
		return oauthRedirectModeHosted
	case oauthRedirectModeLocalhostManual:
		return oauthRedirectModeLocalhostManual
	default:
		log.Printf("[handler] invalid OAUTH_REDIRECT_MODE=%q; defaulting to %s", mode, oauthRedirectModeAuto)
		return oauthRedirectModeAuto
	}
}

func shouldUsePublicOAuthCallback(redirectMode, publicBaseURL string) bool {
	if redirectMode == oauthRedirectModeLocalhostManual {
		return false
	}
	return publicBaseURL != ""
}

func isLocalManualOAuthMode(redirectMode string) bool {
	return redirectMode == oauthRedirectModeLocalhostManual
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

func parsePortOrDefault(u *url.URL, provider models.LLMProvider) int {
	if u == nil {
		if provider == models.ProviderOpenAI {
			return openAIOAuthPort
		}
		return 80
	}
	if p := u.Port(); p != "" {
		if parsed, err := net.LookupPort("tcp", p); err == nil {
			return parsed
		}
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}
