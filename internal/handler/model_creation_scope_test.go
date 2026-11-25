package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

// A selected remote may be disconnected. It must not participate in creating
// an installation-wide model or starting this installation's OAuth session.
func TestNewModelIgnoresProjectDestination(t *testing.T) {
	t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
	t.Setenv("APP_BASE_URL", "https://app.example.com")
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	for index, project := range []string{"", remoteid.ID("0123456789abcdef", "project1"), models.SharedProjectID("github.com/team/project")} {
		t.Run(project, func(t *testing.T) {
			name := fmt.Sprintf("Local model %d", index)
			rec := httptest.NewRecorder()
			req := serversRequest(http.MethodPost, "/models?project_id="+url.QueryEscape(project), url.Values{"name": {name}, "provider": {"anthropic"}, "anthropic_auth_type": {"api_key"}, "model": {"claude-sonnet-4-5-20250929"}, "api_key": {"test-key"}})
			req.Header.Set("Accept", "application/json")
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var result map[string]string
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
			cfg, err := local.Stores.LLMConfigs.GetByID(context.Background(), result["id"])
			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.Equal(t, name, cfg.Name)
			// A disconnected selected project cannot block the installation's
			// model list or OAuth account choices either.
			list := httptest.NewRecorder()
			listRequest := httptest.NewRequest(http.MethodGet, "/models?project_id="+url.QueryEscape(project), nil)
			listRequest.Header.Set("HX-Request", "true")
			e.ServeHTTP(list, listRequest)
			require.Equal(t, http.StatusOK, list.Code, list.Body.String())
			require.Contains(t, list.Body.String(), name)

			for _, provider := range []string{"openai", "anthropic"} {
				response := httptest.NewRecorder()
				e.ServeHTTP(response, serversRequest(http.MethodPost, "/models/oauth/setup?project_id="+url.QueryEscape(project), url.Values{"provider": {provider}, "openai_auth_type": {"oauth"}, "anthropic_auth_type": {"oauth"}}))
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				var setup map[string]interface{}
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &setup))
				key, _ := setup["session_id"].(string)
				require.NotEmpty(t, key)
				modelOAuthSetups.Lock()
				session := modelOAuthSetups.sessions[key]
				modelOAuthSetups.Unlock()
				require.NotNil(t, session)
				require.Same(t, h, session.owner)
				removeModelOAuthSetup(key)
			}
		})
	}
}
