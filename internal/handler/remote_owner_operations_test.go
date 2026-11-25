package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteOwnerOperationsPreserveLocalState(t *testing.T) {
	ctx := context.Background()
	caller := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.SetAgentRepo(wrapped.Agents)
	srv, _, owner := fakeRemoteOV(t, "secret")
	owner.SetAgentRepo(owner.remoteExecutor.Stores.Agents)
	owner.lifecycleRepo = owner.remoteExecutor.Stores.Lifecycle
	owner.slackAuthRepo = owner.remoteExecutor.Stores.SlackAuth
	owner.telegramAuthRepo = owner.remoteExecutor.Stores.TelegramAuth
	owner.discordAuthRepo = owner.remoteExecutor.Stores.DiscordAuth
	owner.emailAuthRepo = owner.remoteExecutor.Stores.EmailAuth
	project := &models.Project{Name: "Project 1", RepoPath: t.TempDir()}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(ctx, project))
	localRoot, remoteRoot := t.TempDir(), t.TempDir()
	h.SetAgentSkillRoot(localRoot)
	owner.SetAgentSkillRoot(remoteRoot)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	projectID := remoteid.ID(sid, project.ID)

	t.Run("model create edit secret preservation default and deletion", func(t *testing.T) {
		values := url.Values{"name": {"Remote model"}, "provider": {"anthropic"}, "anthropic_auth_type": {"api_key"}, "model": {"claude-sonnet-4-5-20250929"}, "api_key": {"remote-test-key"}}
		result, err := actModelSave(h, caller, modelSaveArgs{ProjectID: projectID, Values: values})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(result.ID, "r-"))
		values.Set("name", "Updated remote model")
		values.Set("api_key", "")
		_, err = actModelSave(h, caller, modelSaveArgs{ProjectID: projectID, ID: result.ID, Values: values})
		require.NoError(t, err)
		stored, err := owner.llmConfigRepo.GetByID(ctx, remoteid.Strip(sid, result.ID))
		require.NoError(t, err)
		require.Equal(t, "remote-test-key", stored.APIKey)
		_, err = actModelAccount(h, caller, modelAccountArgs{ID: result.ID, Operation: "default"})
		require.NoError(t, err)
		_, err = actModelAccount(h, caller, modelAccountArgs{ID: result.ID, Operation: "delete"})
		require.NoError(t, err)
		stored, err = owner.llmConfigRepo.GetByID(ctx, remoteid.Strip(sid, result.ID))
		require.NoError(t, err)
		require.Nil(t, stored)
	})

	t.Run("agent save and skill mutations preserve matching local folder", func(t *testing.T) {
		localFile := filepath.Join(localRoot, "agents", "reviewer", "skills", "backup", "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(localFile), 0755))
		require.NoError(t, os.WriteFile(localFile, []byte("Local skill must survive"), 0600))
		values := url.Values{"name": {"Reviewer"}, "key": {"reviewer"}, "scope": {"project"}, "enabled": {"true"}, "system_prompt": {"Review"}}
		saved, err := actAgentSave(h, caller, agentSaveArgs{ProjectID: projectID, Values: values, Model: "inherit"})
		require.NoError(t, err)
		id := saved.ID
		require.True(t, strings.HasPrefix(id, "r-"))
		values.Set("description", "Edited on remote")
		_, err = actAgentSave(h, caller, agentSaveArgs{ProjectID: projectID, ID: id, Values: values, Model: "inherit"})
		require.NoError(t, err)
		_, err = actSaveAgentHooks(h, caller, agentHooksArgs{AgentID: id, Hooks: []hookSavePayload{}})
		require.NoError(t, err)
		args := agentSkillOperationArgs{AgentID: id, ProjectID: projectID, Operation: "create", Handle: "backup", Scope: "project", Save: agentSkillSaveRequest{Handle: "backup", Name: "Backup", Body: "Remote skill"}}
		_, err = actAgentSkillOperation(h, caller, args)
		require.NoError(t, err)
		args.Operation = "update"
		args.Save.Body = "Updated remote skill"
		_, err = actAgentSkillOperation(h, caller, args)
		require.NoError(t, err)

		payload, err := json.Marshal(agentSkillSaveRequest{Handle: "backup", Name: "Backup", Body: "HTTP remote edit"})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPut, "/agents/"+id+"/skills/backup?project_id="+url.QueryEscape(projectID), bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		args.Operation = "archive"
		_, err = actAgentSkillOperation(h, caller, args)
		require.NoError(t, err)
		data, err := os.ReadFile(localFile)
		require.NoError(t, err)
		require.Equal(t, "Local skill must survive", string(data))
		data, err = os.ReadFile(filepath.Join(project.RepoPath, ".openvibely", "agents", "reviewer", "skills", "backup", "SKILL.md"))
		require.NoError(t, err)
		require.Contains(t, string(data), "archived: true")
	})

	t.Run("channel settings and senders stay remote", func(t *testing.T) {
		owner.slackSvc = nil
		owner.discordSvc = nil
		owner.emailService = nil
		for _, item := range []struct {
			provider, key string
			values        url.Values
		}{
			{"slack", service.SlackSettingClientID, url.Values{"slack_client_id": {"remote-client"}, "slack_client_secret": {"test-secret"}, "slack_app_token": {"test-token"}}},
			{"discord", service.DiscordSettingBotToken, url.Values{"discord_bot_token": {"test-remote-token"}}},
			{"email", service.EmailSettingAddress, url.Values{"email_provider": {"gmail"}, "email_address": {"remote@example.com"}, "email_password": {"test-password"}}},
		} {
			require.NoError(t, h.settingsRepo.Set(ctx, item.key, "Local unchanged"))
			_, err := actChannelConfigure(h, caller, channelConfigureArgs{ProjectID: projectID, Provider: item.provider, Values: item.values})
			require.NoError(t, err)
			got, err := h.settingsRepo.Get(ctx, item.key)
			require.NoError(t, err)
			require.Equal(t, "Local unchanged", got)
			got, err = owner.settingsRepo.Get(ctx, item.key)
			require.NoError(t, err)
			require.NotEmpty(t, got)
			require.NotEqual(t, "Local unchanged", got)
			_, err = actChannelsBulk(h, caller, cardBulkArgs{ProjectID: projectID, IDs: []string{"channel:" + item.provider}})
			require.NoError(t, err)
			got, err = h.settingsRepo.Get(ctx, item.key)
			require.NoError(t, err)
			require.Equal(t, "Local unchanged", got)
		}
		for _, item := range []struct{ provider, key, value string }{{"slack", "slack_user_id", "U123"}, {"telegram", "user_id_or_username", "123"}, {"discord", "discord_user_id", "123"}, {"email", "authorized_email_address", "remote@example.com"}} {
			_, err := actChannelSender(h, caller, channelSenderArgs{ProjectID: projectID, Provider: item.provider, Operation: "create", Values: url.Values{item.key: {item.value}}})
			require.NoError(t, err)
		}
		users, err := local.Stores.SlackAuth.ListByProject(ctx, projectID)
		require.NoError(t, err)
		require.Empty(t, users)
		users, err = owner.slackAuthRepo.ListByProject(ctx, project.ID)
		require.NoError(t, err)
		require.Len(t, users, 1)
	})

	t.Run("repository health uses remote disk", func(t *testing.T) {
		health, err := actProjectPathHealth(h, caller, projectPathArgs{ProjectID: projectID})
		require.NoError(t, err)
		require.Equal(t, models.RepoPathHealthHealthy, health.Status)
		response := httptest.NewRecorder()
		e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/projects/"+projectID+"/edit", nil))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.NotContains(t, response.Body.String(), "Choose Folder")

	})

	t.Run("HTTP model management stays local and remote task model reads remain available", func(t *testing.T) {
		response := httptest.NewRecorder()
		request := serversRequest(http.MethodPost, "/models?project_id="+url.QueryEscape(projectID), url.Values{"name": {"HTTP local model"}, "provider": {"anthropic"}, "anthropic_auth_type": {"api_key"}, "model": {"claude-sonnet-4-5-20250929"}, "api_key": {"test-key"}})
		request.Header.Set("HX-Request", "true")
		e.ServeHTTP(response, request)
		require.Less(t, response.Code, 400, response.Body.String())
		configs, err := h.llmConfigRepo.List(ctx)
		require.NoError(t, err)
		found := false
		for _, config := range configs {
			if config.Name == "HTTP local model" {
				found = true
			}
		}
		require.True(t, found)
		configs, err = owner.llmConfigRepo.List(ctx)
		require.NoError(t, err)
		for _, config := range configs {
			require.NotEqual(t, "HTTP local model", config.Name)
		}
		oauth := &models.LLMConfig{Name: "Remote connected model", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, Model: "claude-sonnet-4-5-20250929", OAuthAccessToken: "never-send-this", OAuthExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
		require.NoError(t, owner.llmConfigRepo.Create(ctx, oauth))
		remoteAccounts, err := owner.llmConfigRepo.ListOAuthConnections(ctx, "")
		require.NoError(t, err)
		require.NotEmpty(t, remoteAccounts)

		read, err := h.llmConfigRepo.GetByID(caller, remoteid.ID(sid, oauth.ID))
		require.NoError(t, err)
		require.Empty(t, read.OAuthAccessToken)
		require.NotNil(t, read.RemoteOAuthConnected)
		require.True(t, *read.RemoteOAuthConnected)
		localAccountModel := &models.LLMConfig{Name: "Local account model", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, Model: oauth.Model,
			PendingOAuthConnection: &models.PendingModelOAuth{OAuthConnection: models.OAuthConnection{Provider: models.ProviderAnthropic, AccessToken: "local-test-token", RefreshToken: "local-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}}}
		require.NoError(t, local.Stores.LLMConfigs.Create(ctx, localAccountModel))
		accounts, err := local.Stores.LLMConfigs.ListOAuthConnections(ctx, "")
		require.NoError(t, err)
		require.NotEmpty(t, accounts)
		for _, scope := range []string{projectID, models.AllProjectsID, models.SharedProjectID("github.com/team/project")} {
			response = httptest.NewRecorder()
			request = httptest.NewRequest(http.MethodGet, "/models?project_id="+url.QueryEscape(scope), nil)
			request.Header.Set("HX-Request", "true")
			e.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "HTTP local model")
			require.Contains(t, response.Body.String(), accounts[0].ID)
			require.NotContains(t, response.Body.String(), remoteid.ID(sid, oauth.ID))
			require.NotContains(t, response.Body.String(), "Remote connected model")
			for _, account := range remoteAccounts {
				require.NotContains(t, response.Body.String(), account.ID)
			}

		}
		for _, route := range []string{
			"/models/" + remoteid.ID(sid, oauth.ID) + "/edit-details",
			"/models/" + remoteid.ID(sid, oauth.ID) + "/oauth/status",
		} {
			response = httptest.NewRecorder()
			e.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route, nil))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		}

		for _, route := range []string{
			"/models/" + remoteid.ID(sid, oauth.ID),
			"/models/" + remoteid.ID(sid, oauth.ID) + "/set-default",
			"/models/" + remoteid.ID(sid, oauth.ID) + "/oauth/disconnect",
			"/models/oauth-connections/" + remoteid.ID(sid, "account") + "/rename",
			"/models/oauth-connections/" + remoteid.ID(sid, "account") + "/delete",
		} {
			response = httptest.NewRecorder()
			e.ServeHTTP(response, serversRequest(http.MethodPost, route, nil))
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		}
		// Remote models remain readable for tasks, but cannot be edited here.
		response = httptest.NewRecorder()
		e.ServeHTTP(response, serversRequest(http.MethodPost, "/models", url.Values{
			"model_config_id": {remoteid.ID(sid, oauth.ID)},
			"name":            {"Renamed remote model"}, "provider": {"anthropic"},
			"anthropic_auth_type": {"oauth"}, "model": {oauth.Model},
		}))
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		updated, err := owner.llmConfigRepo.GetByID(ctx, oauth.ID)
		require.NoError(t, err)
		require.Equal(t, "Remote connected model", updated.Name)
		localCopy, err := local.Stores.LLMConfigs.GetByID(ctx, oauth.ID)
		require.NoError(t, err)
		require.Nil(t, localCopy)
	})

	t.Run("generation uses remote service availability", func(t *testing.T) {
		oldDiscover := discoverPluginStateFn
		discoverPluginStateFn = func(context.Context) (models.PluginState, error) { return models.PluginState{}, nil }
		defer func() { discoverPluginStateFn = oldDiscover }()
		owner.llmSvc = nil
		response := httptest.NewRecorder()
		e.ServeHTTP(response, serversRequest(http.MethodPost, "/agents/generate?project_id="+url.QueryEscape(projectID), url.Values{"description": {"Review project changes"}, "scope": {"project"}}))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "LLM service unavailable")
	})

	t.Run("denied mutations do not touch either instance", func(t *testing.T) {
		owner.SetOrganizationService(service.NewOrganizationService(agentDeleteMemberStore{owner.remoteExecutor.Stores.Organizations}, "secret"))
		_, err := actChannelConfigure(h, caller, channelConfigureArgs{ProjectID: projectID, Provider: "discord", Values: url.Values{"discord_bot_token": {"denied"}}})
		require.Error(t, err)
		got, err := owner.settingsRepo.Get(ctx, service.DiscordSettingBotToken)
		require.NoError(t, err)
		require.Empty(t, got)
		got, err = h.settingsRepo.Get(ctx, service.DiscordSettingBotToken)
		require.NoError(t, err)

		require.Equal(t, "Local unchanged", got)
		oldUninstall := uninstallPluginFn
		calls := 0
		uninstallPluginFn = func(context.Context, string) error { calls++; return nil }
		defer func() { uninstallPluginFn = oldUninstall }()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/agents/plugins/uninstall?scope=project&project_id="+url.QueryEscape(projectID), strings.NewReader(`{"plugin_id":"reviewer@team"}`))
		request.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(response, request)
		require.GreaterOrEqual(t, response.Code, 400, response.Body.String())
		require.Zero(t, calls, "permission denial must precede local or remote file operations")
	})
	t.Run("disconnection never falls back to Local", func(t *testing.T) {
		hub.Shutdown()
		_, err := actChannelConfigure(h, caller, channelConfigureArgs{ProjectID: projectID, Provider: "discord", Values: url.Values{"discord_bot_token": {"disconnected"}}})
		require.Error(t, err)
		got, err := h.settingsRepo.Get(ctx, service.DiscordSettingBotToken)
		require.NoError(t, err)
		require.Equal(t, "Local unchanged", got)
	})

}
