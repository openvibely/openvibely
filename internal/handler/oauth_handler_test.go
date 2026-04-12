package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func stopOpenAIOAuthCallbackServerForTest(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	// Any callback request will shut down the temporary local listener.
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/auth/callback?error=access_denied&error_description=test", openAIOAuthPort))
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func TestHandler_OAuthInitiate(t *testing.T) {
	t.Setenv("APP_BASE_URL", "")

	t.Run("starts oauth flow for claude max", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthInitiate(c)
		require.NoError(t, err)

		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		location := rec.Header().Get("Location")
		require.Contains(t, location, "https://claude.ai/oauth/authorize")
		require.Contains(t, location, "response_type=code")
		require.Contains(t, location, "client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e")
		require.Contains(t, location, "code_challenge_method=S256")
		require.Contains(t, location, "state=")
		require.Contains(t, location, "code_challenge=")
		require.Contains(t, location, "redirect_uri=http%3A%2F%2Flocalhost%3A")
		require.Contains(t, location, "user%3Ainference")
		require.Contains(t, location, "code=true")
	})

	t.Run("starts oauth flow for openai", func(t *testing.T) {
		stopOpenAIOAuthCallbackServerForTest(t)
		defer stopOpenAIOAuthCallbackServerForTest(t)

		h, e, llmConfigRepo := setupTestHandler(t)
		model := &models.LLMConfig{
			Name:              "Test OpenAI OAuth",
			Provider:          models.ProviderOpenAI,
			AuthMethod:        models.AuthMethodOAuth,
			Model:             "gpt-4",
			Temperature:       0.7,
			MaxTokens:         4096,
			OAuthClientID:     "test-client-id",
			OAuthClientSecret: "test-client-secret",
			OAuthAuthorizeURL: "https://example.com/oauth/authorize",
			OAuthTokenURL:     "https://example.com/oauth/token",
			OAuthScopes:       "read write",
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthInitiate(c)
		require.NoError(t, err)

		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		location := rec.Header().Get("Location")
		require.Contains(t, location, "https://auth.openai.com/oauth/authorize")
		require.Contains(t, location, "response_type=code")
		require.Contains(t, location, "client_id="+openAIOAuthClientID)
		require.Contains(t, location, "code_challenge_method=S256")
		require.Contains(t, location, "state=")
		require.Contains(t, location, "code_challenge=")
		require.Contains(t, location, "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback")
		require.Contains(t, location, "%2Fauth%2Fcallback")
		require.Contains(t, location, "scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke")
		require.Contains(t, location, "originator="+openAIOriginator)
		require.NotContains(t, location, "code=true")
	})

	t.Run("returns error for non-oauth model", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)
		model := &models.LLMConfig{
			Name:            "Test Claude CLI",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodCLI,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(model.ID)

		err = h.OAuthInitiate(c)
		require.Error(t, err)
		httpErr := err.(*echo.HTTPError)
		require.Equal(t, http.StatusBadRequest, httpErr.Code)
		require.Equal(t, "OAuth is only available for OAuth-configured models", httpErr.Message)
	})

	t.Run("returns error for non-existent model", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/models/nonexistent/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("nonexistent")

		err := h.OAuthInitiate(c)
		require.Error(t, err)
		httpErr := err.(*echo.HTTPError)
		require.Equal(t, http.StatusNotFound, httpErr.Code)
		require.Equal(t, "model not found", httpErr.Message)
	})

	t.Run("uses defaults for openai without explicit oauth fields", func(t *testing.T) {
		stopOpenAIOAuthCallbackServerForTest(t)
		defer stopOpenAIOAuthCallbackServerForTest(t)

		h, e, llmConfigRepo := setupTestHandler(t)
		model := &models.LLMConfig{
			Name:        "Test OpenAI OAuth Incomplete",
			Provider:    models.ProviderOpenAI,
			AuthMethod:  models.AuthMethodOAuth,
			Model:       "gpt-4",
			Temperature: 0.7,
			MaxTokens:   4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(model.ID)

		err = h.OAuthInitiate(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		location := rec.Header().Get("Location")
		require.Contains(t, location, "https://auth.openai.com/oauth/authorize")
		require.Contains(t, location, "client_id="+openAIOAuthClientID)
		require.Contains(t, location, "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback")
		require.Contains(t, location, "scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke")
		require.Contains(t, location, "id_token_add_organizations=true")
		require.Contains(t, location, "codex_cli_simplified_flow=true")
		require.Contains(t, location, "originator="+openAIOriginator)
		require.Contains(t, location, "%2Fauth%2Fcallback")
	})

	t.Run("stores flow state correctly", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)
		oauthFlowsMu.Lock()
		oauthFlows = make(map[string]*oauthPendingFlow)
		oauthFlowsMu.Unlock()

		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(model.ID)

		err = h.OAuthInitiate(c)
		require.NoError(t, err)

		location := rec.Header().Get("Location")
		stateStart := strings.Index(location, "state=") + 6
		stateEnd := strings.Index(location[stateStart:], "&")
		if stateEnd == -1 {
			stateEnd = len(location) - stateStart
		}
		state := location[stateStart : stateStart+stateEnd]

		oauthFlowsMu.Lock()
		flow, exists := oauthFlows[state]
		oauthFlowsMu.Unlock()

		require.True(t, exists)
		require.NotNil(t, flow)
		require.Equal(t, model.ID, flow.ConfigID)
		require.Equal(t, state, flow.State)
		require.NotEmpty(t, flow.Verifier)
		require.Contains(t, flow.RedirectURI, "http://localhost:")
		require.Contains(t, flow.RedirectURI, "/callback")
		require.Equal(t, models.ProviderAnthropic, flow.Provider)
		require.Equal(t, oauthClientID, flow.ClientID)
		require.Equal(t, oauthTokenURL, flow.TokenURL)
		require.WithinDuration(t, time.Now(), flow.CreatedAt, 5*time.Second)
	})

		t.Run("uses built-in anthropic client when hosted client id is not provided", func(t *testing.T) {
			t.Setenv("APP_BASE_URL", "https://dubee.org")
			t.Setenv("ANTHROPIC_OAUTH_CLIENT_ID", "")
			h, e, llmConfigRepo := setupTestHandler(t)
			model := &models.LLMConfig{
				Name:            "Hosted Anthropic OAuth",
				Provider:        models.ProviderAnthropic,
				AuthMethod:      models.AuthMethodOAuth,
				Model:           "claude-3.5-sonnet",
				Temperature:     0.7,
				ReasoningEffort: "medium",
				MaxTokens:       4096,
			}
			err := llmConfigRepo.Create(context.Background(), model)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(model.ID)

			err = h.OAuthInitiate(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
			location := rec.Header().Get("Location")
			require.Contains(t, location, "client_id="+url.QueryEscape(oauthClientID))
			require.Contains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fcallback")
			require.NotContains(t, location, "localhost")
		})

		t.Run("forces localhost redirects in localhost_manual mode", func(t *testing.T) {
			t.Setenv("APP_BASE_URL", "https://dubee.org")
			t.Setenv("OAUTH_REDIRECT_MODE", "localhost_manual")
			h, e, llmConfigRepo := setupTestHandler(t)
			model := &models.LLMConfig{
				Name:       "Manual Mode Anthropic OAuth",
				Provider:   models.ProviderAnthropic,
				AuthMethod: models.AuthMethodOAuth,
				Model:      "claude-3.5-sonnet",
			}
			err := llmConfigRepo.Create(context.Background(), model)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(model.ID)

			err = h.OAuthInitiate(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
			location := rec.Header().Get("Location")
			require.Contains(t, location, "redirect_uri=http%3A%2F%2Flocalhost%3A53692%2Fcallback")
			require.NotContains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fcallback")
		})

	t.Run("uses explicit anthropic hosted oauth client in APP_BASE_URL mode", func(t *testing.T) {
		t.Setenv("APP_BASE_URL", "https://dubee.org")
		t.Setenv("ANTHROPIC_OAUTH_CLIENT_ID", "anthropic-hosted-client")
		h, e, llmConfigRepo := setupTestHandler(t)
		model := &models.LLMConfig{
			Name:            "Hosted Anthropic OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(model.ID)

		err = h.OAuthInitiate(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		location := rec.Header().Get("Location")
		require.Contains(t, location, "client_id=anthropic-hosted-client")
		require.Contains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fcallback")
		require.NotContains(t, location, "localhost")
	})

		t.Run("uses built-in openai client when hosted client id is not provided", func(t *testing.T) {
			t.Setenv("APP_BASE_URL", "https://dubee.org")
			t.Setenv("OPENAI_OAUTH_CLIENT_ID", "")
			h, e, llmConfigRepo := setupTestHandler(t)
			model := &models.LLMConfig{
				Name:        "Hosted OpenAI OAuth",
				Provider:    models.ProviderOpenAI,
				AuthMethod:  models.AuthMethodOAuth,
				Model:       "gpt-4",
				Temperature: 0.7,
				MaxTokens:   4096,
			}
			err := llmConfigRepo.Create(context.Background(), model)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(model.ID)

			err = h.OAuthInitiate(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
			location := rec.Header().Get("Location")
			require.Contains(t, location, "client_id="+url.QueryEscape(openAIOAuthClientID))
			require.Contains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fauth%2Fcallback")
			require.NotContains(t, location, "localhost")
		})

		t.Run("uses explicit openai hosted oauth client from model config in APP_BASE_URL mode", func(t *testing.T) {
			t.Setenv("APP_BASE_URL", "https://dubee.org")
			h, e, llmConfigRepo := setupTestHandler(t)
			model := &models.LLMConfig{
				Name:              "Hosted OpenAI OAuth",
				Provider:          models.ProviderOpenAI,
				AuthMethod:        models.AuthMethodOAuth,
				Model:             "gpt-4",
				Temperature:       0.7,
				MaxTokens:         4096,
				OAuthClientID:     "openai-hosted-client",
				OAuthClientSecret: "openai-hosted-secret",
				OAuthAuthorizeURL: "https://auth.openai.com/oauth/authorize",
				OAuthTokenURL:     "https://auth.openai.com/oauth/token",
				OAuthScopes:       "openid profile email offline_access api.connectors.read api.connectors.invoke",
			}
			err := llmConfigRepo.Create(context.Background(), model)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(model.ID)

			err = h.OAuthInitiate(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
			location := rec.Header().Get("Location")
			require.Contains(t, location, "client_id=openai-hosted-client")
			require.Contains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fauth%2Fcallback")
			require.NotContains(t, location, "localhost")
		})

		t.Run("uses localhost redirect in localhost_manual mode for openai", func(t *testing.T) {
			t.Setenv("APP_BASE_URL", "https://dubee.org")
			t.Setenv("OAUTH_REDIRECT_MODE", "localhost_manual")
			h, e, llmConfigRepo := setupTestHandler(t)
			model := &models.LLMConfig{
				Name:       "Manual Mode OpenAI OAuth",
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodOAuth,
				Model:      "gpt-4",
			}
			err := llmConfigRepo.Create(context.Background(), model)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, "/models/"+model.ID+"/oauth/initiate", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("id")
			c.SetParamValues(model.ID)

			err = h.OAuthInitiate(c)
			require.NoError(t, err)
			require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
			location := rec.Header().Get("Location")
			require.Contains(t, location, "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback")
			require.NotContains(t, location, "redirect_uri=https%3A%2F%2Fdubee.org%2Fauth%2Fcallback")
		})
}

func TestHandler_OAuthManualComplete(t *testing.T) {
	t.Run("completes oauth flow from pasted localhost callback url", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		model := &models.LLMConfig{
			Name:       "Manual Complete OpenAI",
			Provider:   models.ProviderOpenAI,
			AuthMethod: models.AuthMethodOAuth,
			Model:      "gpt-4",
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "manual-access-token",
				"refresh_token": "manual-refresh-token",
				"expires_in":    3600,
			})
		}))
		defer tokenServer.Close()

		testState := "manual-openai-state"
		oauthFlowsMu.Lock()
		oauthFlows[testState] = &oauthPendingFlow{
			ConfigID:    model.ID,
			Verifier:    "manual-verifier",
			State:       testState,
			RedirectURI: "http://localhost:1455/auth/callback",
			CreatedAt:   time.Now(),
			Provider:    models.ProviderOpenAI,
			ClientID:    openAIOAuthClientID,
			TokenURL:    tokenServer.URL,
		}
		oauthFlowsMu.Unlock()

		body := `{"callback_url":"http://localhost:1455/auth/callback?code=manual-code&state=` + testState + `"}`
		req := httptest.NewRequest(http.MethodPost, "/models/oauth/manual-complete", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"status":"connected"`)

		updatedModel, err := h.llmConfigRepo.GetByID(context.Background(), model.ID)
		require.NoError(t, err)
		require.Equal(t, "manual-access-token", updatedModel.OAuthAccessToken)
		require.Equal(t, "manual-refresh-token", updatedModel.OAuthRefreshToken)
	})

	t.Run("returns error for missing code or state", func(t *testing.T) {
		_, e, _ := setupTestHandler(t)
		body := `{"callback_url":"http://localhost:1455/auth/callback?code=only-code"}`
		req := httptest.NewRequest(http.MethodPost, "/models/oauth/manual-complete", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "code and state")
	})

	t.Run("returns error for expired state", func(t *testing.T) {
		_, e, _ := setupTestHandler(t)
		body := `{"callback_url":"http://localhost:1455/auth/callback?code=manual-code&state=missing"}`
		req := httptest.NewRequest(http.MethodPost, "/models/oauth/manual-complete", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "expired")
	})
}

func TestHandler_OAuthStatus(t *testing.T) {
	t.Run("returns not_configured for CLI model", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		// Create a CLI model
		model := &models.LLMConfig{
			Name:            "Test Claude CLI",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodCLI,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/status", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthStatus(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)

		var result map[string]string
		err = json.Unmarshal(rec.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Equal(t, "not_configured", result["status"])
	})

	t.Run("returns not_connected for OAuth model without tokens", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		// Create an OAuth model without tokens
		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/status", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthStatus(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)

		var result map[string]string
		err = json.Unmarshal(rec.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Equal(t, "not_connected", result["status"])
	})

	t.Run("returns connected for OAuth model with valid tokens", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		// Create an OAuth model
		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		// Update with valid tokens
		futureExpiry := time.Now().Add(1 * time.Hour).UnixMilli()
		err = h.llmConfigRepo.UpdateOAuthTokens(context.Background(), modelID, "access-token", "refresh-token", futureExpiry)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/status", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthStatus(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)

		var result map[string]string
		err = json.Unmarshal(rec.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Equal(t, "connected", result["status"])
	})

	t.Run("returns expired for OAuth model with expired tokens", func(t *testing.T) {
		h, e, llmConfigRepo := setupTestHandler(t)

		// Create an OAuth model
		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		// Update with expired tokens
		pastExpiry := time.Now().Add(-1 * time.Hour).UnixMilli()
		err = h.llmConfigRepo.UpdateOAuthTokens(context.Background(), modelID, "access-token", "refresh-token", pastExpiry)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/status", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(modelID)

		err = h.OAuthStatus(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)

		var result map[string]string
		err = json.Unmarshal(rec.Body.Bytes(), &result)
		require.NoError(t, err)
		require.Equal(t, "expired", result["status"])
	})

	t.Run("returns error for non-existent model", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)

		req := httptest.NewRequest(http.MethodGet, "/models/nonexistent/oauth/status", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues("nonexistent")

		err := h.OAuthStatus(c)
		require.Error(t, err)
		httpErr := err.(*echo.HTTPError)
		require.Equal(t, http.StatusNotFound, httpErr.Code)
		require.Equal(t, "model not found", httpErr.Message)
	})
}

func TestHandler_OAuthCallback(t *testing.T) {
	t.Run("returns session expired when callback has unknown state", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)

		req := httptest.NewRequest(http.MethodGet, "/models/oauth/callback?code=test&state=missing", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.OAuthCallback(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "Session Expired")
		require.Contains(t, rec.Body.String(), "Return to Models")
	})

	t.Run("handles hosted callback via /auth/callback route", func(t *testing.T) {
		t.Setenv("APP_BASE_URL", "https://dubee.org")
		h, e, llmConfigRepo := setupTestHandler(t)

		model := &models.LLMConfig{
			Name:        "Hosted Callback OpenAI",
			Provider:    models.ProviderOpenAI,
			AuthMethod:  models.AuthMethodOAuth,
			Model:       "gpt-4",
			Temperature: 0.7,
			MaxTokens:   4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)

		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "hosted-access-token",
				"refresh_token": "hosted-refresh-token",
				"expires_in":    3600,
			})
		}))
		defer tokenServer.Close()

		testState := "hosted-openai-state"
		oauthFlowsMu.Lock()
		oauthFlows[testState] = &oauthPendingFlow{
			ConfigID:    model.ID,
			Verifier:    "verifier-hosted",
			State:       testState,
			RedirectURI: "https://dubee.org/auth/callback",
			CreatedAt:   time.Now(),
			Provider:    models.ProviderOpenAI,
			ClientID:    openAIOAuthClientID,
			TokenURL:    tokenServer.URL,
		}
		oauthFlowsMu.Unlock()

		req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=hosted-code&state="+testState, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		require.Equal(t, http.StatusTemporaryRedirect, rec.Code)
		require.Contains(t, rec.Header().Get("Location"), "/models")

		updatedModel, err := h.llmConfigRepo.GetByID(context.Background(), model.ID)
		require.NoError(t, err)
		require.Equal(t, "hosted-access-token", updatedModel.OAuthAccessToken)
		require.Equal(t, "hosted-refresh-token", updatedModel.OAuthRefreshToken)
	})
}

func TestHandler_startOAuthCallbackServer(t *testing.T) {
	t.Run("handles successful callback", func(t *testing.T) {
		h, _, llmConfigRepo := setupTestHandler(t)

		// Create an OAuth model
		model := &models.LLMConfig{
			Name:            "Test Claude OAuth",
			Provider:        models.ProviderAnthropic,
			AuthMethod:      models.AuthMethodOAuth,
			Model:           "claude-3.5-sonnet",
			Temperature:     0.7,
			ReasoningEffort: "medium",
			MaxTokens:       4096,
		}
		err := llmConfigRepo.Create(context.Background(), model)
		require.NoError(t, err)
		modelID := model.ID

		// Set up a mock token server
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			require.Equal(t, "2024-11-21", r.Header.Get("anthropic-version"))

			// Return success response
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":  "test-access-token",
				"refresh_token": "test-refresh-token",
				"expires_in":    3600,
			})
		}))
		defer tokenServer.Close()

		// Create a pending flow
		testState := "test-state-123"
		oauthFlowsMu.Lock()
		oauthFlows[testState] = &oauthPendingFlow{
			ConfigID:     modelID,
			Verifier:     "test-verifier",
			State:        testState,
			RedirectURI:  "http://localhost:12345/callback",
			CreatedAt:    time.Now(),
			Provider:     models.ProviderAnthropic,
			ClientID:     oauthClientID,
			ClientSecret: "",
			TokenURL:     tokenServer.URL,
		}
		oauthFlowsMu.Unlock()

		// Start a listener
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port

		// Start the callback server with context
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done := make(chan bool)
		go func() {
			h.startOAuthCallbackServer(ctx, cancel, listener, "http://example.com/models", modelID)
			done <- true
		}()

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		// Make a callback request without following redirects.
		callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-code&state=%s", port, testState)
		httpClient := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := httpClient.Get(callbackURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should redirect to models page
		require.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode)
		require.Equal(t, "http://example.com/models", resp.Header.Get("Location"))

		// Verify tokens were saved
		updatedModel, err := h.llmConfigRepo.GetByID(context.Background(), modelID)
		require.NoError(t, err)
		require.Equal(t, "test-access-token", updatedModel.OAuthAccessToken)
		require.Equal(t, "test-refresh-token", updatedModel.OAuthRefreshToken)
		require.True(t, updatedModel.OAuthExpiresAt > time.Now().UnixMilli())

		// Wait for server to shut down
		select {
		case <-done:
			// Server shut down successfully
		case <-time.After(2 * time.Second):
			t.Fatal("Server did not shut down")
		}
	})

	t.Run("handles callback with error", func(t *testing.T) {
		h, _, _ := setupTestHandler(t)

		// Start a listener
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := listener.Addr().(*net.TCPAddr).Port

		// Start the callback server with context
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done := make(chan bool)
		go func() {
			h.startOAuthCallbackServer(ctx, cancel, listener, "http://example.com/models", "test-config")
			done <- true
		}()

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		// Make a callback request with error
		callbackURL := fmt.Sprintf("http://localhost:%d/callback?error=access_denied&error_description=User%%20denied%%20access", port)
		resp, err := http.Get(callbackURL)
		require.NoError(t, err)
		defer resp.Body.Close()

		// Should return error page
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		bodyStr := string(body[:n])
		require.Contains(t, bodyStr, "OAuth Failed")
		require.Contains(t, bodyStr, "User denied access")
		require.Contains(t, bodyStr, "Return to Models")

		// Wait for server to shut down
		select {
		case <-done:
			// Server shut down successfully
		case <-time.After(2 * time.Second):
			t.Fatal("Server did not shut down")
		}
	})

	t.Run("shuts down on context timeout", func(t *testing.T) {
		h, _, _ := setupTestHandler(t)

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		// Use a very short timeout to test auto-shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		done := make(chan bool)
		go func() {
			h.startOAuthCallbackServer(ctx, cancel, listener, "http://example.com/models", "timeout-config")
			done <- true
		}()

		// Server should shut down within ~200ms + some buffer
		select {
		case <-done:
			// Server shut down from timeout as expected
		case <-time.After(2 * time.Second):
			t.Fatal("Server did not shut down after context timeout")
		}
	})

	t.Run("shuts down on context cancel", func(t *testing.T) {
		h, _, _ := setupTestHandler(t)

		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan bool)
		go func() {
			h.startOAuthCallbackServer(ctx, cancel, listener, "http://example.com/models", "cancel-config")
			done <- true
		}()

		// Give server time to start
		time.Sleep(100 * time.Millisecond)

		// Cancel the context (simulates new flow for same config)
		cancel()

		select {
		case <-done:
			// Server shut down from cancellation as expected
		case <-time.After(2 * time.Second):
			t.Fatal("Server did not shut down after context cancel")
		}
	})
}

func TestOAuthServerTracking(t *testing.T) {
	t.Run("shutdownPreviousOAuthServer cancels tracked server", func(t *testing.T) {
		cancelled := false
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-ctx.Done()
			cancelled = true
		}()

		oauthServersMu.Lock()
		oauthServers["test-config-track"] = &oauthRunningServer{
			ConfigID: "test-config-track",
			Cancel:   cancel,
		}
		oauthServersMu.Unlock()

		shutdownPreviousOAuthServer("test-config-track")

		// Give goroutine time to observe cancellation
		time.Sleep(50 * time.Millisecond)
		require.True(t, cancelled, "Previous server should have been cancelled")

		oauthServersMu.Lock()
		_, exists := oauthServers["test-config-track"]
		oauthServersMu.Unlock()
		require.False(t, exists, "Tracked server should be removed")

		_ = ctx // keep linter happy
	})

	t.Run("shutdownPreviousOAuthServer no-op when no server exists", func(t *testing.T) {
		// Should not panic
		shutdownPreviousOAuthServer("nonexistent-config")
	})

	t.Run("untrackOAuthServer only removes matching serverID", func(t *testing.T) {
		_, cancel := context.WithCancel(context.Background())
		defer cancel()

		oauthServersMu.Lock()
		oauthServers["test-untrack"] = &oauthRunningServer{
			ConfigID: "test-untrack",
			ServerID: "server-2",
			Cancel:   cancel,
		}
		oauthServersMu.Unlock()

		// Try to untrack with mismatching serverID (old server)
		untrackOAuthServer("test-untrack", "server-1")

		oauthServersMu.Lock()
		_, exists := oauthServers["test-untrack"]
		oauthServersMu.Unlock()
		require.True(t, exists, "Should not remove server with non-matching serverID")

		// Now untrack with matching serverID
		untrackOAuthServer("test-untrack", "server-2")

		oauthServersMu.Lock()
		_, exists = oauthServers["test-untrack"]
		oauthServersMu.Unlock()
		require.False(t, exists, "Should remove server with matching serverID")
	})
}

func Test_generatePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	require.NoError(t, err)

	// Verifier should be base64url encoded random bytes
	require.NotEmpty(t, verifier)
	require.Regexp(t, "^[A-Za-z0-9_-]+$", verifier)

	// Challenge should be base64url encoded SHA256 of verifier
	require.NotEmpty(t, challenge)
	require.Regexp(t, "^[A-Za-z0-9_-]+$", challenge)

	// Verifier and challenge should be different
	require.NotEqual(t, verifier, challenge)
}

func Test_buildTokenExchangeBody(t *testing.T) {
	t.Run("claude max flow", func(t *testing.T) {
		flow := &oauthPendingFlow{
			ConfigID:     "test-config-id",
			Verifier:     "test-verifier",
			State:        "test-state",
			RedirectURI:  "http://localhost:12345/callback",
			Provider:     models.ProviderAnthropic,
			ClientID:     "test-client-id",
			ClientSecret: "", // No secret for Claude Max
		}

		body, contentType := buildTokenExchangeBody(flow, "test-code", "test-state")
		require.Equal(t, "application/json", contentType)

		var result map[string]interface{}
		err := json.Unmarshal(body, &result)
		require.NoError(t, err)

		require.Equal(t, "authorization_code", result["grant_type"])
		require.Equal(t, "test-client-id", result["client_id"])
		require.Equal(t, "test-code", result["code"])
		require.Equal(t, "http://localhost:12345/callback", result["redirect_uri"])
		require.Equal(t, "test-verifier", result["code_verifier"])
		require.Equal(t, "test-state", result["state"])
		_, hasSecret := result["client_secret"]
		require.False(t, hasSecret, "Claude Max should not include client_secret")
	})

	t.Run("openai flow with client secret", func(t *testing.T) {
		flow := &oauthPendingFlow{
			ConfigID:     "test-config-id",
			Verifier:     "test-verifier",
			State:        "test-state",
			RedirectURI:  "http://localhost:12345/callback",
			Provider:     models.ProviderOpenAI,
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
		}

		body, contentType := buildTokenExchangeBody(flow, "test-code", "test-state")
		require.Equal(t, "application/x-www-form-urlencoded", contentType)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		require.Equal(t, "authorization_code", values.Get("grant_type"))
		require.Equal(t, "test-client-id", values.Get("client_id"))
		require.Equal(t, "test-code", values.Get("code"))
		require.Equal(t, "http://localhost:12345/callback", values.Get("redirect_uri"))
		require.Equal(t, "test-verifier", values.Get("code_verifier"))
		require.Equal(t, "test-client-secret", values.Get("client_secret"))
		require.Empty(t, values.Get("state"))
	})
}

func Test_OAuthFlowCleanup(t *testing.T) {
	// Clear any existing flows
	oauthFlowsMu.Lock()
	oauthFlows = make(map[string]*oauthPendingFlow)

	// Add an old flow (> 10 minutes)
	oldFlow := &oauthPendingFlow{
		ConfigID:  "old-config",
		Verifier:  "old-verifier",
		State:     "old-state",
		CreatedAt: time.Now().Add(-15 * time.Minute),
	}
	oauthFlows["old-state"] = oldFlow

	// Add a recent flow
	recentFlow := &oauthPendingFlow{
		ConfigID:  "recent-config",
		Verifier:  "recent-verifier",
		State:     "recent-state",
		CreatedAt: time.Now().Add(-5 * time.Minute),
	}
	oauthFlows["recent-state"] = recentFlow
	oauthFlowsMu.Unlock()

	// Trigger cleanup by initiating a new flow
	h, e, llmConfigRepo := setupTestHandler(t)

	// Create an OAuth model
	model := &models.LLMConfig{
		Name:            "Test Cleanup",
		Provider:        models.ProviderAnthropic,
		AuthMethod:      models.AuthMethodOAuth,
		Model:           "claude-3.5-sonnet",
		Temperature:     0.7,
		ReasoningEffort: "medium",
		MaxTokens:       4096,
	}
	err := llmConfigRepo.Create(context.Background(), model)
	require.NoError(t, err)
	modelID := model.ID

	req := httptest.NewRequest(http.MethodGet, "/models/"+modelID+"/oauth/initiate", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(modelID)

	err = h.OAuthInitiate(c)
	require.NoError(t, err)

	// Check that old flow was cleaned up
	oauthFlowsMu.Lock()
	_, oldExists := oauthFlows["old-state"]
	_, recentExists := oauthFlows["recent-state"]
	oauthFlowsMu.Unlock()

	require.False(t, oldExists, "Old flow should have been cleaned up")
	require.True(t, recentExists, "Recent flow should still exist")
}

func Test_extractOpenAIAccountIDFromIDToken(t *testing.T) {
	t.Run("extracts account ID from auth claim", func(t *testing.T) {
		// Create a minimal JWT with account ID in auth claim
		payload := `{"https://api.openai.com/auth":{"chatgpt_account_id":"test-account-123"}}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		idToken := "header." + encoded + ".signature"

		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Equal(t, "test-account-123", accountID)
	})

	t.Run("extracts account ID from top-level claim", func(t *testing.T) {
		// Create a minimal JWT with account ID at top level
		payload := `{"chatgpt_account_id":"test-account-456"}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		idToken := "header." + encoded + ".signature"

		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Equal(t, "test-account-456", accountID)
	})

	t.Run("prefers auth claim over top-level", func(t *testing.T) {
		// Create a JWT with account ID in both locations
		payload := `{"https://api.openai.com/auth":{"chatgpt_account_id":"auth-account"},"chatgpt_account_id":"top-level-account"}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		idToken := "header." + encoded + ".signature"

		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Equal(t, "auth-account", accountID)
	})

	t.Run("returns empty for invalid JWT format", func(t *testing.T) {
		testCases := []string{
			"",                    // empty token
			"invalid",            // no parts
			"only.two",           // only 2 parts
			"four.parts.not.three", // 4 parts
			"header..signature",  // empty payload
		}

		for _, tc := range testCases {
			accountID := extractOpenAIAccountIDFromIDToken(tc)
			require.Empty(t, accountID, "Should return empty for token: %s", tc)
		}
	})

	t.Run("returns empty for invalid base64", func(t *testing.T) {
		idToken := "header.!!!invalid-base64!!!.signature"
		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Empty(t, accountID)
	})

	t.Run("returns empty for invalid JSON", func(t *testing.T) {
		payload := `{invalid json`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		idToken := "header." + encoded + ".signature"

		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Empty(t, accountID)
	})

	t.Run("returns empty when no account ID present", func(t *testing.T) {
		payload := `{"sub":"user123","exp":1234567890}`
		encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
		idToken := "header." + encoded + ".signature"

		accountID := extractOpenAIAccountIDFromIDToken(idToken)
		require.Empty(t, accountID)
	})
}

func Test_resolveOAuthExpiryAt(t *testing.T) {
	t.Run("calculates expiry from expires_in", func(t *testing.T) {
		expiresIn := int64(3600) // 1 hour
		before := time.Now().UnixMilli()
		expiryAt := resolveOAuthExpiryAt(expiresIn)
		after := time.Now().UnixMilli()

		// Should be approximately 1 hour from now
		expectedMin := before + 3600*1000
		expectedMax := after + 3600*1000
		require.GreaterOrEqual(t, expiryAt, expectedMin)
		require.LessOrEqual(t, expiryAt, expectedMax)
	})

	t.Run("uses fallback for zero expires_in", func(t *testing.T) {
		expiresIn := int64(0)
		before := time.Now().UnixMilli()
		expiryAt := resolveOAuthExpiryAt(expiresIn)
		after := time.Now().UnixMilli()

		// Should be approximately 24 hours from now
		expectedMin := before + 24*60*60*1000
		expectedMax := after + 24*60*60*1000
		require.GreaterOrEqual(t, expiryAt, expectedMin)
		require.LessOrEqual(t, expiryAt, expectedMax)
	})

	t.Run("uses fallback for negative expires_in", func(t *testing.T) {
		expiresIn := int64(-100)
		before := time.Now().UnixMilli()
		expiryAt := resolveOAuthExpiryAt(expiresIn)
		after := time.Now().UnixMilli()

		// Should be approximately 24 hours from now
		expectedMin := before + 24*60*60*1000
		expectedMax := after + 24*60*60*1000
		require.GreaterOrEqual(t, expiryAt, expectedMin)
		require.LessOrEqual(t, expiryAt, expectedMax)
	})
}

func Test_buildAbsoluteURL_UsesConfiguredAppBaseURL(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://dubee.org")
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	url := buildAbsoluteURL(c, "/models")
	require.Equal(t, "https://dubee.org/models", url)
}

func Test_firstNonEmpty(t *testing.T) {
	t.Run("returns first non-empty string", func(t *testing.T) {
		result := firstNonEmpty("", "", "third", "fourth")
		require.Equal(t, "third", result)
	})

	t.Run("returns first string if non-empty", func(t *testing.T) {
		result := firstNonEmpty("first", "second", "third")
		require.Equal(t, "first", result)
	})

	t.Run("returns empty if all empty", func(t *testing.T) {
		result := firstNonEmpty("", "", "")
		require.Equal(t, "", result)
	})

	t.Run("returns empty for no arguments", func(t *testing.T) {
		result := firstNonEmpty()
		require.Equal(t, "", result)
	})
}

func Test_parsePortOrDefault(t *testing.T) {
	t.Run("parses explicit port", func(t *testing.T) {
		u, err := url.Parse("https://dubee.org:4443/auth/callback")
		require.NoError(t, err)
		require.Equal(t, 4443, parsePortOrDefault(u, models.ProviderOpenAI))
	})

	t.Run("uses https default port", func(t *testing.T) {
		u, err := url.Parse("https://dubee.org/auth/callback")
		require.NoError(t, err)
		require.Equal(t, 443, parsePortOrDefault(u, models.ProviderAnthropic))
	})

	t.Run("uses http default port", func(t *testing.T) {
		u, err := url.Parse("http://dubee.org/auth/callback")
		require.NoError(t, err)
		require.Equal(t, 80, parsePortOrDefault(u, models.ProviderOpenAI))
	})
}

func Test_startOAuthCallbackListener(t *testing.T) {
	t.Run("starts on random port for non-OpenAI", func(t *testing.T) {
		listener, err := startOAuthCallbackListener(models.ProviderAnthropic)
		require.NoError(t, err)
		require.NotNil(t, listener)
		defer listener.Close()

		// Should be on a random port
		addr := listener.Addr().(*net.TCPAddr)
		require.Equal(t, "127.0.0.1", addr.IP.String())
		require.NotEqual(t, openAIOAuthPort, addr.Port)
	})

	t.Run("starts on fixed port for OpenAI", func(t *testing.T) {
		// Clean up any existing listener
		stopOpenAIOAuthCallbackServerForTest(t)

		listener, err := startOAuthCallbackListener(models.ProviderOpenAI)
		require.NoError(t, err)
		require.NotNil(t, listener)
		defer listener.Close()

		// Should be on the fixed OpenAI port
		addr := listener.Addr().(*net.TCPAddr)
		require.Equal(t, "127.0.0.1", addr.IP.String())
		require.Equal(t, openAIOAuthPort, addr.Port)
	})

	t.Run("cancels existing OpenAI listener and retries", func(t *testing.T) {
		// Start a conflicting server on the OpenAI port
		conflictingListener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", openAIOAuthPort))
		require.NoError(t, err)

		// Mock server that responds to /cancel
		go func() {
			mux := http.NewServeMux()
			mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				conflictingListener.Close()
			})
			http.Serve(conflictingListener, mux)
		}()

		// Give the conflicting server time to start
		time.Sleep(50 * time.Millisecond)

		// Try to start OAuth listener - should cancel the conflicting one and succeed
		listener, err := startOAuthCallbackListener(models.ProviderOpenAI)
		require.NoError(t, err)
		require.NotNil(t, listener)
		defer listener.Close()

		addr := listener.Addr().(*net.TCPAddr)
		require.Equal(t, openAIOAuthPort, addr.Port)
	})
}

func Test_cancelOpenAIOAuthCallbackServer(t *testing.T) {
	t.Skip("Skipping test - flaky when run with other OAuth tests due to port 1455 conflicts")
	t.Run("sends cancel request to localhost server", func(t *testing.T) {
		// Start a mock server on the OpenAI OAuth port
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", openAIOAuthPort))
		require.NoError(t, err)

		cancelReceived := false
		server := &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/cancel" {
					cancelReceived = true
					w.WriteHeader(http.StatusOK)
				}
			}),
		}

		go server.Serve(listener)
		defer server.Close()

		// Give server time to start
		time.Sleep(50 * time.Millisecond)

		// Send cancel request
		err = cancelOpenAIOAuthCallbackServer()
		require.NoError(t, err)
		require.True(t, cancelReceived)
	})

	t.Run("returns error when no server listening", func(t *testing.T) {
		// Make sure no server is listening
		stopOpenAIOAuthCallbackServerForTest(t)
		time.Sleep(50 * time.Millisecond)

		err := cancelOpenAIOAuthCallbackServer()
		require.Error(t, err)
	})
}
