package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type identityXAPI struct {
	*fakeXAPI
	calls int
}

func (a *identityXAPI) Me(ctx context.Context) (XUser, error) { a.calls++; return a.fakeXAPI.Me(ctx) }

func TestXRequestAndRestartIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx, svc, settings, _, _, _, _ := setupXServiceTestWithStores(t, env)
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc.SetCredentialAuthorization(worker)
	api := &identityXAPI{fakeXAPI: &fakeXAPI{me: XUser{ID: "bot"}}}
	svc.SetAPI(api)
	_, _, err := svc.PrepareConnection(ctx)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, svc.Start(), authz.ErrDenied)
	require.Zero(t, api.calls)
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "installer", "long-password-value", "")
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, user.Principal)
	_, _, err = svc.PrepareConnection(actor)
	require.NoError(t, err)
	require.NoError(t, settings.Set(actor, XSettingRunAsUserID, user.UserID))
	// A fresh process resolves the saved user without a browser identity.
	restored := NewXService(svc.credentials, settings, nil, nil, nil, nil, nil, nil)
	restored.SetCredentialAuthorization(worker)
	restored.SetAPI(api)
	runCtx, err := restored.startupPrincipal(ctx)
	require.NoError(t, err)
	principal, ok := models.PrincipalFromContext(runCtx)
	require.True(t, ok)
	require.Equal(t, user.UserID, principal.UserID)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	before := api.calls
	// An owner's request cannot substitute for the saved installer's revoked role.
	_, err = restored.startupPrincipal(models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID}))
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, restored.Start(), authz.ErrDenied)
	require.ErrorIs(t, svc.StartVerified(XUser{ID: "bot"}), authz.ErrDenied)
	require.ErrorIs(t, svc.pollOnce(ctx), authz.ErrDenied)
	require.Equal(t, before, api.calls)
	require.False(t, svc.Status().Running)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	// Auth-off does not replace an explicitly saved, revoked installer.
	_, err = restored.startupPrincipal(ctx)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.NoError(t, settings.Set(ctx, XSettingRunAsUserID, ""))
	runCtx, err = restored.startupPrincipal(ctx)
	require.NoError(t, err)
	principal, ok = models.PrincipalFromContext(runCtx)
	require.True(t, ok)
	require.Equal(t, models.BootstrapUserID, principal.UserID)
}

func TestXPollRevocationDuringProviderWork(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "returned page"
		if blocked {
			name = "blocked request"
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t)
			ctx, svc, settings, _, _, _, _ := setupXServiceTestWithStores(t, env)
			org := NewOrganizationService(env.Organizations, "secret")
			invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
			require.NoError(t, err)
			user, err := org.Accept(ctx, invite, "installer", "long-password-value", "")
			require.NoError(t, err)
			require.NoError(t, settings.SetMany(ctx, map[string]string{XSettingRunAsUserID: user.UserID, XSettingConfigurationID: "generation", XSettingSinceID: "10"}))
			worker := &WorkerService{}
			worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
			svc.SetCredentialAuthorization(worker)
			svc.SetConfigurationID("generation")
			svc.me = XUser{ID: "bot"}
			api := &fakeXAPI{}
			calls := 0
			api.mentionsFunc = func(callCtx context.Context, _, _, _ string) (xMentionsResponse, error) {
				calls++
				require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
				if blocked {
					select {
					case <-callCtx.Done():
						return xMentionsResponse{}, callCtx.Err()
					case <-time.After(5 * time.Second):
						t.Error("provider request was not cancelled")
						return xMentionsResponse{}, context.DeadlineExceeded
					}
				}
				// Invalid data must not be processed after revocation; cursor must not advance.
				page := xMentionsResponse{Data: []XTweet{{ID: "20"}}}
				page.Meta.NewestID = "20"
				page.Meta.NextToken = "next"
				return page, nil
			}
			svc.SetAPI(api)
			err = svc.pollOnce(ctx)
			if blocked {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, authz.ErrDenied)
			}
			require.Equal(t, 1, calls)
			cursor, err := settings.Get(ctx, XSettingSinceID)
			require.NoError(t, err)
			require.Equal(t, "10", cursor)
			pagination, err := settings.Get(ctx, XSettingPaginationState)
			require.NoError(t, err)
			require.Empty(t, pagination)
		})
	}
}

func TestXIngressDoesNotInheritInstallerIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx, svc, _, allowlist, _, project, _ := setupXServiceTestWithStores(t, env)
	svc.me = XUser{ID: "bot"}
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	svc.SetCredentialAuthorization(worker)
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	entry := &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123"}
	require.NoError(t, allowlist.Create(ctx, entry))
	tweet := XTweet{ID: "100", AuthorID: "123"}
	ok, err := svc.processMention(owner, tweet, XUser{ID: "123"})
	require.False(t, ok)
	require.Error(t, err)
	require.NoError(t, allowlist.Delete(ctx, project.ID, entry.ID))
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "sender", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "editor"))
	require.NoError(t, allowlist.Create(owner, &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123", ApplicationUserID: user.UserID}))
	ok, err = svc.processMention(owner, tweet, XUser{ID: "456"})
	require.False(t, ok)
	require.Error(t, err)
	ok, err = svc.processMention(owner, tweet, XUser{ID: "123"})
	require.True(t, ok)
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	// Completed receipt reuse must not restore a removed sender's permission.
	ok, err = svc.processMention(owner, tweet, XUser{ID: "123"})
	require.False(t, ok)
	require.Error(t, err)
}
