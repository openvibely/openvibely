package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessServiceGatesPrivateAndNotifiesChanges(t *testing.T) {
	env := teststores.New(t)
	org := NewOrganizationService(env.Organizations, "test-secret")
	ctx := t.Context()
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, token, "bob", "bob-long-password", "")
	require.NoError(t, err)
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	project := &models.Project{Name: "Policy"}
	require.NoError(t, env.Projects.Create(ownerCtx, project))
	s := NewProjectAccessService(env.ProjectAccess)
	changes := s.Changes()
	policy, err := s.Policy(ownerCtx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.ErrorIs(t, s.Update(ownerCtx, *policy), authz.ErrSandboxRequired)
	select {
	case <-changes:
		t.Fatal("rejected change notified subscribers")
	default:
	}
	stored, err := s.Policy(ownerCtx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "org", stored.Visibility)
	policy.Visibility = "org"
	policy.OrgRole = "viewer"
	require.ErrorIs(t, s.Update(bobCtx, *policy), authz.ErrDenied)
	require.NoError(t, s.Update(ownerCtx, *policy))
	select {
	case <-changes:
	default:
		t.Fatal("policy change did not notify subscribers")
	}
	require.NoError(t, s.Require(bobCtx, project.ID, authz.View))
	require.ErrorIs(t, s.Require(bobCtx, project.ID, authz.Edit), authz.ErrDenied)
	changes = s.Changes()
	require.NoError(t, s.SetMember(ownerCtx, project.ID, bob.UserID, "editor"))
	select {
	case <-changes:
	default:
		t.Fatal("member change did not notify subscribers")
	}
	require.NoError(t, s.Require(bobCtx, project.ID, authz.Edit))
	_, err = s.Audit(bobCtx, project.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = s.Members(bobCtx, project.ID)
	require.ErrorIs(t, err, authz.ErrDenied)
	events, err := s.Audit(ownerCtx, project.ID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, bob.UserID, "", true))
	require.ErrorIs(t, s.Require(bobCtx, project.ID, authz.View), authz.ErrDenied)
	// Rejoining restores the org-wide viewer floor, not a previous editor grant.
	token, err = org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	_, err = org.Accept(ctx, token, "", "", bob.UserID)
	require.NoError(t, err)
	require.NoError(t, s.Require(bobCtx, project.ID, authz.View))
	require.ErrorIs(t, s.Require(bobCtx, project.ID, authz.Edit), authz.ErrDenied)
	events, err = s.Audit(ownerCtx, project.ID)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, "project.member.removed_with_org", events[0].Action)
}
