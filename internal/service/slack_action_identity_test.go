package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestSlackRuntimeRechecksSenderBinding(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "slack-action-user", "long-password-value", "")
	require.NoError(t, err)
	caller := models.WithPrincipal(ctx, user.Principal)
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := &SlackService{workerSvc: worker, slackAuthRepo: env.SlackAuth, projectRepo: env.Projects, slackUserProjectRepo: env.SlackUserProjects}
	entry := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, env.SlackAuth.Create(owner, entry))
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", entry.ID, "T123", user.UserID))
	handlers := svc.slackActionHandlers("default", slackActionContext{TeamID: "T123", UserID: "U123"}, nil)
	require.NotEmpty(t, handlers)
	// Missing and substituted principals are rejected before any handler runs,
	// even for domains whose other dependencies are intentionally absent here.
	for name, handler := range handlers {
		_, err := handler(ctx, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
		_, err = handler(owner, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
	_, err = handlers["get_current_project"](caller, nil)
	require.NoError(t, err)
	policy, err := env.ProjectAccess.Get(ctx, "default")
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	_, err = handlers["get_current_project"](caller, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	policy.OrgRole = "editor"
	require.NoError(t, env.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", entry.ID, "T123", models.BootstrapUserID))
	_, err = handlers["get_current_project"](caller, nil)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, env.SlackAuth.DeleteForProject(owner, "default", entry.ID))
	for name, handler := range handlers {
		_, err := handler(caller, json.RawMessage(`{}`))
		require.ErrorIs(t, err, authz.ErrDenied, name)
	}
}

func TestSlackSwitchReusesGlobalBinding(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	target := &models.Project{Name: "Destination"}
	require.NoError(t, env.Projects.Create(ctx, target))
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc := &SlackService{workerSvc: worker, slackAuthRepo: env.SlackAuth, projectRepo: env.Projects, slackUserProjectRepo: env.SlackUserProjects, userProjects: map[string]string{"T123:U123": "default"}}
	source := &models.SlackAuthorizedUser{ProjectID: "default", SlackUserID: "U123"}
	require.NoError(t, env.SlackAuth.Create(owner, source))
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, "default", source.ID, "T123", models.BootstrapUserID))
	handlers := svc.slackActionHandlers("default", slackActionContext{TeamID: "T123", UserID: "U123"}, nil)
	_, err := handlers["switch_project"](owner, json.RawMessage(`{"project":"Destination"}`))
	require.NoError(t, err)
	active, err := svc.getActiveProject(ctx, "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, target.ID, active)
	// Reset selection for the admission-race check below.
	svc.userProjects["T123:U123"] = "default"
	require.NoError(t, env.SlackUserProjects.SetUserProject(ctx, "T123", "U123", "default"))
	destination := source
	// Revoke after the action guard succeeds but before its persistence call.
	svc.slackUserProjectRepo = &slackSelectionRevocationStore{SlackUserProjectStore: env.SlackUserProjects, before: func() { require.NoError(t, env.SlackAuth.DeleteForProject(owner, target.ID, destination.ID)) }}
	_, err = handlers["switch_project"](owner, json.RawMessage(`{"project":"Destination"}`))
	require.Error(t, err)
	active, err = svc.getActiveProject(ctx, "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, "default", active)
	stored, err := env.SlackUserProjects.GetUserProject(ctx, "T123", "U123")
	require.NoError(t, err)
	require.NotEqual(t, target.ID, stored)
	svc.slackUserProjectRepo = env.SlackUserProjects
	require.NoError(t, env.SlackAuth.Create(owner, destination))
	require.NoError(t, env.SlackAuth.BindApplicationUser(owner, target.ID, destination.ID, "T123", models.BootstrapUserID))

	_, err = handlers["switch_project"](owner, json.RawMessage(`{"project":"Destination"}`))
	require.NoError(t, err)
	active, err = svc.getActiveProject(ctx, "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, target.ID, active)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	require.NoError(t, env.SlackAuth.DeleteForProject(owner, "default", destination.ID))
	_, err = handlers["get_current_project"](ctx, nil)
	require.NoError(t, err)
}

type slackSelectionRevocationStore struct {
	persistence.SlackUserProjectStore
	before func()
}

func (s *slackSelectionRevocationStore) SetUserProject(ctx context.Context, teamID, userID, projectID string) error {
	s.before()
	return s.SlackUserProjectStore.SetUserProject(ctx, teamID, userID, projectID)
}
