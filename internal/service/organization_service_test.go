package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestOrganizationAccountsInvitesSessionsAndPreferences(t *testing.T) {
	env := teststores.New(t)
	s := NewOrganizationService(env.Organizations, "secret")
	ctx := t.Context()
	require.NoError(t, s.Bootstrap(ctx, "alice", "alice-password-long", true))
	require.NoError(t, s.Bootstrap(ctx, "ignored", "ignored-password", true))
	owner, err := env.Organizations.UserByUsername(ctx, "alice")
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, "owner", owner.Role)
	require.NotEqual(t, "alice-password-long", owner.PasswordHash)
	b, err := json.Marshal(owner)
	require.NoError(t, err)
	require.NotContains(t, string(b), owner.PasswordHash)
	token, err := s.Login(ctx, "alice", "alice-password-long", time.Hour)
	require.NoError(t, err)
	authenticated, err := s.Session(ctx, token)
	require.NoError(t, err)
	require.Equal(t, owner.UserID, authenticated.UserID)
	_, err = s.Login(ctx, "alice", "wrong", time.Hour)
	require.ErrorIs(t, err, ErrOrgLogin)
	require.NoError(t, s.Logout(ctx, token))
	authenticated, err = s.Session(ctx, token)
	require.NoError(t, err)
	require.Nil(t, authenticated)
	invite, err := s.Invite(ctx, owner.UserID, "member")
	require.NoError(t, err)
	bob, err := s.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	require.Equal(t, "member", bob.Role)
	_, err = s.Accept(ctx, invite, "mallory", "mallory-password", "")
	require.ErrorIs(t, err, persistence.ErrInvitationInvalid)
	_, err = s.Invite(ctx, bob.UserID, "admin")
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
	require.Error(t, s.Bootstrap(ctx, "", "", false))
	require.ErrorIs(t, env.Organizations.ChangeMember(ctx, owner.UserID, owner.UserID, "member", false), persistence.ErrLastOwner)
	ownerCtx := models.WithPrincipal(ctx, owner.Principal)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	require.NoError(t, env.Settings.Set(ownerCtx, "ui.theme", "dark"))
	require.NoError(t, env.Settings.Set(bobCtx, "ui.theme", "light"))
	value, err := env.Settings.Get(ownerCtx, "ui.theme")
	require.NoError(t, err)
	require.Equal(t, "dark", value)
	value, err = env.Settings.Get(bobCtx, "ui.theme")
	require.NoError(t, err)
	require.Equal(t, "light", value)
	require.NoError(t, env.Settings.Set(ownerCtx, "integration.test", "shared"))
	value, err = env.Settings.Get(bobCtx, "integration.test")
	require.NoError(t, err)
	require.Equal(t, "shared", value)
	bobToken, err := s.Login(ctx, "bob", "bob-password-long", time.Hour)
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, owner.UserID, bob.UserID, "", true))
	user, err := s.Session(ctx, bobToken)
	require.NoError(t, err)
	require.Nil(t, user)
	_, err = s.Login(ctx, "bob", "bob-password-long", time.Hour)
	require.ErrorIs(t, err, ErrOrgLogin)
	require.NoError(t, s.Bootstrap(ctx, "", "", false))
	// Expired sessions and invitations fail closed.
	require.NoError(t, env.Organizations.CreateSession(ctx, s.tokenHash("expired"), owner.UserID, time.Now().Add(-time.Hour).Unix()))
	user, err = env.Organizations.SessionUser(ctx, s.tokenHash("expired"), time.Now().Unix())
	require.NoError(t, err)
	require.Nil(t, user)
	expired, _ := orgToken()
	require.NoError(t, env.Organizations.CreateInvitation(ctx, owner.UserID, "expired", s.tokenHash(expired), "member", time.Now().Add(-time.Hour).Unix()))
	_, err = s.Accept(ctx, expired, "another", "another-password", "")
	require.ErrorIs(t, err, persistence.ErrInvitationInvalid)
}

func TestOrganizationInvitationConsumptionIsAtomic(t *testing.T) {
	env := teststores.New(t)
	s := NewOrganizationService(env.Organizations, "secret")
	ctx := t.Context()
	invite, err := s.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.Accept(context.Background(), invite, name, "long-test-password", "")
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, persistence.ErrInvitationInvalid)
		}
	}
	require.Equal(t, 1, success)
	members, err := env.Organizations.Members(ctx)
	require.NoError(t, err)
	require.Len(t, members, 2)
	// Revocation and issuer demotion invalidate outstanding grants.
	invite, err = s.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	invitations, err := env.Organizations.Invitations(ctx)
	require.NoError(t, err)
	for _, i := range invitations {
		if !i.Used {
			require.NoError(t, env.Organizations.RevokeInvitation(ctx, models.BootstrapUserID, i.ID))
		}
	}
	_, err = s.Accept(ctx, invite, "revoked", "long-test-password", "")
	require.ErrorIs(t, err, persistence.ErrInvitationInvalid)
}

func TestOrganizationLastOwnerConcurrentChanges(t *testing.T) {
	env := teststores.New(t)
	s := NewOrganizationService(env.Organizations, "secret")
	ctx := t.Context()
	invite, err := s.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	second, err := s.Accept(ctx, invite, "second", "long-test-password", "")
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, second.UserID, "owner", false))
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{models.BootstrapUserID, second.UserID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			results <- env.Organizations.ChangeMember(context.Background(), id, id, "member", false)
		}(id)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.ErrorIs(t, err, persistence.ErrLastOwner)
		}
	}
	require.Equal(t, 1, success)
}

func TestOrganizationHostedOwnerBindingDoesNotAdmitSecondIdentity(t *testing.T) {
	env := teststores.New(t)
	first, err := env.Organizations.BindHostedOwner(t.Context(), "verified-subject")
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, first.UserID)
	_, err = env.Organizations.BindHostedOwner(t.Context(), "different-subject")
	require.ErrorIs(t, err, persistence.ErrOrgDenied)
}

func TestOrganizationHostedSessionRevocation(t *testing.T) {
	env := teststores.New(t)
	s := NewOrganizationService(env.Organizations, "secret")
	ctx := t.Context()
	const signedToken = "signed-hosted-cookie-validated-by-handler"
	user, err := s.HostedSession(ctx, signedToken)
	require.NoError(t, err)
	require.Nil(t, user)
	require.NoError(t, s.RegisterHostedSession(ctx, signedToken, models.BootstrapUserID, time.Now().Add(time.Hour).Unix()))
	user, err = s.HostedSession(ctx, signedToken)
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NoError(t, s.Logout(ctx, signedToken))
	user, err = s.HostedSession(ctx, signedToken)
	require.NoError(t, err)
	require.Nil(t, user)
}

func TestOrganizationPreferenceAtomicUpdatesAreUserScoped(t *testing.T) {
	env := teststores.New(t)
	s := NewOrganizationService(env.Organizations, "secret")
	ctx := t.Context()
	invite, err := s.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := s.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	bobCtx := models.WithPrincipal(ctx, bob.Principal)
	changed, err := env.Settings.CompareAndSet(bobCtx, "ui.model_favorites", "", "bob-favorites", nil)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = env.Settings.CompareAndSet(bobCtx, "ui.model_favorites", "", "stale", nil)
	require.NoError(t, err)
	require.False(t, changed)
	ownerValue, err := env.Settings.Get(ctx, "ui.model_favorites")
	require.NoError(t, err)
	require.Empty(t, ownerValue)
	bobValue, err := env.Settings.Get(bobCtx, "ui.model_favorites")
	require.NoError(t, err)
	require.Equal(t, "bob-favorites", bobValue)
	changed, err = env.Settings.CompareAndSet(bobCtx, "ui.model_favorites", "bob-favorites", "new", map[string]string{"ui.theme": "dark"})
	require.NoError(t, err)
	require.False(t, changed)
	require.Error(t, env.Settings.SetMany(bobCtx, map[string]string{"ui.theme": "dark", "shared": "value"}))
}

func TestOrganizationLoginLimitsSeparateAccountsAndSignup(t *testing.T) {
	s := NewOrganizationService(nil, "secret")
	for i := 0; i < 20; i++ {
		finish, ok := s.BeginLogin("192.0.2.1", "alice")
		require.True(t, ok)
		finish(false)
	}
	for i := 0; i < 10; i++ {
		finish, ok := s.BeginLogin("192.0.2.1", " Alice ")
		require.True(t, ok)
		finish(true)
	}
	_, ok := s.BeginLogin("192.0.2.1", "ALICE")
	require.False(t, ok)
	finish, ok := s.BeginLogin("192.0.2.1", "bob")
	require.True(t, ok)
	finish(false)
	finish, ok = s.BeginLogin("192.0.2.2", "alice")
	require.True(t, ok)
	finish(false)
	for i := 0; i < 30; i++ {
		require.True(t, s.AllowSignup("192.0.2.1"))
	}
	require.False(t, s.AllowSignup("192.0.2.1"))
	finish, ok = s.BeginLogin("192.0.2.1", "bob")
	require.True(t, ok)
	finish(false)
	// Expired failures no longer block login.
	s.mu.Lock()
	for k, v := range s.attempts {
		v.Start = time.Now().Add(-16 * time.Minute)
		s.attempts[k] = v
	}
	s.mu.Unlock()
	finish, ok = s.BeginLogin("192.0.2.1", "alice")
	require.True(t, ok)
	finish(false)
}

func TestOrganizationLoginBroaderLimitsAndReservations(t *testing.T) {
	s := NewOrganizationService(nil, "secret")
	// In-flight password checks count too; releasing a successful check frees it.
	var finishes []func(bool)
	for i := 0; i < 10; i++ {
		finish, ok := s.BeginLogin("client", "alice")
		require.True(t, ok)
		finishes = append(finishes, finish)
	}
	_, ok := s.BeginLogin("client", "alice")
	require.False(t, ok)
	finishes[0](false)
	finishes[0](false) // completion is idempotent
	finish, ok := s.BeginLogin("client", "alice")
	require.True(t, ok)
	finish(true)
	_, ok = s.BeginLogin("client", "alice")
	require.False(t, ok)
	for _, mode := range []string{"account", "ip"} {
		t.Run(mode, func(t *testing.T) {
			s := NewOrganizationService(nil, "secret")
			for i := 0; i < 100; i++ {
				ip, user := "client", "alice"
				if mode == "account" {
					ip = fmt.Sprint(i)
				} else {
					user = fmt.Sprint(i)
				}
				finish, ok := s.BeginLogin(ip, user)
				require.True(t, ok)
				finish(true)
			}
			ip, user := "client", "alice"
			if mode == "account" {
				ip = "new-client"
			} else {
				user = "new-user"
			}
			_, ok := s.BeginLogin(ip, user)
			require.False(t, ok)
		})
	}
}

func TestOrganizationTrustedProxyIP(t *testing.T) {
	for _, tc := range []struct{ name, trusted, remote, header, want string }{
		{"direct ignores spoof", "", "192.0.2.1:123", "198.51.100.1", "192.0.2.1"},
		{"untrusted peer", "127.0.0.1/32", "192.0.2.1:123", "198.51.100.1", "192.0.2.1"},
		{"trusted peer", "127.0.0.1", "127.0.0.1:123", "198.51.100.1", "198.51.100.1"},
		{"forged left prefix", "127.0.0.1", "127.0.0.1:123", "203.0.113.1, 198.51.100.1", "198.51.100.1"},
		{"trusted chain", "127.0.0.1,10.0.0.0/24", "127.0.0.1:123", "198.51.100.1, 10.0.0.2", "198.51.100.1"},
		{"malformed", "127.0.0.1", "127.0.0.1:123", "invalid", "127.0.0.1"},
		{"ipv6", "::1/128", "[::1]:123", "2001:db8::1", "2001:db8::1"},
		{"mapped peer", "127.0.0.1/32", "[::ffff:127.0.0.1]:123", "198.51.100.1", "198.51.100.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewOrganizationService(nil, "secret")
			require.NoError(t, s.ConfigureTrustedProxies(tc.trusted))
			require.Equal(t, tc.want, s.ClientIP(tc.remote, tc.header))
		})
	}
	s := NewOrganizationService(nil, "secret")
	require.Error(t, s.ConfigureTrustedProxies("not-a-network"))
}

func TestOrganizationLoginConcurrentAttempts(t *testing.T) {
	s := NewOrganizationService(nil, "secret")
	var wg sync.WaitGroup
	accepted := make(chan func(bool), 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if finish, ok := s.BeginLogin("same-client", "alice"); ok {
				accepted <- finish
			}
		}()
	}
	wg.Wait()
	close(accepted)
	count := 0
	for finish := range accepted {
		count++
		finish(false)
	}
	require.Equal(t, 10, count)
	finish, ok := s.BeginLogin("same-client", "alice")
	require.True(t, ok)
	finish(false)
}
