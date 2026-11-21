package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_ = json.NewEncoder(w).Encode(map[string]string{"email": "dev@example.com"})
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
