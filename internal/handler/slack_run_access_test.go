package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSlackRunBindingRevocation(t *testing.T) {
	for _, change := range []string{"delete", "rebind", "downgrade"} {
		t.Run(change, func(t *testing.T) {
			tc := NewStoreTestContext(t)
			h, ctx := tc.handler, t.Context()
			h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
			h.SetAuthMode(auth.AuthModeLocal)
			h.SetRunIdentityStore(tc.stores.RunIdentities)
			org := service.NewOrganizationService(tc.stores.Organizations, "secret")
			h.SetOrganizationService(org)
			token, err := org.Invite(ctx, models.BootstrapUserID, "member")
			require.NoError(t, err)
			user, err := org.Accept(ctx, token, "slack-run-user", "long-password-value", "")
			require.NoError(t, err)
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			caller := models.WithPrincipal(ctx, user.Principal)
			entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
			require.NoError(t, tc.stores.SlackAuth.Create(owner, entry))
			require.NoError(t, tc.stores.SlackAuth.BindApplicationUser(owner, "default", entry.ID, "T123", user.UserID))
			task := createTask(t, h, "default", "Slack run", func(task *models.Task) { task.Category = models.CategoryChat })
			model := models.LLMConfig{Name: "Slack", Provider: "test", Model: "test"}
			require.NoError(t, tc.stores.LLMConfigs.Create(owner, &model))
			execution := models.Execution{TaskID: task.ID, AgentConfigID: model.ID, Status: models.ExecRunning, PromptSent: "hello"}
			require.NoError(t, tc.stores.Executions.Create(caller, &execution))
			params := streamingResponseParams{ExecID: execution.ID, TaskID: task.ID, ProjectID: "default", Surface: chatcontrol.SurfaceSlack, ChannelReply: service.ChannelReplyContext{SlackTeamID: "T123", SlackUserID: "U123"}}
			require.NoError(t, h.requireFollowupAccess(caller, params))
			forged := params
			forged.ChannelReply.SlackTeamID = "other"
			require.Error(t, h.requireFollowupAccess(caller, forged))
			require.Error(t, h.requireFollowupAccess(owner, params))
			active, cancel := context.WithCancel(caller)
			defer cancel()
			h.watchFollowupAccess(active, cancel, params)
			switch change {
			case "delete":
				require.NoError(t, tc.stores.SlackAuth.DeleteForProject(owner, "default", entry.ID))
			case "rebind":
				require.NoError(t, tc.stores.SlackAuth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
			case "downgrade":
				policy, err := tc.stores.ProjectAccess.Get(ctx, "default")
				require.NoError(t, err)
				policy.OrgRole = "viewer"
				require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
			}
			select {
			case <-active.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("revoked Slack run remained active")
			}
			require.Error(t, h.requireFollowupAccess(caller, params))
		})
	}
}

func TestChannelRunnerCarriesPrincipal(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	h.startStreamingResponseOverride = func(params streamingResponseParams) error {
		require.Equal(t, "user:sender", params.PrincipalID)
		require.Equal(t, "T123", params.ChannelReply.SlackTeamID)
		return nil
	}
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "sender"})
	h.StartChannelChatRun(ctx, service.ChannelChatRunRequest{ExecID: "execution", TaskID: "task", ProjectID: "default", ReplyContext: service.ChannelReplyContext{Source: models.TaskOriginSlack, SlackTeamID: "T123"}})
}
