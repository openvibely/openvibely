package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	"github.com/stretchr/testify/require"
)

func fakeGoogle(t *testing.T, loadCodeAssist http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
		case r.URL.Path == "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "google-user-1", "email": "dev@example.com"})
		case strings.HasSuffix(r.URL.Path, ":loadCodeAssist"):
			loadCodeAssist(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	oldToken, oldUser, oldAssist := geminiclient.OAuthTokenURL, geminiclient.UserInfoURL, geminiclient.CodeAssistBaseURL
	geminiclient.OAuthTokenURL, geminiclient.UserInfoURL, geminiclient.CodeAssistBaseURL = server.URL+"/token", server.URL+"/userinfo", server.URL+"/v1internal"
	t.Cleanup(func() {
		geminiclient.OAuthTokenURL, geminiclient.UserInfoURL, geminiclient.CodeAssistBaseURL = oldToken, oldUser, oldAssist
	})
}

func geminiSetupStatus(t *testing.T, h *Handler, key string) (map[string]any, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/models/oauth/setup/status", nil)
	req.Header.Set(modelOAuthSetupHeader, key)
	rec := httptest.NewRecorder()
	if err := h.ModelOAuthSetupStatus(echo.New().NewContext(req, rec)); err != nil {
		return nil, err
	}
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out, nil
}

func TestGeminiModelSetupRunsAccountSetupInBackground(t *testing.T) {
	t.Run("callback returns before account setup finishes", func(t *testing.T) {
		release := make(chan struct{})
		fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
			<-release
			_ = json.NewEncoder(w).Encode(map[string]any{"currentTier": map[string]string{"id": "standard-tier"}, "cloudaicompanionProject": "proj-1"})
		})
		h, _, _ := setupTestHandler(t)
		key := "gemini-background-setup"
		modelOAuthSetups.Lock()
		modelOAuthSetups.sessions[key] = &modelOAuthSetup{owner: h, expires: time.Now().Add(time.Minute), config: models.LLMConfig{Provider: models.ProviderGemini, AuthMethod: models.AuthMethodOAuth}}
		modelOAuthSetups.Unlock()
		t.Cleanup(func() { removeModelOAuthSetup(key) })

		done := make(chan error, 1)
		go func() {
			_, err := h.exchangeGeminiOAuthCodeAndSaveTokens(context.Background(), &oauthPendingFlow{SetupID: key, Provider: models.ProviderGemini}, "code")
			done <- err
		}()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("callback waited for account setup")
		}

		status, err := geminiSetupStatus(t, h, key)
		require.NoError(t, err)
		require.Equal(t, false, status["connected"])
		require.Contains(t, status["progress"], "Finishing Google account setup")

		close(release)
		require.Eventually(t, func() bool {
			status, err := geminiSetupStatus(t, h, key)
			return err == nil && status["connected"] == true && status["progress"] == ""
		}, 5*time.Second, 20*time.Millisecond)
		config, err := h.modelOAuthSetupConfig(key, true)
		require.NoError(t, err)
		require.Equal(t, "proj-1", config.OAuthAccountID)
	})

	t.Run("setup error is shown in the modal status", func(t *testing.T) {
		fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"not eligible for Code Assist"}}`))
		})
		h, _, _ := setupTestHandler(t)
		key := "gemini-background-setup-failure"
		modelOAuthSetups.Lock()
		modelOAuthSetups.sessions[key] = &modelOAuthSetup{owner: h, expires: time.Now().Add(time.Minute), config: models.LLMConfig{Provider: models.ProviderGemini, AuthMethod: models.AuthMethodOAuth}}
		modelOAuthSetups.Unlock()
		t.Cleanup(func() { removeModelOAuthSetup(key) })

		_, err := h.exchangeGeminiOAuthCodeAndSaveTokens(context.Background(), &oauthPendingFlow{SetupID: key, Provider: models.ProviderGemini}, "code")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			_, err := geminiSetupStatus(t, h, key)
			return err != nil && strings.Contains(err.Error(), "not eligible for Code Assist")
		}, 5*time.Second, 20*time.Millisecond)
	})
}

func TestGeminiModelSetupMergesSameGoogleUser(t *testing.T) {
	t.Setenv("OAUTH_REDIRECT_MODE", "localhost_manual")
	fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"currentTier": map[string]string{"id": "standard-tier"}, "cloudaicompanionProject": "proj-1"})
	})
	h, e, repo := setupTestHandler(t)
	addModel := func(name string) *models.LLMConfig {
		form := url.Values{"provider": {"gemini"}, "gemini_auth_type": {"oauth"}, "auth_method": {"oauth"}, "oauth_connection_id": {"new"}}
		send := func(path string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			return rec
		}
		rec := send("/models/oauth/setup")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var flow struct {
			Session string `json:"session_id"`
			URL     string `json:"authorization_url"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &flow))
		t.Cleanup(func() { removeModelOAuthSetup(flow.Session) })
		authURL, err := url.Parse(flow.URL)
		require.NoError(t, err)
		require.Equal(t, oauthCompletionSucceeded, h.completeOAuthFlow(authURL.Query().Get("state"), "code").Outcome)
		require.Eventually(t, func() bool {
			_, err := h.modelOAuthSetupConfig(flow.Session, true)
			return err == nil
		}, 5*time.Second, 20*time.Millisecond)
		form.Set("oauth_setup_session", flow.Session)
		form.Set("name", name)
		form.Set("model", "gemini-2.5-pro")
		rec = send("/models")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var saved map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &saved))
		cfg, err := repo.GetByID(context.Background(), saved["id"])
		require.NoError(t, err)
		return cfg
	}

	first := addModel("Gemini one")
	connection, err := repo.GetOAuthConnectionByID(context.Background(), first.OAuthConnectionID)
	require.NoError(t, err)
	require.True(t, connection.PrincipalVerified)
	require.Equal(t, geminiclient.PrincipalHash("proj-1", "google-user-1"), connection.PrincipalHash)
	require.Equal(t, "dev@example.com", connection.ProviderDisplayName)

	addModel("Gemini two")
	connections, err := repo.ListOAuthConnections(context.Background(), models.ProviderGemini)
	require.NoError(t, err)
	require.Len(t, connections, 1, "same Google user must share one saved account")
}
